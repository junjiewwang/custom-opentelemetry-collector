// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"hash/fnv"
	"sort"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// redMetricSeries holds the aggregated RED metrics for a unique dimension set.
type redMetricSeries struct {
	dims     dimensionSet
	appID    string
	calls    counter
	latency  *histogram
	overflow atomic.Bool
	// lastSeenCycle is the flush cycle index when this series last received data.
	// Used in cumulative mode to evict stale series.
	lastSeenCycle uint64
}

// dimensionSet is a sorted, canonicalized set of key=value labels.
type dimensionSet struct {
	keys   []string
	values []string
	hash   uint64
}

// Lookup returns the value for a dimension key, ok=false if absent.
// keys is sorted (see newDimensionSet), but a linear scan is fine — dimension
// sets are small (handful of keys).
func (ds dimensionSet) Lookup(key string) (string, bool) {
	for i, k := range ds.keys {
		if k == key {
			return ds.values[i], true
		}
	}
	return "", false
}

func newDimensionSet(attrs map[string]string) dimensionSet {
	ds := dimensionSet{
		keys:   make([]string, 0, len(attrs)),
		values: make([]string, 0, len(attrs)),
	}
	for k := range attrs {
		ds.keys = append(ds.keys, k)
	}
	sort.Strings(ds.keys)
	ds.values = make([]string, len(ds.keys))
	h := fnv.New64a()
	for i, k := range ds.keys {
		ds.values[i] = attrs[k]
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(ds.values[i]))
		h.Write([]byte{0})
	}
	ds.hash = h.Sum64()
	return ds
}

// REDGenerator processes spans and aggregates RED (Rate/Error/Duration) metrics.
//
// Metrics produced (aligned with Tempo MetricGenerator naming):
//
//	traces_spanmetrics_calls_total  — Counter
//	traces_spanmetrics_latency       — Histogram (sum/count/buckets)
type REDGenerator struct {
	config    *REDConfig
	cardLimit int
	mu        sync.RWMutex
	series    map[uint64]*redMetricSeries
	dropped   atomic.Int64
	cycle     atomic.Int64 // flush cycle index, used for stale-series eviction in cumulative mode
}

// NewREDGenerator creates a new REDGenerator.
func NewREDGenerator(config *REDConfig, cardinalityLimit int) *REDGenerator {
	return &REDGenerator{
		config:    config,
		cardLimit: cardinalityLimit,
		series:    make(map[uint64]*redMetricSeries),
	}
}

// ProcessSpan aggregates a single span into the RED metrics.
func (g *REDGenerator) ProcessSpan(svcName, appID string, resource pcommon.Resource, span ptrace.Span) {
	if !g.config.Enabled {
		return
	}

	attrs := span.Attributes()
	dims := make(map[string]string, len(g.config.Dimensions)+3)

	dims["service.name"] = svcName
	dims["span.name"] = span.Name()
	dims["span.kind"] = span.Kind().String()
	dims["status.code"] = span.Status().Code().String()

	for _, d := range g.config.Dimensions {
		if v, ok := attrs.Get(d); ok && v.Str() != "" {
			dims[d] = v.Str()
		} else if v, ok := resource.Attributes().Get(d); ok && v.Str() != "" {
			dims[d] = v.Str()
		}
	}

	ds := newDimensionSet(dims)
	series := g.getOrCreateSeries(ds, appID)

	if series.overflow.Load() {
		return
	}

	series.calls.Add(1)
	series.latency.Record(spanDuration(span))
	// Mark this series as active in the current flush cycle (cumulative-mode eviction).
	atomic.StoreUint64(&series.lastSeenCycle, uint64(g.cycle.Load()))
}

// getOrCreateSeries returns the series for the given dimensions, creating one
// if it doesn't exist (subject to cardinality limits).
func (g *REDGenerator) getOrCreateSeries(ds dimensionSet, appID string) *redMetricSeries {
	// Fast path: read lock.
	g.mu.RLock()
	s, ok := g.series[ds.hash]
	g.mu.RUnlock()
	if ok && s.dims.hash == ds.hash {
		return s
	}

	// Slow path: write lock.
	g.mu.Lock()
	defer g.mu.Unlock()

	// Double-check.
	if s, ok = g.series[ds.hash]; ok && s.dims.hash == ds.hash {
		return s
	}

	// Cardinality limit check.
	if len(g.series) >= g.cardLimit {
		g.dropped.Add(1)
		// Don't store overflow series in the map — they'd consume a slot.
		p := &redMetricSeries{dims: ds, latency: newHistogram(g.config.Histogram.Buckets)}
		p.overflow.Store(true)
		return p
	}

	s = &redMetricSeries{
		dims:    ds,
		appID:   appID,
		latency: newHistogram(g.config.Histogram.Buckets),
	}
	g.series[ds.hash] = s
	return s
}

// Cardinality returns the current number of active series.
func (g *REDGenerator) Cardinality() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.series)
}

// Dropped returns the number of series dropped due to cardinality limits.
func (g *REDGenerator) Dropped() int64 {
	return g.dropped.Load()
}

// Collect drains all aggregated series and returns them for metric emission.
// The generator is reset after this call. Used in delta-temporality mode.
func (g *REDGenerator) Collect() []*redMetricSeries {
	g.mu.Lock()
	old := g.series
	g.series = make(map[uint64]*redMetricSeries)
	g.mu.Unlock()

	result := make([]*redMetricSeries, 0, len(old))
	for _, s := range old {
		if s.overflow.Load() {
			continue
		}
		result = append(result, s)
	}
	return result
}

// CollectCumulative returns a read-only snapshot of all active series WITHOUT
// resetting them, and evicts series that have been stale for staleCycles flush
// cycles. Used in cumulative-temporality mode so that counters are monotonically
// increasing (matching Prometheus semantics).
func (g *REDGenerator) CollectCumulative(staleCycles int) []*redMetricSeries {
	g.mu.Lock()
	defer g.mu.Unlock()

	cur := uint64(g.cycle.Load())
	result := make([]*redMetricSeries, 0, len(g.series))
	for id, s := range g.series {
		if s.overflow.Load() {
			continue
		}
		if staleCycles > 0 && cur-s.lastSeenCycle > uint64(staleCycles) {
			// Evict stale series: no new data for too long.
			delete(g.series, id)
			continue
		}
		result = append(result, s)
	}
	return result
}
