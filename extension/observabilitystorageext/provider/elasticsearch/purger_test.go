// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
)

// ═══════════════════════════════════════════════════
// Mock ES Server for Integration Testing
// ═══════════════════════════════════════════════════

// purgerMockES simulates Elasticsearch API endpoints needed by Purger.
// It maintains an in-memory index registry to verify deletions.
type purgerMockES struct {
	mu      sync.Mutex
	indices map[string]purgerMockIndexInfo // indexName → info
	t       *testing.T
}

type purgerMockIndexInfo struct {
	DocCount  int64
	SizeBytes int64
}

func newPurgerMockES(t *testing.T) *purgerMockES {
	return &purgerMockES{
		indices: make(map[string]purgerMockIndexInfo),
		t:       t,
	}
}

func (m *purgerMockES) addIndex(name string, docCount int64, sizeBytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.indices[name] = purgerMockIndexInfo{DocCount: docCount, SizeBytes: sizeBytes}
}

func (m *purgerMockES) getIndices() map[string]purgerMockIndexInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]purgerMockIndexInfo, len(m.indices))
	for k, v := range m.indices {
		result[k] = v
	}
	return result
}

func (m *purgerMockES) handler() http.Handler {
	mux := http.NewServeMux()

	// Ping
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"mock-es","cluster_name":"test","version":{"number":"8.0.0"}}`))
			return
		}
		// Fall through for other paths via default
		http.NotFound(w, r)
	})

	// _cat/indices — returns matching indices
	mux.HandleFunc("/_cat/indices/", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()

		// Extract the pattern from path: /_cat/indices/{pattern}
		path := strings.TrimPrefix(r.URL.Path, "/_cat/indices/")
		pattern := strings.TrimSuffix(path, "")

		var items []map[string]string
		for name := range m.indices {
			if matchPattern(name, pattern) {
				items = append(items, map[string]string{"index": name})
			}
		}

		if len(items) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Sort for deterministic test output
		sort.Slice(items, func(i, j int) bool {
			return items[i]["index"] < items[j]["index"]
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	})

	// _count — returns doc count for an index
	mux.HandleFunc("/_count", func(w http.ResponseWriter, r *http.Request) {
		// Path is /{indexPattern}/_count, need to parse from full path
		http.NotFound(w, r)
	})

	// _delete_by_query
	mux.HandleFunc("/_delete_by_query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deleted": 500}`))
	})

	// Catch-all for dynamic paths (index operations)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// DELETE /{indexName} — delete index
		if r.Method == http.MethodDelete && !strings.Contains(path[1:], "/") {
			indexName := strings.TrimPrefix(path, "/")
			m.mu.Lock()
			_, exists := m.indices[indexName]
			if exists {
				delete(m.indices, indexName)
			}
			m.mu.Unlock()

			if !exists {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"type":"index_not_found_exception"},"status":404}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
			return
		}

		// POST /{indexPattern}/_count
		if r.Method == http.MethodPost && strings.HasSuffix(path, "/_count") {
			indexPattern := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/_count")
			m.mu.Lock()
			var total int64
			for name, info := range m.indices {
				if matchPattern(name, indexPattern) || name == indexPattern {
					total += info.DocCount
				}
			}
			m.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int64{"count": total})
			return
		}

		// POST /{indexPattern}/_delete_by_query
		if r.Method == http.MethodPost && strings.Contains(path, "/_delete_by_query") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"deleted": 500}`))
			return
		}

		// GET /_cat/indices/{pattern}
		if r.Method == http.MethodGet && strings.HasPrefix(path, "/_cat/indices/") {
			pattern := strings.TrimPrefix(path, "/_cat/indices/")
			// Remove query params artifact if any
			if idx := strings.Index(pattern, "?"); idx != -1 {
				pattern = pattern[:idx]
			}

			m.mu.Lock()
			var items []map[string]string
			for name := range m.indices {
				if matchPattern(name, pattern) {
					items = append(items, map[string]string{"index": name})
				}
			}
			m.mu.Unlock()

			if len(items) == 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			sort.Slice(items, func(i, j int) bool {
				return items[i]["index"] < items[j]["index"]
			})

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(items)
			return
		}

		// GET / (ping)
		if r.Method == http.MethodGet && path == "/" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"mock-es"}`))
			return
		}

		mux.ServeHTTP(w, r)
	})
}

// matchPattern implements simple glob matching for ES index patterns.
// Supports trailing "*" wildcard only.
func matchPattern(name, pattern string) bool {
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(name, prefix)
	}
	return name == pattern
}

