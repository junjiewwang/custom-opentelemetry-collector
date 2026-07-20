// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/influxql"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// ═══════════════════════════════════════════════════
// InfluxDB v1 Compatible HTTP API (/api/influxdb/query)
//
// Supported InfluxQL subset:
//   SELECT <func>("value") FROM <measurement>
//   WHERE <tag> = 'value' [AND <tag> =~ /regex/]
//   GROUP BY time(<interval>)[, "tag1", "tag2"]
//   FILL(null|none|0|previous|linear)
//   LIMIT <n> SLIMIT <n> ORDER BY time ASC
//
// Grafana macros supported:
//   $timeFilter → time >= <start> AND time <= <end>
//   $__interval → auto-calculated step based on time range
// ═══════════════════════════════════════════════════

// influxdbQueryResponse is the InfluxDB v1 HTTP API response format.
type influxdbQueryResponse struct {
	Results []influxdbResult `json:"results"`
}

type influxdbResult struct {
	StatementID int               `json:"statement_id"`
	Series      []influxdbSeries  `json:"series,omitempty"`
	Error       string            `json:"error,omitempty"`
}

type influxdbSeries struct {
	Name    string              `json:"name,omitempty"`
	Tags    map[string]string   `json:"tags,omitempty"`
	Columns []string            `json:"columns"`
	Values  [][]any             `json:"values,omitempty"`
}

// handleInfluxDBPing handles GET /api/v2/influxdb/ping.
// InfluxDB standard ping returns 204 No Content with X-Influxdb-Version header.
// Grafana uses this to verify the InfluxDB instance is reachable.
func (e *Extension) handleInfluxDBPing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Influxdb-Version", "1.8.10-compatible")
	w.Header().Set("X-Influxdb-Build", "custom-otel-collector")
	w.WriteHeader(http.StatusNoContent)
}

// handleInfluxDBQuery handles POST /api/influxdb/query.
// This is the InfluxDB v1 compatible endpoint for Grafana to connect directly.
//
// Request format:
//
//	POST /api/influxdb/query?db=<database>&epoch=ms
//	Content-Type: application/x-www-form-urlencoded
//	q=SELECT mean("value") FROM "my_metric" WHERE $timeFilter GROUP BY time($__interval) FILL(null)
//
// Response format (InfluxDB v1):
//
//	{"results":[{"statement_id":0,"series":[{"name":"my_metric","columns":["time","value"],"values":[[time1,val1],...]}]}]}
func (e *Extension) handleInfluxDBQuery(w http.ResponseWriter, r *http.Request) {
	if e.storageMetricReader == nil {
		e.writeInfluxDBError(w, "metric reader not available")
		return
	}

	// 1. Parse request parameters
	if err := r.ParseForm(); err != nil {
		e.writeInfluxDBError(w, fmt.Sprintf("failed to parse form: %v", err))
		return
	}

	q := r.FormValue("q")
	if q == "" {
		q = r.URL.Query().Get("q")
	}
	if q == "" {
		e.writeInfluxDBError(w, "missing required parameter: q")
		return
	}

	db := r.URL.Query().Get("db")
	epoch := r.URL.Query().Get("epoch") // "ms" for milliseconds

	// 2. Parse time range from query parameters (for Grafana $timeFilter macro)
	_, _, timeRange := parseInfluxDBTimeRange(r)

	// 3. Replace Grafana macros in the query
	step := timeRange.End.Sub(timeRange.Start) / 60 // default: ~60 data points
	if step < time.Second {
		step = time.Second
	}
	q = replaceInfluxDBMacros(q, timeRange, step)

	// 4. Parse InfluxQL
	stmt, err := influxql.ParseStatement(q)
	if err != nil {
		e.writeInfluxDBError(w, fmt.Sprintf("InfluxQL parse error: %v", err))
		return
	}

	// 5. Handle non-SELECT statements (SHOW queries used by Grafana for connection test & autocomplete)
	sel, ok := stmt.(*influxql.SelectStatement)
	if !ok {
		e.handleInfluxDBShowQuery(w, r, stmt, db, epoch)
		return
	}

	// 5. Extract metric name (measurement)
	metricName, appID := extractMeasurement(sel, db)

	// 6. Extract aggregation function
	aggregation := extractAggregation(sel)

	// 7. Extract WHERE conditions → labels map
	labels, labelMatch := extractWhereConditions(sel, timeRange)

	// 8. Extract GROUP BY → groupBy tags + time interval
	groupByTags, stepDuration := extractGroupBy(sel, timeRange, step)
	if stepDuration > 0 {
		step = stepDuration
	}

	// 9. Extract FILL strategy
	fill := extractFill(sel)

	// 10. Extract LIMIT / SLIMIT
	limit := sel.Limit
	seriesLimit := sel.SLimit
	if seriesLimit == 0 {
		seriesLimit = 100
	}
	if limit == 0 {
		limit = 10000
	}

	// 11. Build MetricRangeQuery and execute
	query := observabilitystorageext.MetricRangeQuery{
		AppID:       appID,
		MetricName:  metricName,
		Labels:      labels,
		LabelMatch:  labelMatch,
		TimeRange:   timeRange,
		Aggregation: aggregation,
		Step:        step,
		GroupBy:     groupByTags,
		Fill:        fill,
		Limit:       limit,
		SeriesLimit: seriesLimit,
	}

	result, err := e.storageMetricReader.QueryRange(r.Context(), query)
	if err != nil {
		e.writeInfluxDBError(w, fmt.Sprintf("query execution failed: %v", err))
		return
	}

	// 12. Convert to InfluxDB v1 response format
	series := convertToInfluxDBSeries(result, metricName, epoch)

	e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
		Results: []influxdbResult{
			{
				StatementID: 0,
				Series:      series,
			},
		},
	})
}

