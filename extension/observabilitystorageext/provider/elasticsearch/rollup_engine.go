// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
	"go.uber.org/zap"
)

// RollupEngineConfig holds rollup engine behavior configuration.
type RollupEngineConfig struct {
	// Enabled controls whether the rollup engine runs.
	Enabled bool

	// TickInterval is how often the rollup cycle runs (default 1h).
	TickInterval time.Duration

	// ReadyAfter skips raw indices newer than this duration (default 24h) so
	// we only rollup closed, stable indices.
	ReadyAfter time.Duration
}

// ApplyDefaults fills unset fields with defaults.
func (c *RollupEngineConfig) ApplyDefaults() {
	if c.TickInterval <= 0 {
		c.TickInterval = time.Hour
	}
	if c.ReadyAfter <= 0 {
		c.ReadyAfter = 24 * time.Hour
	}
}

// RollupEngine periodically aggregates raw 1m metric documents into the 5m
// rollup tier. It is a single-node engine: the distributed claim/watermark
// coordination is handled by lifecycle.RollupLock so multiple replicas do not
// double-aggregate or re-run already-processed slices.
type RollupEngine struct {
	config     RollupEngineConfig
	client     *Client
	aggregator *RollupAggregator
	writer     *MetricWriter
	lock       *lifecycle.RollupLock
	logger     *zap.Logger
	metrics    *rollupMetrics

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewRollupEngine creates a RollupEngine.
func NewRollupEngine(cfg RollupEngineConfig, client *Client, agg *RollupAggregator, writer *MetricWriter, lock *lifecycle.RollupLock, logger *zap.Logger) *RollupEngine {
	return &RollupEngine{
		config:     cfg,
		client:     client,
		aggregator: agg,
		writer:     writer,
		lock:       lock,
		logger:     logger.Named("rollup-engine"),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// SetMetrics injects the rollup self-monitoring instruments into the engine and
// aggregator (nil-safe).
func (e *RollupEngine) SetMetrics(m *rollupMetrics) {
	e.metrics = m
	e.aggregator.setMetrics(m)
}

// Start launches the background rollup loop.
func (e *RollupEngine) Start(ctx context.Context) {
	if !e.config.Enabled {
		e.logger.Info("rollup engine disabled")
		close(e.doneCh)
		return
	}
	go e.loop()
}

// Stop signals the loop to stop and waits for it to exit.
func (e *RollupEngine) Stop() {
	close(e.stopCh)
	<-e.doneCh
}

func (e *RollupEngine) loop() {
	defer close(e.doneCh)
	// Run once immediately, then on tick.
	e.tick(context.Background())

	ticker := time.NewTicker(e.config.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.tick(context.Background())
		}
	}
}

// rollupWorkItem is one (appID, date, hour) slice to roll up.
// Hour granularity lets different replicas claim different hours of the same
// app/day, so rollup work genuinely parallelizes across replicas (each hour
// is an independent 5m-bucket aggregation).
type rollupWorkItem struct {
	appID   string
	date    time.Time
	hour    int       // 0..23 (UTC hour of the day)
	indices []string
}

// sliceKey builds the deterministic claim-slice identifier for a work item.
func (w *rollupWorkItem) sliceKey() string {
	return fmt.Sprintf("%s:%02d", w.date.Format("2006.01.02"), w.hour)
}

// tick runs one rollup cycle: list ready raw indices, aggregate each pending
// hour slice, write rollup docs, advance watermark.
func (e *RollupEngine) tick(ctx context.Context) {
	start := time.Now()
	defer func() {
		e.logger.Debug("rollup cycle complete", zap.Duration("elapsed", time.Since(start)))
	}()

	// Enumerate ready raw metric indices older than ReadyAfter.
	rawPrefix := signalPrefix(e.writer.config, "metric")
	readyBefore := time.Now().UTC().Add(-e.config.ReadyAfter)
	indices, err := e.client.ListIndices(ctx, rawPrefix+"-*")
	if err != nil {
		e.logger.Warn("rollup list indices failed", zap.Error(err))
		return
	}

	// Group indices by (appID, date), filter to ready ones. Each date expands
	// into 24 hour-slices so replicas can claim individual hours in parallel.
	byKey := make(map[string]*rollupWorkItem)
	for _, idx := range indices {
		date, ok := extractIndexDate(idx, e.writer.config.Metrics.IndexDateFormat)
		if !ok {
			continue
		}
		if !date.Before(readyBefore) {
			continue
		}
		appID := extractAppID(idx, rawPrefix)
		key := appID + "|" + date.Format("2006.01.02")
		it, ok := byKey[key]
		if !ok {
			it = &rollupWorkItem{appID: appID, date: date}
			byKey[key] = it
		}
		it.indices = append(it.indices, idx)
	}

	if len(byKey) == 0 {
		return
	}

	// Expand each (appID, date) into 24 hour work items and process them.
	var work []*rollupWorkItem
	for _, day := range byKey {
		for hour := 0; hour < 24; hour++ {
			work = append(work, &rollupWorkItem{
				appID:   day.appID,
				date:    day.date,
				hour:    hour,
				indices: day.indices,
			})
		}
	}

	for _, item := range work {
		if err := e.processItem(ctx, item); err != nil {
			e.logger.Warn("rollup item failed",
				zap.String("appID", item.appID),
				zap.Time("date", item.date),
				zap.Int("hour", item.hour),
				zap.Error(err),
			)
		}
	}
}

// processItem aggregates one (appID, date, hour) slice and writes rollup docs.
func (e *RollupEngine) processItem(ctx context.Context, item *rollupWorkItem) error {
	slice := item.sliceKey()

	// Claim the hour slice. If another replica owns it, skip.
	if e.lock != nil {
		claimed, err := e.lock.TryClaimSlice(ctx, RollupTier5m, item.appID, slice, 2*e.config.TickInterval)
		if err != nil {
			return fmt.Errorf("claim slice: %w", err)
		}
		if !claimed {
			e.logger.Debug("slice already claimed by another node, skipping",
				zap.String("appID", item.appID),
				zap.String("slice", slice),
			)
			return nil
		}
		defer e.lock.ReleaseClaimSlice(ctx, RollupTier5m, item.appID, slice)
	}

	// Aggregate this single hour into 5m buckets (12 buckets/hour).
	hourStart := item.date.Add(time.Duration(item.hour) * time.Hour)
	hourEnd := hourStart.Add(time.Hour - time.Millisecond)
	startTime := time.Now()
	points, err := e.aggregator.AggregateSlice(ctx, item.appID, item.indices, hourStart, hourEnd)
	if err != nil {
		if e.metrics != nil {
			e.metrics.recordSlice(ctx, item.appID, 0, true, time.Since(startTime))
		}
		return fmt.Errorf("aggregate slice: %w", err)
	}
	if len(points) == 0 {
		e.logger.Debug("no data to rollup", zap.String("appID", item.appID), zap.String("slice", slice))
		if e.metrics != nil {
			e.metrics.recordSlice(ctx, item.appID, 0, false, time.Since(startTime))
		}
		return nil
	}

	if err := e.writer.WriteRollupPoints(ctx, RollupTier5m, points); err != nil {
		if e.metrics != nil {
			e.metrics.recordSlice(ctx, item.appID, 0, true, time.Since(startTime))
		}
		return fmt.Errorf("write rollup points: %w", err)
	}
	if e.metrics != nil {
		e.metrics.recordSlice(ctx, item.appID, len(points), false, time.Since(startTime))
	}
	e.logger.Info("rolled up metric slice",
		zap.String("appID", item.appID),
		zap.String("slice", slice),
		zap.Int("points", len(points)),
	)
	return nil
}

// ── index name parsing helpers ────────────────────────

// rawDatePattern matches the date suffix yyyy.MM.dd at end of an index name.
var rawDatePattern = regexp.MustCompile(`(\d{4}\.\d{2}\.\d{2})$`)

// extractIndexDate parses the date suffix from an index name using the given
// date format. Returns ok=false if no date suffix is found.
func extractIndexDate(indexName, dateFormat string) (time.Time, bool) {
	m := rawDatePattern.FindStringSubmatch(indexName)
	if len(m) < 2 {
		return time.Time{}, false
	}
	t, err := time.Parse(dateFormat, m[1])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// extractAppID extracts the appID segment from an index name of the form
// {prefix}-{appID}-{date}. appID may itself contain hyphens, so we strip the
// known prefix and the trailing date suffix, leaving the appID.
func extractAppID(indexName, prefix string) string {
	rest := indexName[len(prefix)+1:] // strip "{prefix}-"
	m := rawDatePattern.FindStringIndex(rest)
	if m == nil {
		return rest
	}
	return rest[:m[0]-1] // strip "-{date}"
}
