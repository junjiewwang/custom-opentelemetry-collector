// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// typeRegistry maintains metric name → {type, unit} as metrics are written.
// VictoriaMetrics (Prometheus data model) has no metric type concept, but
// Grafana Metrics Drilldown relies on /api/v1/metadata type to decide whether
// to wrap a query in rate(). The registry fills that gap at the provider layer.
//
// Persistence: entries are dumped to a local JSON file (atomic tmp+rename)
// periodically and at Shutdown; Start reloads them so a restart does not blank
// the metadata until the next write. Entries carry lastSeen so stale metrics
// (long-stopped reporting) can be filtered by time window on read.
type typeRegistry struct {
	mu      sync.RWMutex
	entries map[string]registryEntry
	path    string // "" = memory-only
}

type registryEntry struct {
	Type     string `json:"type"`
	Unit     string `json:"unit,omitempty"`
	LastSeen int64  `json:"lastSeenMs"`
}

func newTypeRegistry(path string) *typeRegistry {
	return &typeRegistry{
		entries: make(map[string]registryEntry),
		path:    path,
	}
}

// record notes a metric's type/unit at ingest time. lastSeen is refreshed.
func (r *typeRegistry) record(name, metricType, unit string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[name] = registryEntry{
		Type:     metricType,
		Unit:     unit,
		LastSeen: now.UnixMilli(),
	}
}

// snapshot returns entries whose lastSeen falls inside [start, end]. An open
// bound (zero time) skips that filter. The returned map is a copy.
func (r *typeRegistry) snapshot(start, end time.Time) map[string]storedmodel.MetricMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]storedmodel.MetricMeta, len(r.entries))
	for name, e := range r.entries {
		if !start.IsZero() && e.LastSeen < start.UnixMilli() {
			continue
		}
		if !end.IsZero() && e.LastSeen > end.UnixMilli() {
			continue
		}
		out[name] = storedmodel.MetricMeta{Type: e.Type, Unit: e.Unit}
	}
	return out
}

// get returns one entry by name.
func (r *typeRegistry) get(name string) (storedmodel.MetricMeta, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	if !ok {
		return storedmodel.MetricMeta{}, false
	}
	return storedmodel.MetricMeta{Type: e.Type, Unit: e.Unit}, true
}

// flushToFile atomically writes the registry to its configured path.
// Memory-only registries (empty path) are a no-op.
func (r *typeRegistry) flushToFile() error {
	if r.path == "" {
		return nil
	}
	r.mu.RLock()
	data, err := json.Marshal(r.entries)
	r.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// loadFromFile restores previously persisted entries. Missing file is not an
// error (fresh deploy); entries already in memory win over stale file data.
func (r *typeRegistry) loadFromFile() error {
	if r.path == "" {
		return nil
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var loaded map[string]registryEntry
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, e := range loaded {
		if _, exists := r.entries[name]; !exists {
			r.entries[name] = e
		}
	}
	return nil
}
