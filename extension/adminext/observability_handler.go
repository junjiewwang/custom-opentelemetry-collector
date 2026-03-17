// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// ============================================================================
// Observability Query Proxy
//
// 作为 Query Proxy 将前端请求透传到 Jaeger Query API 和 Prometheus HTTP API。
// 设计原则：
//   - 前端不直接访问 Jaeger/Prometheus，统一走 Admin API 鉴权
//   - Jaeger Query API: https://www.jaegertracing.io/docs/apis/#http-json
//   - Prometheus HTTP API: https://prometheus.io/docs/prometheus/latest/querying/api/
// ============================================================================

// observabilityClient 封装 Jaeger 和 Prometheus 的 HTTP 查询客户端。
type observabilityClient struct {
	jaegerEndpoint     string
	prometheusEndpoint string
	httpClient         *http.Client
	logger             *zap.Logger
}

// newObservabilityClient 创建可观测性查询客户端。
func newObservabilityClient(logger *zap.Logger, cfg ObservabilityConfig) *observabilityClient {
	return &observabilityClient{
		jaegerEndpoint:     strings.TrimRight(cfg.Jaeger.Endpoint, "/"),
		prometheusEndpoint: strings.TrimRight(cfg.Prometheus.Endpoint, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// ============================================================================
// Trace 查询 API Handlers（代理 Jaeger Query API）
// ============================================================================

// handleGetTraceServices 获取 Jaeger 中所有可用的 Service 列表。
// GET /api/v2/observability/traces/services
// → Jaeger API: GET /api/services
func (e *Extension) handleGetTraceServices(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.jaegerEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Jaeger query backend not configured")
		return
	}

	targetURL := fmt.Sprintf("%s/api/services", e.obsClient.jaegerEndpoint)
	e.proxyGET(w, r, targetURL)
}

// handleGetTraceOperations 获取指定 Service 的所有 Operation。
// GET /api/v2/observability/traces/services/{service}/operations
// → Jaeger API: GET /api/services/{service}/operations
func (e *Extension) handleGetTraceOperations(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.jaegerEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Jaeger query backend not configured")
		return
	}

	service := chi.URLParam(r, "service")
	if service == "" {
		e.writeError(w, http.StatusBadRequest, "service parameter is required")
		return
	}

	targetURL := fmt.Sprintf("%s/api/services/%s/operations", e.obsClient.jaegerEndpoint, url.PathEscape(service))
	e.proxyGET(w, r, targetURL)
}

// handleSearchTraces 搜索 Traces。
// GET /api/v2/observability/traces?service=xxx&operation=xxx&tags=xxx&limit=20&start=xxx&end=xxx&minDuration=xxx&maxDuration=xxx
// → Jaeger API: GET /api/traces?service=xxx&...
func (e *Extension) handleSearchTraces(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.jaegerEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Jaeger query backend not configured")
		return
	}

	// 透传前端的查询参数到 Jaeger
	targetURL := fmt.Sprintf("%s/api/traces?%s", e.obsClient.jaegerEndpoint, r.URL.RawQuery)
	e.proxyGET(w, r, targetURL)
}

// handleGetTrace 获取单个 Trace 的详细信息。
// GET /api/v2/observability/traces/{traceID}
// → Jaeger API: GET /api/traces/{traceID}
func (e *Extension) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.jaegerEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Jaeger query backend not configured")
		return
	}

	traceID := chi.URLParam(r, "traceID")
	if traceID == "" {
		e.writeError(w, http.StatusBadRequest, "traceID parameter is required")
		return
	}

	targetURL := fmt.Sprintf("%s/api/traces/%s", e.obsClient.jaegerEndpoint, url.PathEscape(traceID))
	e.proxyGET(w, r, targetURL)
}

// handleGetDependencies 获取服务间依赖关系（用于 Service Map）。
// GET /api/v2/observability/dependencies?endTs=xxx&lookback=xxx
// → Jaeger API: GET /api/dependencies?endTs=xxx&lookback=xxx
// endTs: 结束时间（Unix 毫秒），lookback: 回溯时长（毫秒）
func (e *Extension) handleGetDependencies(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.jaegerEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Jaeger query backend not configured")
		return
	}

	targetURL := fmt.Sprintf("%s/api/dependencies?%s", e.obsClient.jaegerEndpoint, r.URL.RawQuery)
	e.proxyGET(w, r, targetURL)
}

// ============================================================================
// Metric 查询 API Handlers（代理 Prometheus HTTP API）
// ============================================================================

