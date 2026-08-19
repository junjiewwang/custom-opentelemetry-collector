// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
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

	// ReadyAfter skips raw hours newer than this duration (default 2h) so we
	// only rollup closed, stable hours. It must match the read-routing boundary
	// (routeTierDecision) so a query window >2h can safely read the 5m tier for
	// everything older than this lag.
	ReadyAfter time.Duration
}

// ApplyDefaults fills unset fields with defaults.
func (c *RollupEngineConfig) ApplyDefaults() {
	if c.TickInterval <= 0 {
		c.TickInterval = time.Hour
	}
	if c.ReadyAfter <= 0 {
		c.ReadyAfter = 2 * time.Hour
	}
}

// claimTTL is the TTL for a per-slice claim lock. A slice aggregation takes
// ~10-15s, so a fixed 5-minute TTL is deliberately short: if a replica crashes
// mid-aggregation, its claim expires quickly and another replica can take over
// and complete the hour, rather than the watermark stalling for the old 2h TTL.
// A live holder keeps the lock alive via the heartbeat goroutine (see
// AcquireWithLease), so the short TTL only bounds the stale-lock window after a
// genuine crash.
const claimTTL = 5 * time.Minute

// claimHeartbeatInterval is how often a live holder refreshes the claim TTL. It
// is TTL/3, giving a holder two refresh attempts before the TTL would lapse —
// enough to survive transient Redis latency/network jitter without hammering
// Redis, and far shorter than the TTL so a healthy holder never loses the lock.
const claimHeartbeatInterval = claimTTL / 3



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

// sliceStartMs returns the hour start in unix millis (UTC).
func (w *rollupWorkItem) sliceStartMs() int64 {
	return w.date.Add(time.Duration(w.hour) * time.Hour).UnixMilli()
}

// sliceEndMs returns the hour end (exclusive) in unix millis (UTC): the start of
// the next hour. A watermark >= sliceEndMs means the entire hour is done.
func (w *rollupWorkItem) sliceEndMs() int64 {
	return w.sliceStartMs() + int64(time.Hour/time.Millisecond)
}

