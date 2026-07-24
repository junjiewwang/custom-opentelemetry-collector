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

// captureHandler records the last _delete_by_query request URL and body.
type captureHandler struct {
	lastPath string
	lastBody map[string]any
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && (r.URL.Path == "/_delete_by_query" || containsSegment(r.URL.Path, "_delete_by_query")) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &h.lastBody)
		h.lastPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deleted": 7}`))
		return
	}
	// Ping
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"mock"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
}

// containsSegment reports whether path contains the given "/"-delimited segment.
func containsSegment(path, seg string) bool {
	for i := 0; i+len(seg) <= len(path); i++ {
		if path[i:i+len(seg)] == seg && (i == 0 || path[i-1] == '/') && (i+len(seg) == len(path) || path[i+len(seg)] == '/') {
			return true
		}
	}
	return false
}

// TestAdmin_Purge_CorrectFieldAndIntegerBound asserts that Admin.Purge emits a
// delete_by_query on the signal's canonical ES timestamp field with an INTEGER
// bound (not an RFC3339 string), per signal. This is the regression guard for
// the consolidation: the admin path previously used legacy field names
// (start_time/@timestamp/timestamp) and string bounds that matched nothing on
// real data.
func TestAdmin_Purge_CorrectFieldAndIntegerBound(t *testing.T) {
	tests := []struct {
		signal    string
		wantField string
	}{
		{"trace", FieldStartTimeUnixNano},
		{"metric", FieldMetricTimeUnixMilli},
		{"log", FieldLogTimeUnixNano},
	}
	for _, tt := range tests {
		t.Run(tt.signal, func(t *testing.T) {
			h := &captureHandler{}
			ts := httptest.NewServer(h)
			defer ts.Close()

			client, err := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
			require.NoError(t, err)
			admin := NewAdmin(client, &Config{}, zaptest.NewLogger(t))

			before := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
			deleted, err := admin.Purge(context.Background(), "otel-"+tt.signal+"s", tt.signal, before)
			require.NoError(t, err)
			assert.Equal(t, int64(7), deleted)

			// Index pattern in the URL path.
			assert.Contains(t, h.lastPath, "otel-"+tt.signal+"s-*")

			// The query must target the canonical field with a numeric bound.
			q := h.lastBody["query"].(map[string]any)
			rng := q["range"].(map[string]any)
			fieldSpec, ok := rng[tt.wantField]
			require.True(t, ok, "expected range on %s, got %v", tt.wantField, rng)
			lt := fieldSpec.(map[string]any)["lt"]
			// json.Unmarshal of the captured body yields float64 for any JSON
			// number; assert it is numeric (not a string) and equal in value.
			_, isStr := lt.(string)
			assert.False(t, isStr, "bound must be numeric, not a string")
			ltFloat, ok := lt.(float64)
			require.True(t, ok, "bound must be a JSON number, got %T", lt)
			switch tt.signal {
			case "metric":
				assert.Equal(t, float64(before.UnixMilli()), ltFloat, "metric bound must be epoch millis")
			default:
				assert.Equal(t, float64(before.UnixNano()), ltFloat, "trace/log bound must be unix nanos")
			}
		})
	}
}

// TestAdmin_PurgeByApp_AppScoping asserts PurgeByApp adds the appId term and
// uses the app-scoped index pattern.
func TestAdmin_PurgeByApp_AppScoping(t *testing.T) {
	h := &captureHandler{}
	ts := httptest.NewServer(h)
	defer ts.Close()

	client, err := NewClient(&Config{Addresses: []string{ts.URL}}, zaptest.NewLogger(t))
	require.NoError(t, err)
	admin := NewAdmin(client, &Config{}, zaptest.NewLogger(t))

	before := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	_, err = admin.PurgeByApp(context.Background(), "otel-traces", "trace", "my-app", before)
	require.NoError(t, err)

	// App-scoped index pattern (sanitized app id, no lowercase).
	assert.Contains(t, h.lastPath, "otel-traces-my-app-*")

	q := h.lastBody["query"].(map[string]any)
	boolQ := q["bool"].(map[string]any)
	must := boolQ["must"].([]any)
	require.Len(t, must, 2)

	// First clause: range on startTimeUnixNano with integer bound.
	rng := must[0].(map[string]any)["range"].(map[string]any)
	fieldSpec := rng[FieldStartTimeUnixNano].(map[string]any)
	lt, ok := fieldSpec["lt"].(float64)
	require.True(t, ok, "bound must be a JSON number, got %T", fieldSpec["lt"])
	assert.Equal(t, float64(before.UnixNano()), lt)

	// Second clause: term on the canonical top-level appId field.
	term := must[1].(map[string]any)["term"].(map[string]any)
	assert.Equal(t, "my-app", term[FieldAppID])
}

// TestBuildDeleteByQuery_Shapes verifies the shared builder directly.
func TestBuildDeleteByQuery_Shapes(t *testing.T) {
	// No app: bare range.
	q := buildDeleteByQuery("startTimeUnixNano", int64(123), "")
	rng, ok := q["range"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"lt": int64(123)}, rng["startTimeUnixNano"])

	// With app: bool.must(range, term(appId)).
	q = buildDeleteByQuery("timeUnixNano", int64(456), "app-1")
	boolQ, ok := q["bool"].(map[string]any)
	require.True(t, ok)
	must, ok := boolQ["must"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, must, 2)
	assert.Equal(t, map[string]any{"lt": int64(456)}, must[0]["range"].(map[string]any)["timeUnixNano"])
	assert.Equal(t, "app-1", must[1]["term"].(map[string]any)[FieldAppID])
}

// TestSignalTimestampField_Bound verifies the field/bound helpers per signal.
func TestSignalTimestampField_Bound(t *testing.T) {
	before := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, FieldStartTimeUnixNano, signalTimestampField("trace"))
	assert.Equal(t, FieldMetricTimeUnixMilli, signalTimestampField("metric"))
	assert.Equal(t, FieldLogTimeUnixNano, signalTimestampField("log"))

	assert.Equal(t, before.UnixNano(), signalTimestampBound("trace", before))
	assert.Equal(t, before.UnixMilli(), signalTimestampBound("metric", before))
	assert.Equal(t, before.UnixNano(), signalTimestampBound("log", before))
}