// writeInfluxDBError writes an InfluxDB-compatible error response.
func (e *Extension) writeInfluxDBError(w http.ResponseWriter, msg string) {
	e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
		Results: []influxdbResult{
			{
				StatementID: 0,
				Error:       msg,
			},
		},
	})
}

// ═══════════════════════════════════════════════════
// InfluxQL → MetricRangeQuery Mappers
// ═══════════════════════════════════════════════════

// extractMeasurement returns the metric name and optional app_id from the FROM clause.
// Handles InfluxQL's dotted notation: "db"."rp"."measurement" → full dotted name.
func extractMeasurement(sel *influxql.SelectStatement, db string) (metricName, appID string) {
	if len(sel.Sources) > 0 {
		switch src := sel.Sources[0].(type) {
		case *influxql.Measurement:
			// Reconstruct full measurement name from parts.
			// InfluxQL parses "a.b.c" as Database="a", RetentionPolicy="b", Name="c".
			// We want the full dotted name if it has dots.
			name := strings.Trim(src.Name, `"`)
			if src.RetentionPolicy != "" {
				rp := strings.Trim(src.RetentionPolicy, `"`)
				dbPart := strings.Trim(src.Database, `"`)
				if dbPart != "" {
					name = dbPart + "." + rp + "." + name
				} else {
					name = rp + "." + name
				}
			} else if src.Database != "" {
				dbPart := strings.Trim(src.Database, `"`)
				name = dbPart + "." + name
			}
			metricName = name

			// Use the InfluxDB 'db' parameter as app_id if meaningful
			if db != "" && db != "_internal" && db != "telegraf" && db != "test" {
				appID = db
			}
		}
	}
	return metricName, appID
}

