// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// metaDeleteCapture records the last delete_by_query request and returns a
// configurable response body/status, so tests can exercise both the success and
// index-not-found paths.
type metaDeleteCapture struct {
	lastPath  string
	lastBody  map[string]any
	respond   func() (int, string)
}

func (h *metaDeleteCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && containsSegment(r.URL.Path, "_delete_by_query") {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &h.lastBody)
		h.lastPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		status, respBody := 200, `{"deleted": 7}`
		if h.respond != nil {
			status, respBody = h.respond()
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func TestCleanStaleMetadata_DeletesWithRangeQuery(t *testing.T) {
	h := &metaDeleteCapture{}
	ts := httptest.NewServer(h)
	defer ts.Close()

	client, err := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	require.NoError(t, err)
	admin := NewAdmin(client, &Config{Metrics: IndexConfig{IndexPrefix: "otel-metrics"}}, zaptest.NewLogger(t))

	deleted, err := admin.CleanStaleMetadata(context.Background(), 1700000000000)
	require.NoError(t, err)
	assert.Equal(t, int64(7), deleted)

	// Must target the singleton meta index.
	assert.Contains(t, h.lastPath, "otel-metrics-meta")

	// Query must be a range on lastSeenAt with an epoch-millis bound.
	q := h.lastBody["query"].(map[string]any)
	rng := q["range"].(map[string]any)
	field := rng[FieldMetaLastSeenAt].(map[string]any)
	assert.Equal(t, float64(1700000000000), field["lt"], "lastSeenAt bound must be epoch millis")
}

func TestCleanStaleMetadata_IndexNotFoundIsIdempotent(t *testing.T) {
	h := &metaDeleteCapture{
		respond: func() (int, string) {
			return http.StatusNotFound, `{"error":{"type":"index_not_found_exception"},"status":404}`
		},
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	client, err := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	require.NoError(t, err)
	admin := NewAdmin(client, &Config{Metrics: IndexConfig{IndexPrefix: "otel-metrics"}}, zaptest.NewLogger(t))

	// Fresh deployment (no meta index): cleanup must succeed with 0 deleted,
	// not error.
	deleted, err := admin.CleanStaleMetadata(context.Background(), 1700000000000)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
}

func TestCleanStaleMetadataByRetention_UsesConfiguredRetention(t *testing.T) {
	h := &metaDeleteCapture{}
	ts := httptest.NewServer(h)
	defer ts.Close()

	client, err := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	require.NoError(t, err)

	// 1 hour retention.
	admin := NewAdmin(client, &Config{Metrics: IndexConfig{
		IndexPrefix: "otel-metrics",
		Retention:   time.Hour,
	}}, zaptest.NewLogger(t))

	before := time.Now().UnixMilli()
	_, err = admin.CleanStaleMetadataByRetention(context.Background())
	require.NoError(t, err)

	// The cutoff must be ~1 hour ago (within a couple seconds of jitter).
	q := h.lastBody["query"].(map[string]any)
	rng := q["range"].(map[string]any)
	lt := rng[FieldMetaLastSeenAt].(map[string]any)["lt"].(float64)
	delta := before - int64(lt)
	assert.InDelta(t, time.Hour.Milliseconds(), delta, float64(5*time.Second/time.Millisecond),
		"cutoff must be ~1h before now")
}
