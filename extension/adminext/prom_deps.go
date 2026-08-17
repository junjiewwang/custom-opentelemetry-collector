// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// promHandlers holds the dependencies required by the Prometheus-compatible API
// handlers, decoupling them from *Extension (P1 dependency-injection seam).
type promHandlers struct {
	metricReader     observabilitystorageext.MetricReader
	traceReader      observabilitystorageext.TraceReader
	logger           *zap.Logger
	// Full PromQL engine backed by ES→storage.Queryable adapter.
	// Falls back to the subset parser (parsePromQL) for expressions
	// the engine cannot handle (e.g. unsupported histogram_quantile).
	queryable *esQueryable
	engine    *promql.Engine
}

func newPromHandlers(e *Extension) *promHandlers {
	queryable := newESQueryable(e.storageMetricReader, e.logger)
	engine := promql.NewEngine(promql.EngineOpts{
		Logger:               nil,
		MaxSamples:           50_000_000,
		Timeout:              60 * time.Second,
		LookbackDelta:        5 * time.Minute,
		EnableAtModifier:     true,
		EnableNegativeOffset: true,
	})
	return &promHandlers{
		metricReader: e.storageMetricReader,
		traceReader:  e.storageTraceReader,
		logger:       e.logger,
		queryable:    queryable,
		engine:       engine,
	}
}

// tryPromQLRange executes the PromQL expression as a range query over
// [start, end] with the given step. Returns nil on failure so the caller
// falls back to the subset parser.
func (h *promHandlers) tryPromQLRange(ctx context.Context, queryStr string, start, end time.Time, step time.Duration) *promQueryData {
	if h.engine == nil || h.queryable == nil {
		return nil
	}
	queryStr = normalizeQueryForPromQL(queryStr)

	q, err := h.engine.NewRangeQuery(ctx, h.queryable, nil, queryStr, start, end, step)
	if err != nil {
		h.logger.Error("promql range query parse failed", zap.String("query", queryStr), zap.Error(err))
		return nil
	}
	defer q.Close()
	res := q.Exec(ctx)
	if res.Err != nil {
		h.logger.Error("promql range query exec failed", zap.String("query", queryStr), zap.Error(res.Err))
		return nil
	}
	if res.Value == nil {
		return nil
	}
	if res.Value.Type() != parser.ValueTypeMatrix {
		return nil
	}
	m, _ := res.Matrix()
	matrix := make([]promMatrixSample, 0, len(m))
	for _, s := range m {
		metric := make(map[string]string, len(s.Metric))
		for _, l := range s.Metric {
			metric[l.Name] = l.Value
		}
		values := make([][]any, len(s.Floats))
		for i, fp := range s.Floats {
			values[i] = []any{float64(fp.T) / 1000, fmt.Sprintf("%f", fp.F)}
		}
		matrix = append(matrix, promMatrixSample{Metric: metric, Values: values})
	}
	return &promQueryData{ResultType: ResultTypeMatrix, Result: matrix}
}

// tryPromQL executes the PromQL expression via the full promql.Engine and returns
// the serialized result. Returns nil on any error so the caller falls back to
// the subset parsePromQL parser.
func (h *promHandlers) tryPromQL(ctx context.Context, queryStr string, evalTime time.Time) *promQueryData {
	if h.engine == nil || h.queryable == nil {
		return nil
	}
	// Normalize OTel dotted metric names (jvm.memory.used) to Prometheus-safe
	// underscored names (jvm_memory_used) so the parser doesn't reject them.
	queryStr = normalizeQueryForPromQL(queryStr)

	q, err := h.engine.NewInstantQuery(ctx, h.queryable, nil, queryStr, evalTime)
	if err != nil {
		h.logger.Error("promql engine NewInstantQuery failed", zap.String("query", queryStr), zap.Error(err))
		return nil
	}
	defer q.Close()
	res := q.Exec(ctx)
	if res.Err != nil {
		h.logger.Error("promql engine Exec failed", zap.String("query", queryStr), zap.Error(res.Err))
		return nil
	}
	if res.Value == nil {
		h.logger.Warn("promql engine returned nil Value",
			zap.String("query", queryStr),
		)
		return nil
	}
	h.logger.Debug("promql engine result",
		zap.String("query", queryStr),
		zap.String("type", fmt.Sprintf("%v", res.Value.Type())),
	)

	switch res.Value.Type() {
	case parser.ValueTypeVector:
		v, _ := res.Vector()
		vectors := make([]promVectorSample, 0, len(v))
		for _, s := range v {
			metric := make(map[string]string, len(s.Metric))
			for _, l := range s.Metric {
				metric[l.Name] = l.Value
			}
			vectors = append(vectors, promVectorSample{
				Metric: metric,
				Value:  []any{float64(s.T) / 1000, fmt.Sprintf("%f", s.F)},
			})
		}
		h.logger.Debug("promql vector result",
			zap.String("query", queryStr),
			zap.Int("items", len(vectors)),
		)
		return &promQueryData{ResultType: ResultTypeVector, Result: vectors}
	case parser.ValueTypeMatrix:
		m, _ := res.Matrix()
		matrix := make([]promMatrixSample, 0, len(m))
		for _, s := range m {
			metric := make(map[string]string, len(s.Metric))
			for _, l := range s.Metric {
				metric[l.Name] = l.Value
			}
			values := make([][]any, len(s.Floats))
			for i, fp := range s.Floats {
				values[i] = []any{float64(fp.T) / 1000, fmt.Sprintf("%f", fp.F)}
			}
			matrix = append(matrix, promMatrixSample{Metric: metric, Values: values})
		}
		return &promQueryData{ResultType: ResultTypeMatrix, Result: matrix}
	// Scalar → delegate to existing parser (writePromScalar).
	case parser.ValueTypeScalar:
		return nil
	}
	return nil
}

// normalizeQueryForPromQL replaces dots in OTel metric names with underscores.
// isComplexPromQL detects expressions that the subset parser cannot handle.
// Range vector functions (rate, delta, deriv) and arithmetic division are
// the only categories that go to the full engine. sum/max/avg and basic
// arithmetic are still routed to the old parser to preserve existing tests.
func isComplexPromQL(q string) bool {
	lower := strings.ToLower(q)
	// Range vector functions
	for _, fn := range []string{"rate(", "delta(", "deriv(", "idelta(", "irate("} {
		if strings.Contains(lower, fn) {
			return true
		}
	}
	// Division (old parser doesn't reliably handle it)
	if strings.Contains(q, "/") {
		return true
	}
	return false
}

// normalizeQueryForPromQL uses a regex-based approach that handles all
// dotted metric names including those inside function calls (delta, rate,
// deriv) and arithmetic expressions. This is far more robust than a
// hardcoded string replacer.
func normalizeQueryForPromQL(q string) string {
	return dottedMetricNameRE.ReplaceAllStringFunc(q, func(match string) string {
		return strings.ReplaceAll(match, ".", "_")
	})
}

// dottedMetricNameRE matches a series of dot-separated identifiers that
// look like OTel metric names (at least two segments with at least one dot).
// Examples: jvm.memory.used, jvm.gc.duration, demo.runtime.jvm.gc.timeOrCount.v2
var dottedMetricNameRE = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9]*(?:\.[a-zA-Z][a-zA-Z0-9]*)+`)