// extractAggregation returns the aggregation function name from SELECT clause.
// Supports: mean→avg, sum, max, min, count, last, first, percentile→pXX.
func extractAggregation(sel *influxql.SelectStatement) string {
	if len(sel.Fields) == 0 {
		return "avg"
	}

	for _, f := range sel.Fields {
		call, ok := f.Expr.(*influxql.Call)
		if !ok {
			continue
		}
		name := strings.ToLower(call.Name)

		// Map InfluxQL function names to our aggregation names
		switch name {
		case "mean":
			return "avg"
		case "sum", "max", "min", "count", "last", "first":
			return name
		case "percentile":
			// Extract percentile value from arguments
			if len(call.Args) >= 2 {
				if numLit, ok := call.Args[1].(*influxql.NumberLiteral); ok {
					pct := numLit.Val
					return fmt.Sprintf("p%d", int(pct))
				}
			}
			return "p95" // default
		default:
			return name
		}
	}

	return "avg"
}

// extractWhereConditions extracts label filters from the WHERE clause.
// Returns exact match labels and regex match labels separately.
func extractWhereConditions(sel *influxql.SelectStatement, tr observabilitystorageext.TimeRange) (labels, labelMatch map[string]string) {
	labels = make(map[string]string)
	labelMatch = make(map[string]string)

	if sel.Condition == nil {
		return labels, labelMatch
	}

	influxql.WalkFunc(sel.Condition, func(n influxql.Node) {
		binary, ok := n.(*influxql.BinaryExpr)
		if !ok {
			return
		}
		// Skip time conditions (already handled by timeRange)
		if isTimeCondition(binary) {
			return
		}

		key := extractTagKey(binary)
		if key == "" {
			return
		}

		switch binary.Op {
		case influxql.EQ:
			// Exact match: tag = 'value'
			if strLit, ok := binary.RHS.(*influxql.StringLiteral); ok {
				labels[key] = strLit.Val
			}
		case influxql.EQREGEX:
			// Regex match: tag =~ /pattern/
			if regexLit, ok := binary.RHS.(*influxql.RegexLiteral); ok {
				labelMatch[key] = regexLit.Val.String()
			}
		}
	})

	return labels, labelMatch
}

// isTimeCondition checks if a binary expression is a time filter.
func isTimeCondition(binary *influxql.BinaryExpr) bool {
	if binary.LHS == nil {
		return false
	}
	vr, ok := binary.LHS.(*influxql.VarRef)
	if !ok {
		return false
	}
	return vr.Val == "time"
}

// extractTagKey extracts the tag name from a binary expression's left side.
func extractTagKey(binary *influxql.BinaryExpr) string {
	vr, ok := binary.LHS.(*influxql.VarRef)
	if !ok {
		return ""
	}
	return vr.Val
}

// extractGroupBy extracts GROUP BY time(interval) and tag keys.
func extractGroupBy(sel *influxql.SelectStatement, tr observabilitystorageext.TimeRange, defaultStep time.Duration) (tagKeys []string, step time.Duration) {
	step = defaultStep

	for _, d := range sel.Dimensions {
		call, ok := d.Expr.(*influxql.Call)
		if !ok {
			// Plain tag reference: GROUP BY "tag_name"
			vr, ok := d.Expr.(*influxql.VarRef)
			if ok && vr.Val != "" {
				tagKeys = append(tagKeys, strings.Trim(vr.Val, `"`))
			}
			continue
		}
		// time() call: GROUP BY time(interval)
		if strings.ToLower(call.Name) == "time" && len(call.Args) > 0 {
			if durLit, ok := call.Args[0].(*influxql.DurationLiteral); ok {
				step = durLit.Val
			}
		}
	}
	return tagKeys, step
}

// extractFill extracts the FILL strategy from the SELECT statement.
func extractFill(sel *influxql.SelectStatement) string {
	switch sel.Fill {
	case influxql.NoFill:
		return "none"
	case influxql.NullFill:
		return "null"
	case influxql.PreviousFill:
		return "previous"
	case influxql.LinearFill:
		return "linear"
	case influxql.NumberFill:
		return "0" // number fill defaults to 0
	default:
		if sel.Fill != influxql.NullFill {
			// Check if it's a number fill
			_ = sel.Fill // handled above
		}
		return "null"
	}
}

