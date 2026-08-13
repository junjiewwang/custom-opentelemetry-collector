// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.uber.org/zap/zaptest"
)

// ── buildMetaDoc ────────────────────────────────────────────────────────

func TestBuildMetaDoc(t *testing.T) {
	pt := storedmodel.StoredMetricDataPoint{
		Name:        "jvm.memory.used",
		Type:        "gauge",
		Unit:        "By",
		AppID:       "myapp",
		ServiceName: "my-service",
		Labels: map[string]any{
			"pool":        1,
			"area":        "heap",
			"server.port": "8080", // already underscore-normalized key
		},
		TimeUnixMilli: 1700000000000,
	}

	doc := buildMetaDoc(pt)
	assert.Equal(t, "jvm.memory.used", doc.Name)
	assert.Equal(t, "myapp", doc.AppID)
	assert.Equal(t, "gauge", doc.Type)
	assert.Equal(t, "By", doc.Unit)

	// labelKeys derived from pt.Labels, sorted deterministically.
	assert.Equal(t, []string{"area", "pool", "server.port"}, doc.LabelKeys)
	// service_name is promoted to ServiceNames (top-level field, not a label).
	assert.Equal(t, []string{"my-service"}, doc.ServiceNames)
	assert.Equal(t, int64(1700000000000), doc.LastSeenAt)
}

func TestBuildMetaDoc_NoService(t *testing.T) {
	pt := storedmodel.StoredMetricDataPoint{Name: "x", Labels: nil}
	doc := buildMetaDoc(pt)
	assert.Empty(t, doc.ServiceNames)
	assert.Empty(t, doc.LabelKeys)
}

// ── metaCache ───────────────────────────────────────────────────────────

func TestMetaCache_ShouldUpsert(t *testing.T) {
	c := newMetaCache()
	now := time.Now()
	doc := func(keys ...string) MetaDoc {
		return MetaDoc{AppID: "app", Name: "m", LabelKeys: keys}
	}

	// First sight → upsert.
	assert.True(t, c.shouldUpsert(doc("a", "b"), now))
	// Same label set, within refresh interval → no upsert (dedup).
	assert.False(t, c.shouldUpsert(doc("a", "b"), now))
	assert.False(t, c.shouldUpsert(doc("b", "a"), now), "order must not matter after sorting")
	// New label key → upsert.
	assert.True(t, c.shouldUpsert(doc("a", "c"), now))
	// Now "a","b","c" all seen → no upsert.
	assert.False(t, c.shouldUpsert(doc("a", "b", "c"), now))
	// Different metric (different docID) → upsert.
	assert.True(t, c.shouldUpsert(MetaDoc{AppID: "app", Name: "n", LabelKeys: []string{"a"}}, now))
	// After metaRefreshInterval elapses, a stable label set re-upserts to
	// refresh lastSeenAt (CleanStaleMetadata must not prune live metrics).
	assert.True(t, c.shouldUpsert(doc("a", "b", "c"), now.Add(metaRefreshInterval+time.Second)))
}

func TestMetaCache_Concurrent(t *testing.T) {
	c := newMetaCache()
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.shouldUpsert(MetaDoc{AppID: "app", Name: "m", LabelKeys: []string{"a", "b"}}, now)
			}
		}()
	}
	wg.Wait()
	// After warm-up, a repeated call must not report new keys.
	assert.False(t, c.shouldUpsert(MetaDoc{AppID: "app", Name: "m", LabelKeys: []string{"a", "b"}}, now))
}

// ── writeMetaForPoint (end-to-end through the writer) ───────────────────

// capturedBulk collects every _bulk request body (NDJSON) the mock ES server
// receives, so tests can assert on action lines and payloads.
type capturedBulk struct {
	mu     sync.Mutex
	bodies []string
}

func (c *capturedBulk) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, string(body))
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(BulkResponse{Took: 1, Errors: false})
	}
}

func (c *capturedBulk) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

func TestMetricWriter_WriteMetaForPoint_EmitsUpdateAction(t *testing.T) {
	cap := &capturedBulk{}
	server := httptest.NewServer(cap.handler())
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)
	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	pt := storedmodel.StoredMetricDataPoint{
		Name:        "cpu.usage",
		Type:        "gauge",
		Unit:        "1",
		AppID:       "myapp",
		ServiceName: "svc-a",
		Labels:      map[string]any{"host": "srv01"},
		TimeUnixMilli: time.Now().UnixMilli(),
	}

	require.NoError(t, writer.WriteMetricPoints(context.Background(), []storedmodel.StoredMetricDataPoint{pt}))
	require.NoError(t, writer.Flush(context.Background()))

	// Find the update action among captured bodies (data buffer emits an "index"
	// action, meta buffer emits an "update" action).
	found := false
	for _, body := range cap.all() {
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, `"update"`) {
				found = true
				assert.Contains(t, line, "otel-metrics-meta", "update action must target the meta index")
				assert.Contains(t, line, `"_id":"myapp:cpu.usage"`, "update action must carry the deterministic doc id")
			}
			if strings.Contains(line, `"scripted_upsert"`) {
				assert.Contains(t, line, `"script"`, "scripted upsert must include the script")
				assert.Contains(t, line, `"labelKeys"`, "script params must carry labelKeys")
			}
		}
	}
	assert.True(t, found, "expected an update action for the metadata upsert")
}

func TestMetricWriter_MetaDedup(t *testing.T) {
	cap := &capturedBulk{}
	server := httptest.NewServer(cap.handler())
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)
	writer := NewMetricWriter(client, cfg, zaptest.NewLogger(t))

	pt := storedmodel.StoredMetricDataPoint{
		Name: "cpu.usage", Type: "gauge", AppID: "myapp",
		Labels: map[string]any{"host": "srv01"}, TimeUnixMilli: time.Now().UnixMilli(),
	}

	// Write the same point twice, flushing between so the cache sees both.
	require.NoError(t, writer.WriteMetricPoints(context.Background(), []storedmodel.StoredMetricDataPoint{pt}))
	require.NoError(t, writer.Flush(context.Background()))
	updatesAfterFirst := countUpdateActions(cap.all())

	require.NoError(t, writer.WriteMetricPoints(context.Background(), []storedmodel.StoredMetricDataPoint{pt}))
	require.NoError(t, writer.Flush(context.Background()))

	// The second write has the same label set, so the cache suppresses a second
	// meta upsert. (Data buffer still emits an index action each time.)
	updatesAfterSecond := countUpdateActions(cap.all())
	assert.Equal(t, updatesAfterFirst, updatesAfterSecond,
		"duplicate label set must not re-upsert metadata")
}

func countUpdateActions(bodies []string) int {
	n := 0
	for _, body := range bodies {
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, `"update"`) {
				n++
			}
		}
	}
	return n
}