// tick runs one rollup cycle: list ready raw indices, aggregate each pending
// hour slice, write rollup docs, advance watermark.
func (e *RollupEngine) tick(ctx context.Context) {
	start := time.Now()
	defer func() {
		dur := time.Since(start)
		e.logger.Debug("rollup cycle complete", zap.Duration("elapsed", dur))
		if e.metrics != nil {
			e.metrics.recordTick(ctx, dur)
		}
	}()

	// Enumerate ready raw metric indices older than ReadyAfter.
	rawPrefix := signalPrefix(e.writer.config, "metric")
	readyBefore := time.Now().UTC().Add(-e.config.ReadyAfter)
	indices, err := e.client.ListIndices(ctx, rawPrefix+"-*")
	if err != nil {
		e.logger.Warn("rollup list indices failed", zap.Error(err))
		return
	}

	// Rollup tier prefix must be excluded from source enumeration: the raw
	// pattern "{prefix}-*" also matches "{prefix}-rollup-5m-*", which would
	// cause the engine to re-rollup already-aggregated data.
	rollupMarker := rawPrefix + "-rollup-"

	// Group indices by (appID, date), filter to ready ones. Each date expands
	// into 24 hour-slices so replicas can claim individual hours in parallel.
	byKey := make(map[string]*rollupWorkItem)
	for _, idx := range indices {
		if strings.HasPrefix(idx, rollupMarker) {
			continue // skip rollup-tier indices (already aggregated)
		}
		date, ok := extractIndexDate(idx, e.writer.config.Metrics.IndexDateFormat)
		if !ok {
			continue
		}
		// Hour-aware readiness: enumerate an index if at least its FIRST hour
		// (ending at date+1h) is ready. The prior `date.Before(readyBefore)`
		// gated on the DAY's midnight, so today's index was never enumerated
		// until tomorrow — even though today's early hours finished long ago.
		// The per-hour guard below skips any hour that is not yet ready.
		if !date.Add(time.Hour).Before(readyBefore) {
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

	// Expand each (appID, date) into 24 hour work items and process them in
	// chronological order. Ordering matters: the watermark records the last
	// CONTIGUOUS completed hour per app, so hours must be attempted oldest-first
	// or the watermark could skip an unprocessed earlier hour.
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
	sort.Slice(work, func(i, j int) bool {
		if work[i].date.Equal(work[j].date) {
			if work[i].appID == work[j].appID {
				return work[i].hour < work[j].hour
			}
			return work[i].appID < work[j].appID
		}
		return work[i].date.Before(work[j].date)
	})

	// Load per-app watermarks once and skip hours that are already durably
	// rolled up (lastBucketMs >= this hour's end). Without this the engine
	// re-aggregates every already-processed hour on every tick forever, so it
	// never advances to newer data.
	watermarks, _ := e.lock.GetAllWatermarks(ctx, RollupTier5m)

	readyBeforeMs := readyBefore.UnixMilli()

	// Snapshot the per-app backlog before processing: how many ready hours are
	// still unprocessed (not covered by the watermark). This is the observable
	// "are we caught up, or backfilling history?" signal — 0 means caught up,
	// a large value means the engine is re-rolling a gap.
	//
	// Every app with a watermark is reported, including backlog=0, so a healthy
	// caught-up engine emits an explicit 0 rather than dropping the series —
	// otherwise "no data" is indistinguishable from "caught up".
	//
	// NOTE: backlog is computed from the PRE-processing watermark snapshot (the
	// true "how much work remains" at tick start). The watermark/lag metrics,
	// however, are reported AFTER the processing loop (below) so they reflect
	// the freshly-advanced watermark, not a snapshot lagging one tick behind.
	var pendingByApp map[string]int
	if e.metrics != nil {
		pendingByApp = make(map[string]int, len(watermarks))
		for appID := range watermarks {
			pendingByApp[appID] = 0
		}
		for _, item := range work {
			if item.sliceEndMs() > readyBeforeMs {
				continue
			}
			if wm, ok := watermarks[item.appID]; ok && wm >= item.sliceEndMs() {
				continue
			}
			pendingByApp[item.appID]++
		}
	}

	for _, item := range work {
		// Per-hour readiness guard: skip hours that are NOT yet ready (their end
		// is within ReadyAfter of now). This is what lets the index-level gate be
		// hour-aware — today's index enumerates, but only its completed hours are
		// rolled up. The current in-progress hour and all future hours are skipped.
		if item.sliceEndMs() > readyBeforeMs {
			continue
		}
		if wm, ok := watermarks[item.appID]; ok && wm >= item.sliceEndMs() {
			continue
		}
		if err := e.processItem(ctx, item, watermarks); err != nil {
			e.logger.Warn("rollup item failed",
				zap.String("appID", item.appID),
				zap.Time("date", item.date),
				zap.Int("hour", item.hour),
				zap.Error(err),
			)
		}
	}

	// Report watermark/lag AFTER the processing loop so they reflect the freshly
	// advanced watermark. watermarks is mutated in-place by advanceWatermark as
	// each contiguous hour completes, so this snapshot is the true post-tick
	// progress — the pre-loop call previously reported a value lagging one tick.
	if e.metrics != nil {
		e.metrics.recordWatermarks(ctx, start, watermarks, pendingByApp)
	}
}

// processItem aggregates one (appID, date, hour) slice and writes rollup docs.
// It takes the per-app watermark map (from tick) to decide whether the slice's
// completion may advance the durable watermark.
func (e *RollupEngine) processItem(ctx context.Context, item *rollupWorkItem, watermarks map[string]int64) error {
	slice := item.sliceKey()

	// Claim the hour slice with a heartbeat lease. If another replica owns it,
	// skip. The lease's heartbeat goroutine keeps the short TTL alive while the
	// aggregation runs, so a live-but-slow aggregation never loses the lock; a
	// crash stops the heartbeat and the lock expires after claimTTL. defer
	// Release() unconditionally releases on every exit path (success, failure,
	// panic), which is safe because the write is idempotent (deterministic _id +
	// ES index overwrite) and the watermark is the durable "done" marker that the
	// next tick's `wm >= sliceEndMs` check uses to skip re-aggregation.
	var lease *lifecycle.Lease
	if e.lock != nil {
		var err error
		lease, err = e.lock.AcquireWithLease(ctx, RollupTier5m, item.appID, slice, claimTTL, claimHeartbeatInterval)
		if err != nil {
			return fmt.Errorf("claim slice: %w", err)
		}
		if lease == nil {
			e.logger.Debug("slice already claimed by another node, skipping",
				zap.String("appID", item.appID),
				zap.String("slice", slice),
			)
			return nil
		}
		defer lease.Release()
	}

	// Aggregate this single hour into 5m buckets (12 buckets/hour).
	// hourEnd is exclusive: aggregateMetric iterates `s.Before(tr.End)`, so the
	// window is [hourStart, hourStart+1h). The prior `-time.Millisecond` was a
	// fencepost error that needlessly cut off the final millisecond of each hour.
	hourStart := item.date.Add(time.Duration(item.hour) * time.Hour)
	hourEnd := hourStart.Add(time.Hour)
	startTime := time.Now()
	points, err := e.aggregator.AggregateSlice(ctx, item.appID, item.indices, hourStart, hourEnd)
	if err != nil {
		if e.metrics != nil {
			e.metrics.recordSlice(ctx, item.appID, 0, true, time.Since(startTime))
		}
		return fmt.Errorf("aggregate slice: %w", err)
	}

	if len(points) > 0 {
		if err := e.writer.WriteRollupPoints(ctx, RollupTier5m, points); err != nil {
			if e.metrics != nil {
				e.metrics.recordSlice(ctx, item.appID, 0, true, time.Since(startTime))
			}
			return fmt.Errorf("write rollup points: %w", err)
		}
	}

	if e.metrics != nil {
		e.metrics.recordSlice(ctx, item.appID, len(points), false, time.Since(startTime))
	}
	if len(points) == 0 {
		e.logger.Debug("no data to rollup", zap.String("appID", item.appID), zap.String("slice", slice))
	} else {
		e.logger.Info("rolled up metric slice",
			zap.String("appID", item.appID),
			zap.String("slice", slice),
			zap.Int("points", len(points)),
		)
	}

	// Advance the watermark for the completed hour. Contiguous-only: the
	// watermark means "all hours up to and including this one are durably rolled
	// up", so we may only advance it when this hour is the next one after the
	// current watermark (or it's the very first). Advancing on a non-contiguous
	// hour would let the tick skip an earlier, un-processed hour.
	e.advanceWatermark(ctx, item, watermarks)
	return nil
}

// advanceWatermark sets the durable per-app watermark to the END of this hour
// only when the hour is contiguous with the current watermark. Contiguity means
// the watermark is 0 (first hour ever) or equals this hour's start (the prior
// hour ended exactly here). Non-contiguous completions are left to the claim
// TTL for dedup; the watermark does not jump past a gap.
//
// On success it writes the new watermark back into the shared map so subsequent
// hours in the same tick chain their contiguity check against the fresh value.
func (e *RollupEngine) advanceWatermark(ctx context.Context, item *rollupWorkItem, watermarks map[string]int64) {
	if e.lock == nil {
		return
	}
	current := int64(0)
	if watermarks != nil {
		current = watermarks[item.appID]
	}
	hourStartMs := item.sliceStartMs()
	hourEndMs := item.sliceEndMs()

	// Contiguous only.
	if !watermarkContiguous(current, hourStartMs) {
		// Not contiguous — do not advance (leave claim TTL as the dedup).
		return
	}
	if err := e.lock.SetWatermark(ctx, RollupTier5m, item.appID, hourEndMs); err != nil {
		e.logger.Warn("set rollup watermark failed", zap.String("appID", item.appID), zap.Error(err))
		return
	}
	if watermarks != nil {
		watermarks[item.appID] = hourEndMs
	}
}

// watermarkContiguous reports whether an hour starting at hourStartMs may
// advance a watermark whose current value is `current`. The watermark means
// "all hours up to and including this one are done", so it may only advance to
// an hour that immediately follows it: current==0 (first hour ever) or
// current==hourStartMs (the prior hour ended exactly here). Any other value
// means there is a gap and advancing would skip an un-processed hour.
func watermarkContiguous(current, hourStartMs int64) bool {
	return current == 0 || current == hourStartMs
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