// ═══════════════════════════════════════════════════
// Grafana Macro Support
// ═══════════════════════════════════════════════════

// replaceInfluxDBMacros replaces Grafana template macros with actual values.
func replaceInfluxDBMacros(q string, tr observabilitystorageext.TimeRange, step time.Duration) string {
	// $timeFilter → time >= start AND time <= end
	timeFilter := fmt.Sprintf("time >= %dms AND time <= %dms", tr.Start.UnixMilli(), tr.End.UnixMilli())
	q = strings.ReplaceAll(q, "$timeFilter", timeFilter)

	// $__interval → auto-calculated step (in seconds)
	intervalSeconds := int64(step.Seconds())
	if intervalSeconds < 1 {
		intervalSeconds = 1
	}
	q = strings.ReplaceAll(q, "$__interval", fmt.Sprintf("%ds", intervalSeconds))
	q = strings.ReplaceAll(q, "$__interval_ms", fmt.Sprintf("%dms", intervalSeconds*1000))

	return q
}

// parseInfluxDBTimeRange extracts start/end from query parameters or defaults.
func parseInfluxDBTimeRange(r *http.Request) (startMs, endMs int64, tr observabilitystorageext.TimeRange) {
	q := r.URL.Query()

	// Try explicit start/end parameters (milliseconds)
	if v := q.Get("start"); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			startMs = ms
		}
	}
	if v := q.Get("end"); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			endMs = ms
		}
	}

	// Fallback: parse from the InfluxQL q parameter's $timeFilter
	if startMs == 0 || endMs == 0 {
		// Default: last 1 hour
		now := time.Now()
		if startMs == 0 {
			startMs = now.Add(-1 * time.Hour).UnixMilli()
		}
		if endMs == 0 {
			endMs = now.UnixMilli()
		}
	}

	tr = observabilitystorageext.TimeRange{
		Start: time.UnixMilli(startMs),
		End:   time.UnixMilli(endMs),
	}
	return startMs, endMs, tr
}

// ═══════════════════════════════════════════════════
// MetricRangeResult → InfluxDB v1 Response Converter
// ═══════════════════════════════════════════════════

// ═══════════════════════════════════════════════════
// SHOW Query Handlers (Grafana Health Check & Autocomplete)
// ═══════════════════════════════════════════════════

// handleInfluxDBShowQuery handles non-SELECT InfluxQL statements.
// Grafana uses these for:
//   - "SHOW MEASUREMENTS" → test connection (health check)
//   - "SHOW DATABASES" → list databases
//   - "SHOW TAG KEYS" → autocomplete labels
//   - "SHOW TAG VALUES" → autocomplete label values
//   - "SHOW FIELD KEYS" → autocomplete fields
//   - "SHOW RETENTION POLICIES" → list retention policies
func (e *Extension) handleInfluxDBShowQuery(w http.ResponseWriter, r *http.Request, stmt influxql.Statement, db string, epoch string) {
	switch stmt.(type) {
	case *influxql.ShowMeasurementsStatement:
		// Health check: list available metrics
		e.handleShowMeasurements(w, r, db)
	case *influxql.ShowDatabasesStatement:
		// List available "databases" (app IDs)
		e.handleShowDatabases(w)
	case *influxql.ShowTagKeysStatement:
		// List available label keys for a metric
		e.handleShowTagKeys(w, r, stmt.(*influxql.ShowTagKeysStatement), db)
	case *influxql.ShowTagValuesStatement:
		// List available label values for a key
		e.handleShowTagValues(w, r, stmt.(*influxql.ShowTagValuesStatement), db)
	case *influxql.ShowFieldKeysStatement:
		// Metrics always have a single "value" field
		e.handleShowFieldKeys(w, stmt.(*influxql.ShowFieldKeysStatement))
	case *influxql.ShowRetentionPoliciesStatement:
		// Return a default retention policy
		e.handleShowRetentionPolicies(w)
	default:
		e.writeInfluxDBError(w, fmt.Sprintf("unsupported statement type: %T", stmt))
	}
}

