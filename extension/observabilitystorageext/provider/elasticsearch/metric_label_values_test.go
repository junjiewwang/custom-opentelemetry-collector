// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawLabelDoc builds a canned metric SearchHit whose `labels` object carries
// the given JSON (so callers can embed typed values — strings, numbers, bools —
// exactly as OTel attributes arrive).
func rawLabelDoc(t *testing.T, labelsJSON string) SearchHit {
	t.Helper()
	src := json.RawMessage(fmt.Sprintf(`{"labels":%s}`, labelsJSON))
	return SearchHit{Source: src}
}

// labelValuesReader builds a MetricReader backed by a fakeSearcher returning a
// fixed set of documents, so each subtest gets a fresh reader with data.
func labelValuesReader(t *testing.T) *MetricReader {
	t.Helper()
	resp := &SearchResponse{}
	resp.Hits.Hits = []SearchHit{
		rawLabelDoc(t, `{"http_request_method":"GET","http_response_status_code":200,"jvm_thread_daemon":true}`),
		rawLabelDoc(t, `{"http_request_method":"POST","http_response_status_code":401}`),
		rawLabelDoc(t, `{"http_request_method":"GET"}`),     // duplicate GET → deduped
		rawLabelDoc(t, `{"http_response_status_code":200}`), // numeric dup → deduped
		rawLabelDoc(t, `{"server_address":"localhost"}`),
	}
	fs := &fakeSearcher{Responses: []any{resp}}
	return &MetricReader{
		searcher: fs,
		config:   &Config{Metrics: IndexConfig{IndexPrefix: "metrics"}},
	}
}

// TestListLabelValuesForMetric_RawDocExtraction verifies the fix for the
// Grafana breakdown value picker: label values are read from the stored
// `labels` object on documents (like ListLabelNames / QueryFlat), NOT from an
// ES `terms` aggregation on `labels.<key>.keyword` — the latter returns empty
// buckets / illegal_argument for string labels in the running indices.
//
// It exercises every label type through the metricLabels decoder: string,
// numeric, and boolean attributes must all normalize to their string form.
func TestListLabelValuesForMetric_RawDocExtraction(t *testing.T) {
	t.Run("string label", func(t *testing.T) {
		r := labelValuesReader(t)
		vals, err := r.ListLabelValuesForMetric(context.Background(), "http_request_method", "http.server.request.duration", TimeRange{})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"GET", "POST"}, vals)
	})

	t.Run("numeric label normalizes to string", func(t *testing.T) {
		r := labelValuesReader(t)
		vals, err := r.ListLabelValuesForMetric(context.Background(), "http_response_status_code", "http.server.request.duration", TimeRange{})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"200", "401"}, vals)
	})

	t.Run("boolean label normalizes to string", func(t *testing.T) {
		r := labelValuesReader(t)
		vals, err := r.ListLabelValuesForMetric(context.Background(), "jvm_thread_daemon", "jvm.thread.count", TimeRange{})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"true"}, vals)
	})

	t.Run("label key with dots normalizes (http.request.method → http_request_method)", func(t *testing.T) {
		// Handler maps prometheus "http_request_method" via prometheusToOtelLabelKeys;
		// regardless, the stored key is the sanitized underscore form.
		r := labelValuesReader(t)
		vals, err := r.ListLabelValuesForMetric(context.Background(), "http.request.method", "http.server.request.duration", TimeRange{})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"GET", "POST"}, vals)
	})

	t.Run("unscoped (metricName empty) still extracts", func(t *testing.T) {
		r := labelValuesReader(t)
		vals, err := r.ListLabelValuesForMetric(context.Background(), "server_address", "", TimeRange{})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"localhost"}, vals)
	})

	t.Run("emitted query is a raw-doc search, not a terms aggregation", func(t *testing.T) {
		r := labelValuesReader(t)
		_, err := r.ListLabelValuesForMetric(context.Background(), "http_request_method", "http.server.request.duration", TimeRange{})
		require.NoError(t, err)
		// Exactly one call for this subtest.
		fs := r.searcher.(*fakeSearcher)
		require.Len(t, fs.Requests, 1)
		req := fs.Requests[0]
		assert.Greater(t, req.Size, 0, "must sample documents, not aggregate")
		assert.Nil(t, req.Aggregations, "must NOT use an ES terms aggregation (the broken path)")
		// metric scoping term present.
		boolQ, ok := req.Query["bool"].(map[string]any)
		require.True(t, ok, "scoped query must wrap in bool.must")
		must := boolQ["must"].([]map[string]any)
		require.Len(t, must, 2)
		termQ := must[1]["term"].(map[string]any)
		assert.Equal(t, "http.server.request.duration", termQ[FieldName])
	})
}
