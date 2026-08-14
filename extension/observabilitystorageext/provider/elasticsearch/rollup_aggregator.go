// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.uber.org/zap"
)

// RollupAggregator performs metric-type-aware aggregation of raw metric
// samples into 5m rollup buckets. It reads via MetricReader.QueryFlat (which
// returns raw samples with bucket_counts/labels) and aggregates in Go — the
// same Go-side grouping pattern used by the PromQL query path, keeping the
// aggregation logic testable and ES-independent.
type RollupAggregator struct {
	reader  *MetricReader
	logger  *zap.Logger
	metrics *rollupMetrics
}

// NewRollupAggregator creates a RollupAggregator.
func NewRollupAggregator(reader *MetricReader, logger *zap.Logger) *RollupAggregator {
	return &RollupAggregator{reader: reader, logger: logger.Named("rollup-aggregator")}
}

// setMetrics injects the rollup self-monitoring instruments (nil-safe).
func (a *RollupAggregator) setMetrics(m *rollupMetrics) {
	a.metrics = m
}

// RollupTier5m is the 5-minute rollup tier identifier.
const RollupTier5m = "5m"

// bucketWidth is the fixed rollup window.
const bucketWidth = 5 * time.Minute

// AggregateSlice aggregates all metrics for appID over [start, end) into
// 5m rollup points. Each point carries a deterministic DocID.
// indices is the exact list of source indices to read from — passing them
// avoids IndexPatternForRange's ±1-day pad hitting non-existent indices for
// sparse app/day combinations.
func (a *RollupAggregator) AggregateSlice(ctx context.Context, appID string, indices []string, start, end time.Time) ([]rollupPoint, error) {
	tr := TimeRange{Start: start, End: end}

	names, err := a.reader.ListMetricNames(ctx, tr)
	if err != nil {
		return nil, fmt.Errorf("list metric names: %w", err)
	}

	// Metric type + unit determine aggregation semantics.
	types, err := a.reader.ListMetricTypes(ctx, tr)
	if err != nil {
		types = nil // fall through; treat as gauge
	}

	indexPattern := strings.Join(indices, ",")

	// Aggregate metrics concurrently with a bounded worker pool. The concurrency
	// is capped at 4 to leave headroom in ES's MaxConnsPerHost=20 pool for live
	// read/write traffic. Each metric's aggregation is independent (its own
	// QueryFlat + Go-side grouping), so results are safe to append under a mutex.
	const rollupMetricConcurrency = 4
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sem      = make(chan struct{}, rollupMetricConcurrency)
		allPoints []rollupPoint
	)
	for _, name := range names {
		meta := types[name]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			points, err := a.aggregateMetric(ctx, appID, name, meta, tr, indexPattern)
			if err != nil {
				a.logger.Warn("rollup metric failed", zap.String("metric", name), zap.Error(err))
				if a.metrics != nil {
					a.metrics.recordMetric(ctx, appID, true)
				}
				return
			}
			if a.metrics != nil {
				a.metrics.recordMetric(ctx, appID, false)
			}
			mu.Lock()
			allPoints = append(allPoints, points...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return allPoints, nil
}

// aggregateMetric aggregates one metric name over the slice time range.
// It time-slices the QueryFlat into 1h windows so each ES search stays under
// the index.max_result_window=10000 limit (a full 24h day at ~2000 docs/h
// would otherwise request Size=48000 and be rejected).
func (a *RollupAggregator) aggregateMetric(ctx context.Context, appID, name string, meta storedmodel.MetricMeta, tr TimeRange, indexPattern string) ([]rollupPoint, error) {
	// Group samples by (labelset, bucketStart) across hourly slices.
	type groupKey struct {
		labels      string
		bucketStart int64
	}
	groups := make(map[groupKey]*sampleGroup)

	const sliceHour = 1 * time.Hour
	for s := tr.Start; s.Before(tr.End); s = s.Add(sliceHour) {
		e := s.Add(sliceHour)
		if e.After(tr.End) {
			e = tr.End
		}
		flat, err := a.reader.QueryFlat(ctx, MetricFlatQuery{
			AppID:        appID,
			MetricName:   name,
			TimeRange:    TimeRange{Start: s, End: e},
			MaxDocs:      0, // adaptive: 1h → 2000, well under ES 10000 window
			IndexPattern: indexPattern,
		})
		if err != nil {
			return nil, fmt.Errorf("query flat: %w", err)
		}
		if flat == nil || len(flat.Samples) == 0 {
			continue
		}
		for _, sm := range flat.Samples {
			bs := bucketStartFor(sm.TimestampMs)
			gk := groupKey{labels: labelsKey(sm.Labels), bucketStart: bs}
			g, ok := groups[gk]
			if !ok {
				g = &sampleGroup{
					labels:     sm.Labels,
					bucketMs:   bs,
					metricType: meta.Type,
				}
				groups[gk] = g
			}
			g.add(sm)
		}
	}

	// Emit one rollup point per group.
	points := make([]rollupPoint, 0, len(groups))
	for _, g := range groups {
		doc := g.toDoc(name, appID, meta.Unit)
		points = append(points, rollupPoint{
			AppID:    appID,
			BucketMs: g.bucketMs,
			DocID:    rollupDocID(RollupTier5m, name, labelsKey(g.labels), g.bucketMs),
			Doc:      doc,
		})
	}
	return points, nil
}

// sampleGroup accumulates samples for one (labelset, bucket).
type sampleGroup struct {
	labels     map[string]string
	bucketMs   int64
	metricType string

	count     int
	sum       float64
	min       float64
	max       float64
	first     float64
	last      float64
	bucketSum []int64   // histogram bucket_counts element-wise sum
	bounds    []float64 // histogram explicit_bounds (copied from first sample)
}

// add folds one sample into the group according to metric type semantics.
func (g *sampleGroup) add(sm MetricSample) {
	// Histogram: accumulate bucket counts element-wise, retain explicit_bounds.
	if len(sm.BucketCounts) > 0 {
		if g.bucketSum == nil {
			g.bucketSum = make([]int64, len(sm.BucketCounts))
		}
		for i, bc := range sm.BucketCounts {
			g.bucketSum[i] += bc
		}
		if g.bounds == nil && len(sm.Bounds) > 0 {
			g.bounds = append([]float64(nil), sm.Bounds...)
		}
		g.count++
		return
	}

	// Number sample.
	g.count++
	g.sum += sm.Value
	if g.count == 1 {
		g.min = sm.Value
		g.max = sm.Value
		g.first = sm.Value
		g.last = sm.Value
	} else {
		if sm.Value < g.min {
			g.min = sm.Value
		}
		if sm.Value > g.max {
			g.max = sm.Value
		}
		g.last = sm.Value
	}
}

// toDoc builds a StoredMetricDataPoint with type-appropriate rollup fields.
func (g *sampleGroup) toDoc(name, appID, unit string) storedmodel.StoredMetricDataPoint {
	// service_name is stored as a TOP-LEVEL serviceName field (matching raw
	// docs), NOT inside the labels object. The raw read path (hitToSample →
	// mergeServiceName) promotes serviceName onto labels as "service_name", so it
	// arrives here inside g.labels. Extract it back to the top-level field and
	// drop it from labels, otherwise rollup docs would have an empty top-level
	// serviceName while the composite grouping (which groups on the top-level
	// serviceName field) sees a DIFFERENT label than raw — breaking the
	// rollup+raw merge for mixed queries.
	serviceName := ""
	labels := make(map[string]string, len(g.labels))
	for k, v := range g.labels {
		if k == "service_name" {
			serviceName = v
			continue
		}
		labels[k] = v
	}

	doc := storedmodel.StoredMetricDataPoint{
		TimeUnixMilli: g.bucketMs,
		Name:          name,
		Type:          g.metricType,
		AppID:         appID,
		Unit:          unit,
		Tier:          RollupTier5m,
		ServiceName:   serviceName,
		Labels:        stringMapToAny(labels),
		Count:         int64(g.count),
	}

	switch g.metricType {
	case "counter":
		// counter: first/last for rate restoration, sum for totals. NOT avg.
		// Value carries the window's LAST raw sample (the instantaneous monotonic
		// reading at window-end), NOT the sum. The read path (QueryRange) applies
		// avg/sum/max/etc. directly to `value`; a counter's `sum` over a 5m window
		// is ~5× the magnitude and varies with per-window sample count, which made
		// bare counter queries render as non-monotonic (seemingly decreasing) lines.
		// last is the only value whose avg/sum keeps counter semantics correct.
		doc.First = g.first
		doc.Last = g.last
		doc.Sum = g.sum
		doc.Value = g.last
	case "histogram":
		// histogram: merged bucket_counts + retained explicit_bounds.
		doc.BucketCounts = uint64Slice(g.bucketSum)
		doc.ExplicitBounds = g.bounds
		doc.Count = int64(g.count)
	default:
		// gauge / summary / unknown: value = avg, plus min/max/sum.
		if g.count > 0 {
			doc.Value = g.sum / float64(g.count)
		}
		doc.Min = g.min
		doc.Max = g.max
		doc.Sum = g.sum
	}

	return doc
}

// ── helpers ───────────────────────────────────────────

// bucketStartFor floors a timestamp (ms) to the 5m bucket boundary.
func bucketStartFor(tsMs int64) int64 {
	return tsMs - (tsMs % int64(bucketWidth/time.Millisecond))
}

// rollupDocID builds a deterministic _id: {tier}:{metric}:{labelsHash}:{bucketMs}.
func rollupDocID(tier, name, labelsStr string, bucketMs int64) string {
	h := md5.Sum([]byte(labelsStr))
	return fmt.Sprintf("%s:%s:%s:%d", tier, name, hex.EncodeToString(h[:8]), bucketMs)
}

// stringMapToAny converts map[string]string to map[string]any for ES doc labels.
func stringMapToAny(m map[string]string) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// uint64Slice converts []int64 to []uint64 (storedmodel histogram field type).
func uint64Slice(in []int64) []uint64 {
	out := make([]uint64, len(in))
	for i, v := range in {
		out[i] = uint64(v)
	}
	return out
}