// handleShowMeasurements returns available metric names.
// This is the primary health check query Grafana uses when testing the InfluxDB connection.
func (e *Extension) handleShowMeasurements(w http.ResponseWriter, r *http.Request, db string) {
	// Use a wide time range (last 30 days) for discovery
	now := time.Now()
	timeRange := observabilitystorageext.TimeRange{
		Start: now.Add(-30 * 24 * time.Hour),
		End:   now,
	}

	names, err := e.storageMetricReader.ListMetricNames(r.Context(), timeRange)
	if err != nil {
		// Even if listing fails, return an empty successful response
		// so Grafana considers the connection healthy
		e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
			Results: []influxdbResult{
				{
					StatementID: 0,
					Series: []influxdbSeries{
						{
							Name:    "measurements",
							Columns: []string{"name"},
							Values:  [][]any{},
						},
					},
				},
			},
		})
		return
	}

	values := make([][]any, 0, len(names))
	for _, name := range names {
		values = append(values, []any{name})
	}

	e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
		Results: []influxdbResult{
			{
				StatementID: 0,
				Series: []influxdbSeries{
					{
						Name:    "measurements",
						Columns: []string{"name"},
						Values:  values,
					},
				},
			},
		},
	})
}

// handleShowDatabases returns available "databases" (mapped to app IDs).
func (e *Extension) handleShowDatabases(w http.ResponseWriter) {
	// Return a minimal response that satisfies Grafana
	e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
		Results: []influxdbResult{
			{
				StatementID: 0,
				Series: []influxdbSeries{
					{
						Name:    "databases",
						Columns: []string{"name"},
						Values:  [][]any{{"_internal"}},
					},
				},
			},
		},
	})
}

// handleShowTagKeys returns label keys for a measurement.
func (e *Extension) handleShowTagKeys(w http.ResponseWriter, r *http.Request, stmt *influxql.ShowTagKeysStatement, _ string) {
	metricName := ""
	if len(stmt.Sources) > 0 {
		if m, ok := stmt.Sources[0].(*influxql.Measurement); ok {
			metricName = m.Name
		}
	}

	// Use a wide time range for discovery
	now := time.Now()
	timeRange := observabilitystorageext.TimeRange{
		Start: now.Add(-30 * 24 * time.Hour),
		End:   now,
	}

	labels, err := e.storageMetricReader.ListLabelNames(r.Context(), timeRange, "")
	if err != nil {
		e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
			Results: []influxdbResult{{StatementID: 0, Series: []influxdbSeries{}}},
		})
		return
	}

	values := make([][]any, 0, len(labels))
	for _, label := range labels {
		values = append(values, []any{label})
	}

	e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
		Results: []influxdbResult{
			{
				StatementID: 0,
				Series: []influxdbSeries{
					{
						Name:    metricName,
						Columns: []string{"tagKey"},
						Values:  values,
					},
				},
			},
		},
	})
}

// handleShowTagValues returns label values for a specific key.
func (e *Extension) handleShowTagValues(w http.ResponseWriter, r *http.Request, stmt *influxql.ShowTagValuesStatement, _ string) {
	metricName := ""
	if len(stmt.Sources) > 0 {
		if m, ok := stmt.Sources[0].(*influxql.Measurement); ok {
			metricName = m.Name
		}
	}

	// Extract tag key from the WITH KEY = condition
	tagKey := ""
	if stmt.Op == influxql.EQ && stmt.TagKeyExpr != nil {
		if lit, ok := stmt.TagKeyExpr.(*influxql.StringLiteral); ok {
			tagKey = lit.Val
		}
	}

	if tagKey == "" {
		e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
			Results: []influxdbResult{{StatementID: 0, Series: []influxdbSeries{}}},
		})
		return
	}

	// Use a wide time range for discovery
	now := time.Now()
	timeRange := observabilitystorageext.TimeRange{
		Start: now.Add(-30 * 24 * time.Hour),
		End:   now,
	}

	values, err := e.storageMetricReader.ListLabelValues(r.Context(), tagKey, timeRange)
	if err != nil {
		e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
			Results: []influxdbResult{{StatementID: 0, Series: []influxdbSeries{}}},
		})
		return
	}

	rows := make([][]any, 0, len(values))
	for _, v := range values {
		rows = append(rows, []any{tagKey, v})
	}

	e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
		Results: []influxdbResult{
			{
				StatementID: 0,
				Series: []influxdbSeries{
					{
						Name:    metricName,
						Columns: []string{"key", "value"},
						Values:  rows,
					},
				},
			},
		},
	})
}

