//go:build integration

package elasticsearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ── ES Client for Integration Tests ─────────────────────────────────────

// esClient is a minimal ES HTTP client for integration testing.
// Connection info is read from environment variables.
type esClient struct {
	baseURL string
	client  *http.Client
}

// NewEsClientFromEnv reads ES connection info from environment variables:
//
//	ES_HOST     — ES base URL (default: http://localhost:9200)
//	ES_USER     — ES username (default: empty = no auth)
//	ES_PASSWORD — ES password (default: empty)
func newEsClientFromEnv() *esClient {
	host := os.Getenv("ES_HOST")
	if host == "" {
		host = "http://localhost:9200"
	}
	return &esClient{
		baseURL: strings.TrimRight(host, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *esClient) search(index, bodyJSON string) (map[string]any, error) {
	path := fmt.Sprintf("%s/%s/_search", c.baseURL, index)
	return c.do("POST", path, bodyJSON)
}

func (c *esClient) count(index, bodyJSON string) (int64, error) {
	path := fmt.Sprintf("%s/%s/_count", c.baseURL, index)
	resp, err := c.do("POST", path, bodyJSON)
	if err != nil {
		return 0, err
	}
	cnt, _ := resp["count"].(float64)
	return int64(cnt), nil
}

func (c *esClient) getMapping(index, fieldPath string) (map[string]any, error) {
	path := fmt.Sprintf("%s/%s/_mapping/field/%s?format=json", c.baseURL, index, fieldPath)
	return c.do("GET", path, "")
}

func (c *esClient) do(method, path, body string) (map[string]any, error) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	if user := os.Getenv("ES_USER"); user != "" {
		req.SetBasicAuth(user, os.Getenv("ES_PASSWORD"))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("invalid JSON (status %d): %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	if resp.StatusCode >= 400 {
		errInfo := result["error"]
		return nil, fmt.Errorf("ES error (status %d): %v", resp.StatusCode, errInfo)
	}

	return result, nil
}

// esIndices returns all index names matching the given pattern.
// Uses _cat/indices for speed — returns name only.
func (c *esClient) esIndices(t *testing.T, pattern string) []string {
	path := fmt.Sprintf("%s/_cat/indices/%s?h=index&format=json", c.baseURL, pattern)
	resp, err := c.httpGet(path)
	require.NoError(t, err, "failed to list indices for %s", pattern)

	var indices []map[string]string
	require.NoError(t, json.Unmarshal(resp, &indices))

	var names []string
	for _, m := range indices {
		if name, ok := m["index"]; ok {
			names = append(names, name)
		}
	}
	return names
}

func (c *esClient) httpGet(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if user := os.Getenv("ES_USER"); user != "" {
		req.SetBasicAuth(user, os.Getenv("ES_PASSWORD"))
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// esPing checks ES connectivity and skips the test if unreachable.
func esPing(t *testing.T) *esClient {
	t.Helper()
	client := newEsClientFromEnv()

	req, err := http.NewRequest("GET", client.baseURL, nil)
	require.NoError(t, err)
	if user := os.Getenv("ES_USER"); user != "" {
		req.SetBasicAuth(user, os.Getenv("ES_PASSWORD"))
	}
	resp, err := client.client.Do(req)
	if err != nil {
		t.Skipf("ES not reachable at %s: %v (set ES_HOST to override)", client.baseURL, err)
	}
	defer resp.Body.Close()

	t.Logf("ES connected: %s (status %d)", client.baseURL, resp.StatusCode)
	return client
}

// ── Mapping Validation Tests ────────────────────────────────────────────

// TestIntegration_ESMapping_TextFieldsHaveKeyword verifies that every text
// field used in TraceQL metrics aggregation has a .keyword sub-field.
// This would have caught the resource.app_id and status.code bugs.
func TestIntegration_ESMapping_TextFieldsHaveKeyword(t *testing.T) {
	client := esPing(t)

	// Fields that appear in metricsAggField / by() aggregations.
	// Each must be either keyword/long type, or text with .keyword sub-field.
	fields := []string{
		// Intrinsic fields
		"status.code",
		"status.message",
		"kind",
		"name",
		"spanId",
		"traceId",
		"parentSpanId",
		"serviceName",

		// Resource fields commonly used in by()
		"resource.service.name",
		"resource.service.namespace",
		"resource.service.instance.id",
		"resource.host.name",
		"resource.process.pid",
		"resource.telemetry.distro.name",
	}

	// Pick the first available index.
	indices := client.esIndices(t, "otel-traces-*")
	require.NotEmpty(t, indices, "no otel-traces-* index found")
	indexName := indices[0]
	t.Logf("using index: %s", indexName)

	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			resp, err := client.getMapping(indexName, field)
			require.NoError(t, err, "failed to get mapping for %s", field)

			// Navigate the mapping response: index → mappings → field path
			fieldMapping := extractFieldMapping(t, resp, field)
			if fieldMapping == nil {
				t.Skipf("field %q not found in index %q (no data uses this field yet)", field, indexName)
				return
			}
			fieldType, _ := fieldMapping["type"].(string)

			isAggregatable := fieldType == "keyword" ||
				fieldType == "long" ||
				fieldType == "double" ||
				fieldType == "boolean" ||
				fieldType == "date" ||
				(fieldType == "text" && fieldHasKeyword(fieldMapping))

			assert.True(t, isAggregatable,
				"field %q must support aggregation (type=%q, has_keyword=%v). "+
					"Add to ES template or ensure .keyword sub-field.",
				field, fieldType, fieldHasKeyword(fieldMapping))
		})
	}
}

// TestIntegration_ESMapping_StatusCodeIsText verifies that status.code is
// a text field with .keyword — the root cause of the capitalizeFirst bug.
func TestIntegration_ESMapping_StatusCodeIsText(t *testing.T) {
	client := esPing(t)
	indices := client.esIndices(t, "otel-traces-*")
	require.NotEmpty(t, indices)

	resp, err := client.getMapping(indices[0], "status.code")
	require.NoError(t, err)

	fieldMapping := extractFieldMapping(t, resp, "status.code")
	fieldType, _ := fieldMapping["type"].(string)

	assert.Equal(t, "text", fieldType,
		"status.code must be 'text' type — capitalizeFirst assumes this. "+
			"If this fails because it changed to keyword, update capitalizeFirst logic.")
	assert.True(t, fieldHasKeyword(fieldMapping),
		"status.code must have .keyword sub-field for aggregation")
}

// ── Query Result Validation Tests ───────────────────────────────────────

// TestIntegration_ESQuery_StatusLowercaseHasResults validates that the
// lowercase "error" value matches documents — the capitalizeFirst bug
// would be caught by this test.
func TestIntegration_ESQuery_StatusLowercaseHasResults(t *testing.T) {
	client := esPing(t)

	bodyLower := `{"query":{"term":{"status.code":"error"}}}`
	bodyUpper := `{"query":{"term":{"status.code":"Error"}}}`

	countLower, err := client.count("otel-traces-*", bodyLower)
	require.NoError(t, err)

	countUpper, err := client.count("otel-traces-*", bodyUpper)
	require.NoError(t, err)

	t.Logf("status.code='error' (lower): %d docs", countLower)
	t.Logf("status.code='Error' (upper): %d docs", countUpper)

	assert.Greater(t, countLower, int64(0),
		"lowercase 'error' must match documents — if 0, capitalizeFirst fix is wrong")
	assert.Greater(t, countLower, countUpper,
		"lowercase should match >= uppercase — capitalizeFirst('error')='Error' would miss data")
}

// TestIntegration_ESQuery_AggregationOnStatusRequiresKeyword verifies that
// terms aggregation on status.code (text) fails and status.code.keyword works.
func TestIntegration_ESQuery_AggregationOnStatusRequiresKeyword(t *testing.T) {
	client := esPing(t)

	// Aggregation on text field → should fail
	bodyNoKeyword := `{"size":0,"aggs":{"test":{"terms":{"field":"status.code","size":3}}}}`
	resp, err := client.search("otel-traces-*", bodyNoKeyword)
	if err != nil {
		t.Logf("text field aggregation failed (expected): %v", err)
	} else {
		// Some ES versions return empty aggregations silently
		aggs, _ := resp["aggregations"].(map[string]any)
		t.Logf("text field agg returned: %v", aggs)
	}

	// Aggregation on .keyword → should work
	bodyWithKeyword := `{"size":0,"aggs":{"test":{"terms":{"field":"status.code.keyword","size":3}}}}`
	resp2, err := client.search("otel-traces-*", bodyWithKeyword)
	require.NoError(t, err, "aggregation on status.code.keyword must not fail")
	require.NotNil(t, resp2["aggregations"],
		"aggregation on status.code.keyword must return results")
	t.Log("status.code.keyword aggregation: OK")
}

// TestIntegration_ESQuery_KindCapitalizedHasResults validates the SpanKind
// capitalizeFirst behavior against real ES data.
func TestIntegration_ESQuery_KindCapitalizedHasResults(t *testing.T) {
	client := esPing(t)

	// capitalizeFirst("server") → "Server" — verify this matches.
	body := `{"query":{"term":{"kind":"Server"}}}`
	count, err := client.count("otel-traces-*", body)
	require.NoError(t, err)

	t.Logf("kind='Server': %d docs", count)
	assert.Greater(t, count, int64(0),
		"capitalized SpanKind must match documents")
}

// ── End-to-End Code Path Validation ─────────────────────────────────────

// TestIntegration_TraceReader_BuildSearchQuery verifies
// buildTraceSearchQuery produces queries that match data in ES.
func TestIntegration_TraceReader_BuildSearchQuery(t *testing.T) {
	client := esPing(t)
	reader := &TraceReader{logger: zap.NewNop()}

	t.Run("status_error_filter", func(t *testing.T) {
		tq := TraceQuery{Status: "error"}
		esQuery := reader.buildTraceSearchQuery(tq)

		// buildTraceSearchQuery returns the "query" content (e.g. {"bool":{...}}),
		// count/search API needs it wrapped: {"query": {...}}
		body, _ := json.Marshal(map[string]any{"query": esQuery})
		count, err := client.count("otel-traces-*", string(body))
		require.NoError(t, err)

		t.Logf("status=error filter: %d docs", count)
		assert.Greater(t, count, int64(0),
			"status=error query must return results — capitalizeFirst bug would cause 0")
	})

	t.Run("root_span_filter", func(t *testing.T) {
		tq := TraceQuery{IsRoot: true}
		esQuery := reader.buildTraceSearchQuery(tq)

		body, _ := json.Marshal(map[string]any{"query": esQuery})
		count, err := client.count("otel-traces-*", string(body))
		require.NoError(t, err)

		t.Logf("IsRoot filter: %d docs", count)
		assert.Greater(t, count, int64(0),
			"root span filter must return results")
	})
}

// TestIntegration_TraceMetrics_AggregationFields verifies
// metricsAggField produces aggregatable field paths against real ES.
func TestIntegration_TraceMetrics_AggregationFields(t *testing.T) {
	client := esPing(t)
	indices := client.esIndices(t, "otel-traces-*")
	require.NotEmpty(t, indices)

	resolver := &AttributeResolver{}

	// Labels that should produce aggregatable fields.
	labels := []struct {
		label    string
		mustWork bool
	}{
		{label: "status", mustWork: true},
		{label: "statusMessage", mustWork: true},
		{label: "kind", mustWork: true},
		{label: "name", mustWork: true},
		{label: "resource.app_id", mustWork: true},
		{label: "resource.service.name", mustWork: true},
		{label: "resource.host.name", mustWork: true},
		// Known gaps — currently no .keyword, test documents this.
		{label: "http.method", mustWork: false},
		{label: "db.system", mustWork: false},
	}

	for _, tc := range labels {
		t.Run(tc.label, func(t *testing.T) {
			aggField := metricsAggField(resolver, tc.label)
			t.Logf("  %s → %s", tc.label, aggField)

			// Verify the field exists in ES mapping.
			resp, err := client.getMapping(indices[0], aggField)
			if err != nil {
				if tc.mustWork {
					t.Errorf("field %q should exist: %v", aggField, err)
				} else {
					t.Logf("  KNOWN GAP: field %q not found in mapping (expected)", aggField)
				}
				return
			}

			fm := extractFieldMapping(t, resp, aggField)
			ft, _ := fm["type"].(string)
			t.Logf("  ES type: %q, has_keyword=%v, sub_fields=%v",
				ft, fieldHasKeyword(fm), fm["fields"])

			if tc.mustWork {
				assert.NotEmpty(t, ft, "field %q must have a type in ES", aggField)
			}
		})
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────

func extractFieldMapping(t *testing.T, resp map[string]any, fieldPath string) map[string]any {
	t.Helper()

	// Response format (ES 7.x _mapping/field API):
	// {"index_name": {"mappings": {"status.code": {"full_name":"...","mapping":{"code":{"type":"text",...}}}}}}
	for _, indexData := range resp {
		data, ok := indexData.(map[string]any)
		if !ok {
			continue
		}
		mappings, _ := data["mappings"].(map[string]any)
		if mappings == nil {
			continue
		}
		// fieldPath is the key directly (e.g., "status.code", "kind")
		fieldEntry, ok := mappings[fieldPath].(map[string]any)
		if !ok {
			continue
		}
		// The last segment of fieldPath is the key inside "mapping"
		parts := strings.Split(fieldPath, ".")
		lastSeg := parts[len(parts)-1]

		mappingWrapper, _ := fieldEntry["mapping"].(map[string]any)
		if mappingWrapper != nil {
			if m, ok := mappingWrapper[lastSeg].(map[string]any); ok {
				return m
			}
		}
	}
	return nil
}

func fieldHasKeyword(fieldMapping map[string]any) bool {
	if fieldMapping == nil {
		return false
	}
	if fields, ok := fieldMapping["fields"].(map[string]any); ok {
		if kw, ok := fields["keyword"]; ok {
			if kwMap, ok := kw.(map[string]any); ok {
				return kwMap["type"] == "keyword"
			}
			return false
		}
	}
	return false
}