// handleMetricLabels 获取 Prometheus 中所有可用的 label 名称。
// GET /api/v2/observability/metrics/labels
// → Prometheus API: GET /api/v1/labels
func (e *Extension) handleMetricLabels(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.prometheusEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Prometheus query backend not configured")
		return
	}

	targetURL := fmt.Sprintf("%s/api/v1/labels", e.obsClient.prometheusEndpoint)
	e.proxyGET(w, r, targetURL)
}

// handleMetricLabelValues 获取指定 label 的所有值。
// GET /api/v2/observability/metrics/labels/{labelName}/values
// → Prometheus API: GET /api/v1/label/{labelName}/values
func (e *Extension) handleMetricLabelValues(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.prometheusEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Prometheus query backend not configured")
		return
	}

	labelName := chi.URLParam(r, "labelName")
	if labelName == "" {
		e.writeError(w, http.StatusBadRequest, "labelName parameter is required")
		return
	}

	targetURL := fmt.Sprintf("%s/api/v1/label/%s/values", e.obsClient.prometheusEndpoint, url.PathEscape(labelName))
	e.proxyGET(w, r, targetURL)
}

// handleMetricQuery 执行 Prometheus instant query。
// GET /api/v2/observability/metrics/query?query=xxx&time=xxx
// → Prometheus API: GET /api/v1/query?query=xxx&time=xxx
func (e *Extension) handleMetricQuery(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.prometheusEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Prometheus query backend not configured")
		return
	}

	targetURL := fmt.Sprintf("%s/api/v1/query?%s", e.obsClient.prometheusEndpoint, r.URL.RawQuery)
	e.proxyGET(w, r, targetURL)
}

// handleMetricQueryRange 执行 Prometheus range query。
// GET /api/v2/observability/metrics/query_range?query=xxx&start=xxx&end=xxx&step=xxx
// → Prometheus API: GET /api/v1/query_range?query=xxx&...
func (e *Extension) handleMetricQueryRange(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.prometheusEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Prometheus query backend not configured")
		return
	}

	targetURL := fmt.Sprintf("%s/api/v1/query_range?%s", e.obsClient.prometheusEndpoint, r.URL.RawQuery)
	e.proxyGET(w, r, targetURL)
}

// handleMetricSeries 查询 Prometheus series 元数据。
// GET /api/v2/observability/metrics/series?match[]=xxx
// → Prometheus API: GET /api/v1/series?match[]=xxx
func (e *Extension) handleMetricSeries(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.prometheusEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Prometheus query backend not configured")
		return
	}

	targetURL := fmt.Sprintf("%s/api/v1/series?%s", e.obsClient.prometheusEndpoint, r.URL.RawQuery)
	e.proxyGET(w, r, targetURL)
}

// handleMetricMetadata 查询 Prometheus metric 元数据。
// GET /api/v2/observability/metrics/metadata?metric=xxx
// → Prometheus API: GET /api/v1/metadata?metric=xxx
func (e *Extension) handleMetricMetadata(w http.ResponseWriter, r *http.Request) {
	if e.obsClient == nil || e.obsClient.prometheusEndpoint == "" {
		e.writeError(w, http.StatusServiceUnavailable, "Prometheus query backend not configured")
		return
	}

	targetURL := fmt.Sprintf("%s/api/v1/metadata?%s", e.obsClient.prometheusEndpoint, r.URL.RawQuery)
	e.proxyGET(w, r, targetURL)
}

// ============================================================================
// 通用代理方法
// ============================================================================

// proxyGET 将 GET 请求代理到目标 URL，将响应透传给客户端。
func (e *Extension) proxyGET(w http.ResponseWriter, _ *http.Request, targetURL string) {
	resp, err := e.obsClient.httpClient.Get(targetURL)
	if err != nil {
		e.logger.Warn("Observability proxy request failed",
			zap.String("url", targetURL),
			zap.Error(err),
		)
		e.writeError(w, http.StatusBadGateway, fmt.Sprintf("Backend request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	// 透传响应头
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}

	// 确保 Content-Type 为 JSON
	if ct := w.Header().Get("Content-Type"); ct == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}

	w.WriteHeader(resp.StatusCode)

	// 限制读取大小防止 OOM（最大 50MB）
	limited := io.LimitReader(resp.Body, 50*1024*1024)
	if _, err := io.Copy(w, limited); err != nil {
		e.logger.Warn("Error copying proxy response", zap.Error(err))
	}
}