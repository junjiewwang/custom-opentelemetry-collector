/**
 * API 请求客户端 - 统一的 HTTP 请求层
 *
 * 从旧版 Alpine.js 前端的 api.js 移植而来，增加了 TypeScript 类型支持。
 */

import type {
  App,
  CreateAppRequest,
  DashboardOverview,
  Instance,
  InstanceStats,
  Service,
  Task,
  CreateTaskRequest,
  AgentConfig,
  ArthasAgent,
  ApiError,
} from '@/types/api';

import type {
  JaegerResponse,
  JaegerTrace,
  JaegerOperation,
  JaegerDependencyLink,
  TraceSearchParams,
} from '@/types/trace';

import type {
  PrometheusResponse,
  PrometheusQueryResult,
  PrometheusLabelsResponse,
  PrometheusLabelValuesResponse,
  PrometheusMetadataResponse,
} from '@/types/metric';

class ApiClient {
  private apiKey: string = '';

  setApiKey(key: string): void {
    this.apiKey = key;
  }

  getApiKey(): string {
    return this.apiKey;
  }

  /**
   * 通用请求方法
   */
  async request<T>(method: string, path: string, data?: unknown): Promise<T> {
    const options: RequestInit = {
      method,
      headers: {
        'Content-Type': 'application/json',
        'X-API-Key': this.apiKey,
      },
    };

    if (data) {
      options.body = JSON.stringify(data);
    }

    const res = await fetch(`/api/v2${path}`, options);

    if (res.status === 401) {
      const err: ApiError = { status: 401, message: 'Unauthorized' };
      throw err;
    }

    if (!res.ok) {
      const errBody = await res.json().catch(() => ({}));
      const err: ApiError = {
        status: res.status,
        message: (errBody as Record<string, string>).error || 'Request failed',
      };
      throw err;
    }

    return res.json() as Promise<T>;
  }

  // ========================================================================
  // Dashboard
  // ========================================================================

  getDashboard(): Promise<DashboardOverview> {
    return this.request<DashboardOverview>('GET', '/dashboard/overview');
  }

  // ========================================================================
  // Apps
  // ========================================================================

  getApps(): Promise<App[]> {
    return this.request<App[]>('GET', '/apps');
  }

  createApp(data: CreateAppRequest): Promise<App> {
    return this.request<App>('POST', '/apps', data);
  }

  deleteApp(id: string): Promise<void> {
    return this.request<void>('DELETE', `/apps/${id}`);
  }

  regenerateToken(id: string): Promise<App> {
    return this.request<App>('POST', `/apps/${id}/token`);
  }

  setToken(id: string, token: string): Promise<App> {
    return this.request<App>('PUT', `/apps/${id}/token`, { token });
  }

  // ========================================================================
  // Instances
  // ========================================================================

  getInstances(status: string = ''): Promise<Instance[]> {
    return this.request<Instance[]>('GET', `/instances?status=${status}`);
  }

  getInstanceStats(): Promise<InstanceStats> {
    return this.request<InstanceStats>('GET', '/instances/stats');
  }

  unregisterAgent(id: string): Promise<void> {
    return this.request<void>('POST', `/instances/${id}/kick`);
  }

  // ========================================================================
  // Services
  // ========================================================================

  getServices(): Promise<Service[]> {
    return this.request<Service[]>('GET', '/services');
  }

  // ========================================================================
  // Tasks
  // ========================================================================

  getTasks(): Promise<Task[]> {
    return this.request<Task[]>('GET', '/tasks');
  }

  getTask(id: string): Promise<Task> {
    return this.request<Task>('GET', `/tasks/${id}`);
  }

  createTask(data: CreateTaskRequest): Promise<Task> {
    return this.request<Task>('POST', '/tasks', data);
  }

  cancelTask(id: string): Promise<void> {
    return this.request<void>('DELETE', `/tasks/${id}`);
  }

  // ========================================================================
  // Arthas
  // ========================================================================

  getArthasAgents(): Promise<ArthasAgent[]> {
    return this.request<ArthasAgent[]>('GET', '/arthas/agents');
  }

  // ========================================================================
  // Config
  // ========================================================================

  getAppServiceConfig(appId: string, serviceName: string): Promise<AgentConfig> {
    return this.request<AgentConfig>('GET', `/apps/${appId}/config/services/${serviceName}`);
  }

  setAppServiceConfig(appId: string, serviceName: string, config: AgentConfig): Promise<void> {
    return this.request<void>('PUT', `/apps/${appId}/config/services/${serviceName}`, config);
  }