// handleShowFieldKeys returns field keys for a measurement.
// Our metrics always have a single "value" field of type float.
func (e *Extension) handleShowFieldKeys(w http.ResponseWriter, stmt *influxql.ShowFieldKeysStatement) {
	metricName := ""
	if len(stmt.Sources) > 0 {
		if m, ok := stmt.Sources[0].(*influxql.Measurement); ok {
			metricName = m.Name
		}
	}

	e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
		Results: []influxdbResult{
			{
				StatementID: 0,
				Series: []influxdbSeries{
					{
						Name:    metricName,
						Columns: []string{"fieldKey", "fieldType"},
						Values:  [][]any{{"value", "float"}},
					},
				},
			},
		},
	})
}

// handleShowRetentionPolicies returns a default retention policy.
func (e *Extension) handleShowRetentionPolicies(w http.ResponseWriter) {
	e.writeJSON(w, http.StatusOK, influxdbQueryResponse{
		Results: []influxdbResult{
			{
				StatementID: 0,
				Series: []influxdbSeries{
					{
						Name:    "retention_policies",
						Columns: []string{"name", "duration", "shardGroupDuration", "replicaN", "default"},
						Values:  [][]any{{"autogen", "0s", "168h0m0s", 1, true}},
					},
				},
			},
		},
	})
}

// ═══════════════════════════════════════════════════
// MetricRangeResult → InfluxDB v1 Response Converter
// ═══════════════════════════════════════════════════

// convertToInfluxDBSeries converts our MetricRangeResult to InfluxDB v1 series format.
func convertToInfluxDBSeries(result *observabilitystorageext.MetricRangeResult, metricName string, epoch string) []influxdbSeries {
	if result == nil || len(result.Data) == 0 {
		return nil
	}

	timeFormat := epoch
	series := make([]influxdbSeries, 0, len(result.Data))

	for _, s := range result.Data {
		// Build tags (excluding internal keys)
		tags := make(map[string]string)
		for k, v := range s.Labels {
			if k == "__name__" || k == "metric" {
				continue
			}
			tags[k] = v
		}

		// Build values
		values := make([][]any, 0, len(s.Values))
		for _, v := range s.Values {
			// Skip NaN values (null fill)
			if math.IsNaN(v.Value) {
				continue
			}

			var timeVal any
			switch timeFormat {
			case "ms":
				ms, err := strconv.ParseInt(v.TimeUnixMilli, 10, 64)
				if err == nil {
					timeVal = ms
				} else {
					timeVal = v.TimeUnixMilli
				}
			default:
				// RFC3339 format
				ms, err := strconv.ParseInt(v.TimeUnixMilli, 10, 64)
				if err == nil {
					timeVal = time.UnixMilli(ms).UTC().Format(time.RFC3339)
				} else {
					timeVal = v.TimeUnixMilli
				}
			}

			values = append(values, []any{timeVal, v.Value})
		}

		// Only include series if there are values
		if len(values) == 0 {
			continue
		}

		influxSeries := influxdbSeries{
			Name:    metricName,
			Columns: []string{"time", "value"},
			Values:  values,
		}
		if len(tags) > 0 {
			influxSeries.Tags = tags
		}
		series = append(series, influxSeries)
	}

	return series
}