// ═══════════════════════════════════════════════════
// Integration Tests
// ═══════════════════════════════════════════════════

func TestPurger_PurgeExpired_DeletesOldIndices(t *testing.T) {
	mock := newPurgerMockES(t)

	// Setup: indices spanning 10 days
	mock.addIndex("otel-traces-2026.05.20", 1000, 1024*1024)
	mock.addIndex("otel-traces-2026.05.21", 1500, 1024*1024*2)
	mock.addIndex("otel-traces-2026.05.22", 2000, 1024*1024*3)
	mock.addIndex("otel-traces-2026.05.28", 800, 1024*1024)
	mock.addIndex("otel-traces-2026.05.29", 900, 1024*1024)
	mock.addIndex("otel-traces-2026.06.01", 500, 512*1024)

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, err := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	purger := NewPurger(client, &Config{
		Traces:  IndexConfig{IndexPrefix: "otel-traces", IndexDateFormat: "2006.01.02"},
		Metrics: IndexConfig{IndexPrefix: "otel-metrics", IndexDateFormat: "2006.01.02"},
		Logs:    IndexConfig{IndexPrefix: "otel-logs", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	// Purge data older than May 25 → should delete 05.20, 05.21, 05.22
	cutoff := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	result, err := purger.PurgeExpired(context.Background(), lifecycle.SignalTrace, cutoff)
	if err != nil {
		t.Fatalf("PurgeExpired failed: %v", err)
	}

	// Verify 3 indices deleted
	if result.DeletedUnits != 3 {
		t.Errorf("expected 3 deleted indices, got %d", result.DeletedUnits)
	}
	if result.Signal != lifecycle.SignalTrace {
		t.Errorf("expected signal trace, got %s", result.Signal)
	}

	// Verify remaining indices
	remaining := mock.getIndices()
	if len(remaining) != 3 {
		t.Errorf("expected 3 remaining indices, got %d: %v", len(remaining), remaining)
	}
	for _, expected := range []string{"otel-traces-2026.05.28", "otel-traces-2026.05.29", "otel-traces-2026.06.01"} {
		if _, ok := remaining[expected]; !ok {
			t.Errorf("expected index %s to still exist", expected)
		}
	}
	for _, deleted := range []string{"otel-traces-2026.05.20", "otel-traces-2026.05.21", "otel-traces-2026.05.22"} {
		if _, ok := remaining[deleted]; ok {
			t.Errorf("expected index %s to be deleted", deleted)
		}
	}
}

func TestPurger_PurgeExpired_NoExpiredIndices(t *testing.T) {
	mock := newPurgerMockES(t)

	// All indices are recent (after cutoff)
	mock.addIndex("otel-traces-2026.06.01", 500, 1024)
	mock.addIndex("otel-traces-2026.06.02", 600, 1024)

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	purger := NewPurger(client, &Config{
		Traces: IndexConfig{IndexPrefix: "otel-traces", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	// Cutoff = May 30 → no indices to delete
	cutoff := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	result, err := purger.PurgeExpired(context.Background(), lifecycle.SignalTrace, cutoff)
	if err != nil {
		t.Fatalf("PurgeExpired failed: %v", err)
	}

	if result.DeletedUnits != 0 {
		t.Errorf("expected 0 deleted indices, got %d", result.DeletedUnits)
	}

	// All indices should remain
	remaining := mock.getIndices()
	if len(remaining) != 2 {
		t.Errorf("expected 2 remaining indices, got %d", len(remaining))
	}
}

func TestPurger_PurgeExpired_EmptyCluster(t *testing.T) {
	mock := newPurgerMockES(t)
	// No indices at all

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	purger := NewPurger(client, &Config{
		Traces: IndexConfig{IndexPrefix: "otel-traces", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	cutoff := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	result, err := purger.PurgeExpired(context.Background(), lifecycle.SignalTrace, cutoff)
	if err != nil {
		t.Fatalf("PurgeExpired failed: %v", err)
	}

	if result.DeletedUnits != 0 {
		t.Errorf("expected 0 deleted indices, got %d", result.DeletedUnits)
	}
}

func TestPurger_PurgeByApp_DeletesAppScopedIndices(t *testing.T) {
	mock := newPurgerMockES(t)

	// Mix of app-scoped and global indices
	mock.addIndex("otel-traces-myapp-2026.05.20", 100, 1024)
	mock.addIndex("otel-traces-myapp-2026.05.21", 200, 1024)
	mock.addIndex("otel-traces-myapp-2026.06.01", 300, 1024)    // recent, keep
	mock.addIndex("otel-traces-otherapp-2026.05.20", 150, 1024) // different app, keep
	mock.addIndex("otel-traces-2026.05.20", 500, 1024)          // global, keep

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	purger := NewPurger(client, &Config{
		Traces: IndexConfig{IndexPrefix: "otel-traces", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	// Purge myapp data older than May 25
	cutoff := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	result, err := purger.PurgeByApp(context.Background(), "myapp", lifecycle.SignalTrace, cutoff)
	if err != nil {
		t.Fatalf("PurgeByApp failed: %v", err)
	}

	// Should delete myapp-05.20 and myapp-05.21
	if result.DeletedUnits != 2 {
		t.Errorf("expected 2 deleted indices for myapp, got %d", result.DeletedUnits)
	}

	// Verify the right indices remain
	remaining := mock.getIndices()
	expectedRemaining := []string{
		"otel-traces-myapp-2026.06.01",
		"otel-traces-otherapp-2026.05.20",
		"otel-traces-2026.05.20",
	}
	if len(remaining) != 3 {
		t.Errorf("expected 3 remaining indices, got %d: %v", len(remaining), remaining)
	}
	for _, name := range expectedRemaining {
		if _, ok := remaining[name]; !ok {
			t.Errorf("expected index %s to still exist", name)
		}
	}
}

func TestPurger_EstimatePurge_ReturnsCorrectPreview(t *testing.T) {
	mock := newPurgerMockES(t)

	mock.addIndex("otel-traces-2026.05.20", 1000, 1024*1024)
	mock.addIndex("otel-traces-2026.05.21", 2000, 1024*1024*2)
	mock.addIndex("otel-traces-2026.05.28", 500, 512*1024) // recent
	mock.addIndex("otel-traces-2026.06.01", 300, 256*1024) // recent

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	purger := NewPurger(client, &Config{
		Traces: IndexConfig{IndexPrefix: "otel-traces", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	cutoff := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	estimate, err := purger.EstimatePurge(context.Background(), lifecycle.SignalTrace, cutoff)
	if err != nil {
		t.Fatalf("EstimatePurge failed: %v", err)
	}

	// Should identify 2 affected indices (05.20, 05.21)
	if len(estimate.AffectedUnits) != 2 {
		t.Errorf("expected 2 affected units, got %d: %v", len(estimate.AffectedUnits), estimate.AffectedUnits)
	}

	// Doc count: 1000 + 2000 = 3000
	if estimate.EstimatedDocs != 3000 {
		t.Errorf("expected 3000 estimated docs, got %d", estimate.EstimatedDocs)
	}

	if estimate.Signal != lifecycle.SignalTrace {
		t.Errorf("expected signal trace, got %s", estimate.Signal)
	}

	// Verify no indices were actually deleted (it's just an estimate!)
	remaining := mock.getIndices()
	if len(remaining) != 4 {
		t.Errorf("estimate should not delete any indices, expected 4, got %d", len(remaining))
	}
}

func TestPurger_GetDataBoundary_ReturnsTimeRange(t *testing.T) {
	mock := newPurgerMockES(t)

	mock.addIndex("otel-metrics-2026.05.15", 100, 1024)
	mock.addIndex("otel-metrics-2026.05.20", 200, 1024)
	mock.addIndex("otel-metrics-2026.05.25", 300, 1024)
	mock.addIndex("otel-metrics-2026.06.01", 400, 1024)

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	purger := NewPurger(client, &Config{
		Metrics: IndexConfig{IndexPrefix: "otel-metrics", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	boundary, err := purger.GetDataBoundary(context.Background(), lifecycle.SignalMetric)
	if err != nil {
		t.Fatalf("GetDataBoundary failed: %v", err)
	}

	if boundary.IsEmpty {
		t.Fatal("expected non-empty boundary")
	}

	expectedOldest := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	expectedNewest := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	if boundary.OldestAt == nil || !boundary.OldestAt.Equal(expectedOldest) {
		t.Errorf("expected oldest %v, got %v", expectedOldest, boundary.OldestAt)
	}
	if boundary.NewestAt == nil || !boundary.NewestAt.Equal(expectedNewest) {
		t.Errorf("expected newest %v, got %v", expectedNewest, boundary.NewestAt)
	}
	if boundary.Signal != lifecycle.SignalMetric {
		t.Errorf("expected signal metric, got %s", boundary.Signal)
	}
}

func TestPurger_GetDataBoundary_EmptyCluster(t *testing.T) {
	mock := newPurgerMockES(t)

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	purger := NewPurger(client, &Config{
		Logs: IndexConfig{IndexPrefix: "otel-logs", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	boundary, err := purger.GetDataBoundary(context.Background(), lifecycle.SignalLog)
	if err != nil {
		t.Fatalf("GetDataBoundary failed: %v", err)
	}

	if !boundary.IsEmpty {
		t.Error("expected empty boundary for empty cluster")
	}
}

func TestPurger_GetDataBoundary_UnparseableIndexNames(t *testing.T) {
	mock := newPurgerMockES(t)

	// Indices with no parseable date suffix
	mock.addIndex("otel-logs-rollover-000001", 100, 1024)
	mock.addIndex("otel-logs-rollover-000002", 200, 1024)

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	purger := NewPurger(client, &Config{
		Logs: IndexConfig{IndexPrefix: "otel-logs", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	boundary, err := purger.GetDataBoundary(context.Background(), lifecycle.SignalLog)
	if err != nil {
		t.Fatalf("GetDataBoundary failed: %v", err)
	}

	// Should return empty since no dates could be parsed
	if !boundary.IsEmpty {
		t.Error("expected empty boundary when no dates are parseable")
	}
}

func TestPurger_PurgeExpired_SkipsUnparseableIndices(t *testing.T) {
	mock := newPurgerMockES(t)

	mock.addIndex("otel-traces-2026.05.20", 1000, 1024)     // old, should delete
	mock.addIndex("otel-traces-rollover-000001", 500, 1024) // unparseable, skip
	mock.addIndex("otel-traces-2026.06.01", 300, 1024)      // recent, keep

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	purger := NewPurger(client, &Config{
		Traces: IndexConfig{IndexPrefix: "otel-traces", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	cutoff := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	result, err := purger.PurgeExpired(context.Background(), lifecycle.SignalTrace, cutoff)
	if err != nil {
		t.Fatalf("PurgeExpired failed: %v", err)
	}

	// Should only delete the one parseable old index
	if result.DeletedUnits != 1 {
		t.Errorf("expected 1 deleted index (skipping unparseable), got %d", result.DeletedUnits)
	}

	remaining := mock.getIndices()
	if _, ok := remaining["otel-traces-rollover-000001"]; !ok {
		t.Error("unparseable index should not be deleted")
	}
	if _, ok := remaining["otel-traces-2026.06.01"]; !ok {
		t.Error("recent index should not be deleted")
	}
}

func TestPurger_PurgeExpired_MultipleSignals(t *testing.T) {
	mock := newPurgerMockES(t)

	// Trace indices
	mock.addIndex("otel-traces-2026.05.20", 1000, 1024)
	mock.addIndex("otel-traces-2026.06.01", 500, 1024)

	// Metric indices
	mock.addIndex("otel-metrics-2026.04.01", 2000, 1024) // very old
	mock.addIndex("otel-metrics-2026.06.01", 800, 1024)

	// Log indices
	mock.addIndex("otel-logs-2026.05.10", 1500, 1024)
	mock.addIndex("otel-logs-2026.06.01", 600, 1024)

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	config := &Config{
		Traces:  IndexConfig{IndexPrefix: "otel-traces", IndexDateFormat: "2006.01.02"},
		Metrics: IndexConfig{IndexPrefix: "otel-metrics", IndexDateFormat: "2006.01.02"},
		Logs:    IndexConfig{IndexPrefix: "otel-logs", IndexDateFormat: "2006.01.02"},
	}
	purger := NewPurger(client, config, zaptest.NewLogger(t))

	cutoff := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)

	// Purge traces
	traceResult, err := purger.PurgeExpired(context.Background(), lifecycle.SignalTrace, cutoff)
	if err != nil {
		t.Fatalf("Trace purge failed: %v", err)
	}
	if traceResult.DeletedUnits != 1 {
		t.Errorf("traces: expected 1 deleted, got %d", traceResult.DeletedUnits)
	}

	// Purge metrics
	metricResult, err := purger.PurgeExpired(context.Background(), lifecycle.SignalMetric, cutoff)
	if err != nil {
		t.Fatalf("Metric purge failed: %v", err)
	}
	if metricResult.DeletedUnits != 1 {
		t.Errorf("metrics: expected 1 deleted, got %d", metricResult.DeletedUnits)
	}

	// Purge logs
	logResult, err := purger.PurgeExpired(context.Background(), lifecycle.SignalLog, cutoff)
	if err != nil {
		t.Fatalf("Log purge failed: %v", err)
	}
	if logResult.DeletedUnits != 1 {
		t.Errorf("logs: expected 1 deleted, got %d", logResult.DeletedUnits)
	}

	// Only recent indices remain
	remaining := mock.getIndices()
	if len(remaining) != 3 {
		t.Errorf("expected 3 remaining indices, got %d: %v", len(remaining), remaining)
	}
}

func TestPurger_PurgeExpired_AppWithHyphenInName(t *testing.T) {
	mock := newPurgerMockES(t)

	// App name with hyphens: "my-cool-app"
	mock.addIndex("otel-traces-my-cool-app-2026.05.20", 100, 1024) // old
	mock.addIndex("otel-traces-my-cool-app-2026.06.01", 200, 1024) // recent

	ts := httptest.NewServer(mock.handler())
	defer ts.Close()

	client, _ := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	purger := NewPurger(client, &Config{
		Traces: IndexConfig{IndexPrefix: "otel-traces", IndexDateFormat: "2006.01.02"},
	}, zaptest.NewLogger(t))

	cutoff := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	result, err := purger.PurgeByApp(context.Background(), "my-cool-app", lifecycle.SignalTrace, cutoff)
	if err != nil {
		t.Fatalf("PurgeByApp failed: %v", err)
	}

	if result.DeletedUnits != 1 {
		t.Errorf("expected 1 deleted index, got %d", result.DeletedUnits)
	}

	remaining := mock.getIndices()
	if _, ok := remaining["otel-traces-my-cool-app-2026.06.01"]; !ok {
		t.Error("recent app index should remain")
	}
}

func TestPurger_extractDate_Various(t *testing.T) {
	purger := &Purger{
		config: &Config{
			Traces: IndexConfig{IndexDateFormat: "2006.01.02"},
		},
	}

	tests := []struct {
		name      string
		indexName string
		signal    lifecycle.SignalType
		expected  *time.Time
	}{
		{
			name:      "standard date suffix",
			indexName: "otel-traces-2026.05.25",
			signal:    lifecycle.SignalTrace,
			expected:  timePtr(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)),
		},
		{
			name:      "app-scoped with date",
			indexName: "otel-traces-myapp-2026.05.25",
			signal:    lifecycle.SignalTrace,
			expected:  timePtr(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)),
		},
		{
			name:      "app with hyphens",
			indexName: "otel-traces-my-cool-app-2026.05.25",
			signal:    lifecycle.SignalTrace,
			expected:  timePtr(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)),
		},
		{
			name:      "rollover index (no date)",
			indexName: "otel-traces-000001",
			signal:    lifecycle.SignalTrace,
			expected:  nil,
		},
		{
			name:      "partial date (invalid)",
			indexName: "otel-traces-2026.13.40",
			signal:    lifecycle.SignalTrace,
			expected:  nil, // invalid month/day
		},
		{
			name:      "empty index name",
			indexName: "",
			signal:    lifecycle.SignalTrace,
			expected:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := purger.extractDate(tt.indexName, tt.signal)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", *result)
				}
			} else {
				if result == nil {
					t.Errorf("expected %v, got nil", *tt.expected)
				} else if !result.Equal(*tt.expected) {
					t.Errorf("expected %v, got %v", *tt.expected, *result)
				}
			}
		})
	}
}

// TestPurger_extractDate_UsesPerSignalFormat is a regression test: extractDate
// previously always read Traces.IndexDateFormat. When traces and metrics use
// different formats, a metrics/logs index date was parsed with the traces
// format and silently failed, so those indices were never matched for deletion.
// Each signal must use its own IndexDateFormat.
func TestPurger_extractDate_UsesPerSignalFormat(t *testing.T) {
	purger := &Purger{
		config: &Config{
			// Traces uses dash format; metrics/logs use dot format.
			Traces:  IndexConfig{IndexDateFormat: "2006-01-02"},
			Metrics: IndexConfig{IndexDateFormat: "2006.01.02"},
			Logs:    IndexConfig{IndexDateFormat: "2006.01.02"},
		},
	}

	want := timePtr(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC))

	// Metrics index with a dot-format date must parse via the Metrics config.
	// With the old bug (always Traces format = "2006-01-02"), this returned nil.
	got := purger.extractDate("otel-metrics-app-2026.05.25", lifecycle.SignalMetric)
	if got == nil || !got.Equal(*want) {
		t.Fatalf("metrics signal: expected %v, got %v", *want, got)
	}

	// Logs index with a dot-format date must parse via the Logs config.
	got = purger.extractDate("otel-logs-app-2026.05.25", lifecycle.SignalLog)
	if got == nil || !got.Equal(*want) {
		t.Fatalf("logs signal: expected %v, got %v", *want, got)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