  deleteAppServiceConfig(appId: string, serviceName: string): Promise<void> {
    return this.request<void>('DELETE', `/apps/${appId}/config/services/${serviceName}`);
  }

  // ========================================================================
  // Auth - WebSocket Token
  // ========================================================================

  generateWSToken(): Promise<{ token: string }> {
    return this.request<{ token: string }>('POST', '/auth/ws-token');
  }

  // ========================================================================
  // Observability - Trace Query (Jaeger)
  // ========================================================================

  /** 获取所有可用的 Service 列表 */
  getTraceServices(): Promise<JaegerResponse<string[]>> {
    return this.request<JaegerResponse<string[]>>('GET', '/observability/traces/services');
  }

  /** 获取指定 Service 的所有 Operation */
  getTraceOperations(service: string): Promise<JaegerResponse<JaegerOperation[]>> {
    return this.request<JaegerResponse<JaegerOperation[]>>(
      'GET',
      `/observability/traces/services/${encodeURIComponent(service)}/operations`,
    );
  }

  /** 搜索 Traces */
  searchTraces(params: TraceSearchParams): Promise<JaegerResponse<JaegerTrace[]>> {
    const query = new URLSearchParams();
    query.set('service', params.service);
    if (params.operation) query.set('operation', params.operation);
    if (params.tags) query.set('tags', params.tags);
    if (params.limit) query.set('limit', String(params.limit));
    if (params.start) query.set('start', String(params.start));
    if (params.end) query.set('end', String(params.end));
    if (params.minDuration) query.set('minDuration', params.minDuration);
    if (params.maxDuration) query.set('maxDuration', params.maxDuration);
    if (params.lookback) query.set('lookback', params.lookback);
    return this.request<JaegerResponse<JaegerTrace[]>>('GET', `/observability/traces?${query.toString()}`);
  }

  /** 获取单个 Trace 的详细信息 */
  getTrace(traceID: string): Promise<JaegerResponse<JaegerTrace[]>> {
    return this.request<JaegerResponse<JaegerTrace[]>>(
      'GET',
      `/observability/traces/${encodeURIComponent(traceID)}`,
    );
  }

  /** 获取服务间依赖关系（用于 Service Map） */
  getDependencies(endTs: number, lookback: number): Promise<JaegerResponse<JaegerDependencyLink[]>> {
    const params = new URLSearchParams();
    params.set('endTs', String(endTs));
    params.set('lookback', String(lookback));
    return this.request<JaegerResponse<JaegerDependencyLink[]>>(
      'GET',
      `/observability/dependencies?${params.toString()}`,
    );
  }

  // ========================================================================
  // Observability - Metric Query (Prometheus)
  // ========================================================================

  /** Prometheus instant query */
  metricQuery(query: string, time?: number): Promise<PrometheusResponse<PrometheusQueryResult>> {
    const params = new URLSearchParams();
    params.set('query', query);
    if (time) params.set('time', String(time));
    return this.request<PrometheusResponse<PrometheusQueryResult>>('GET', `/observability/metrics/query?${params.toString()}`);
  }

  /** Prometheus range query */
  metricQueryRange(query: string, start: number, end: number, step: string): Promise<PrometheusResponse<PrometheusQueryResult>> {
    const params = new URLSearchParams();
    params.set('query', query);
    params.set('start', String(start));
    params.set('end', String(end));
    params.set('step', step);
    return this.request<PrometheusResponse<PrometheusQueryResult>>('GET', `/observability/metrics/query_range?${params.toString()}`);
  }

  /** 获取所有 label 名称 */
  getMetricLabels(): Promise<PrometheusLabelsResponse> {
    return this.request<PrometheusLabelsResponse>('GET', '/observability/metrics/labels');
  }

  /** 获取指定 label 的值 */
  getMetricLabelValues(labelName: string): Promise<PrometheusLabelValuesResponse> {
    return this.request<PrometheusLabelValuesResponse>(
      'GET',
      `/observability/metrics/labels/${encodeURIComponent(labelName)}/values`,
    );
  }

  /** 查询 metric 元数据 */
  getMetricMetadata(metric?: string): Promise<PrometheusMetadataResponse> {
    const params = metric ? `?metric=${encodeURIComponent(metric)}` : '';
    return this.request<PrometheusMetadataResponse>('GET', `/observability/metrics/metadata${params}`);
  }
}

// 单例导出
export const apiClient = new ApiClient();
