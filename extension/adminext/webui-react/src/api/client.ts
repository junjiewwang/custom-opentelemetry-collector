/**
 * API 请求客户端 - 统一的 HTTP 请求层
 *
 * 从旧版 Alpine.js 前端的 api.js 移植而来，增加了 TypeScript 类型支持。
 */

import type {
  App,
  AppService,
  CreateAppRequest,
  DashboardOverview,
  Instance,
  InstanceStats,
  InstanceListParams,
  ServiceDetail,
  UpdateServiceRequest,
  TaskInfoV2,
  TaskListParams,
  CreateTaskRequest,
  AgentConfig,

  ApiError,
} from '@/types/api';

import type {
  OTelTrace,
  TraceSearchResult,
  TraceSearchParams,
  Service,
  Operation,
  DependencyLink,
} from '@/types/trace';

import type {
  MetricResult,
  MetricRangeResult,
  MetricQueryParams,
  MetricRangeQueryParams,
} from '@/types/metric';

import type {
  InstrumentationRule,
  InstrumentationRuleRuntimeSnapshot,
  InstrumentationRuleTargetStatus,
  CreateInstrumentationRuleRequest,
  UpdateInstrumentationRuleRequest,
  ListInstrumentationRulesParams,
} from '@/types/instrumentation';

import type {
  LogSearchResult,
  LogContext,
  LogField,
  LogStats,
  LogSearchParams,
  LogStatsParams,
} from '@/types/log';

import type {
  StorageStatus,
  StorageHealth,
  RetentionPolicies,
  DiskUsage,
  PurgeResult,
  SignalType,
  DailyStorageRequest,
  DailyStorageResponse,
  AppRetentionResponse,
} from '@/types/storage';

interface InstrumentationRuleMutationResponse {
  success: boolean;
  message: string;
  rule: InstrumentationRule;
}

interface InstrumentationTargetListResponse {
  targets: InstrumentationRuleTargetStatus[];
  total: number;
}

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
    // 后端返回嵌套结构 { apps: { total }, instances: { total, online, offline, unhealthy }, tasks: { pending } }
    // 前端需要扁平结构 DashboardOverview，在此做映射
    return this.request<Record<string, Record<string, number>>>('GET', '/dashboard/overview')
      .then(res => ({
        total_apps: res.apps?.total ?? 0,
        total_instances: res.instances?.total ?? 0,
        online_instances: res.instances?.online ?? 0,
        total_services: 0, // 后端 overview 未直接提供服务计数
        total_tasks: 0,
        pending_tasks: res.tasks?.pending ?? 0,
        running_tasks: res.tasks?.running ?? 0,
      }));
  }

  // ========================================================================
  // Apps
  // ========================================================================

  getApps(): Promise<App[]> {
    return this.request<{ apps: App[]; total: number }>('GET', '/apps')
      .then(res => res.apps || []);
  }

  /** 获取指定 App 下的 Service 列表（完整 ServiceInfo） */
  getAppServices(appId: string): Promise<AppService[]> {
    return this.request<{ app_id: string; services: AppService[]; total: number }>('GET', `/apps/${appId}/services`)
      .then(res => res.services || []);
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

  getInstances(status: string = '', params?: InstanceListParams): Promise<Instance[]> {
    const query = new URLSearchParams();
    if (params?.status) query.set('status', params.status);
    else if (status) query.set('status', status);
    if (params?.app_id) query.set('app_id', params.app_id);
    if (params?.service_name) query.set('service_name', params.service_name);
    if (params?.sort_by) query.set('sort_by', params.sort_by);
    if (params?.sort_order) query.set('sort_order', params.sort_order);
    return this.request<{ instances: Instance[]; total: number }>('GET', `/instances?${query.toString()}`)
      .then(res => res.instances || []);
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

  /** 获取全局 Service 列表（含 app_name enrichment） */
  getServices(): Promise<ServiceDetail[]> {
    return this.request<{ services: ServiceDetail[]; total: number }>('GET', '/services')
      .then(res => res.services || []);
  }

  /** 获取单个 Service 详情 */
  getService(appId: string, serviceName: string): Promise<ServiceDetail> {
    return this.request<ServiceDetail>('GET', `/apps/${appId}/services/${encodeURIComponent(serviceName)}`);
  }

  /** 更新 Service 元数据（description / tags） */
  updateService(appId: string, serviceName: string, data: UpdateServiceRequest): Promise<ServiceDetail> {
    return this.request<ServiceDetail>('PUT', `/apps/${appId}/services/${encodeURIComponent(serviceName)}`, data);
  }

  /** 删除 Service（要求 instance_count == 0） */
  deleteService(appId: string, serviceName: string): Promise<void> {
    return this.request<void>('DELETE', `/apps/${appId}/services/${encodeURIComponent(serviceName)}`);
  }

  // ========================================================================
  // Tasks
  // ========================================================================

  getTasks(params?: TaskListParams): Promise<{ tasks: TaskInfoV2[]; total?: number; next_cursor?: string; has_more?: boolean }> {
    const query = new URLSearchParams();
    if (params?.app_id) query.set('app_id', params.app_id);
    if (params?.service_name) query.set('service_name', params.service_name);
    if (params?.agent_id) query.set('agent_id', params.agent_id);
    if (params?.task_type) query.set('task_type', params.task_type);
    if (params?.status) query.set('status', params.status);
    if (params?.limit) query.set('limit', String(params.limit));
    if (params?.cursor) query.set('cursor', params.cursor);
    const qs = query.toString();
    return this.request<{ tasks: TaskInfoV2[]; total?: number; next_cursor?: string; has_more?: boolean }>('GET', `/tasks${qs ? '?' + qs : ''}`);
  }

  getTask(id: string): Promise<TaskInfoV2> {
    return this.request<TaskInfoV2>('GET', `/tasks/${id}`);
  }

  createTask(data: CreateTaskRequest): Promise<unknown> {
    return this.request<unknown>('POST', '/tasks', data);
  }

  cancelTask(id: string): Promise<void> {
    return this.request<void>('DELETE', `/tasks/${id}`);
  }

  // ========================================================================
  // Instrumentation
  // ========================================================================

  listInstrumentationRules(params?: ListInstrumentationRulesParams): Promise<InstrumentationRule[]> {
    const query = new URLSearchParams();
    if (params?.app_id) query.set('app_id', params.app_id);
    if (params?.service_name) query.set('service_name', params.service_name);
    if (params?.instrument_type) query.set('instrument_type', params.instrument_type);
    if (params?.desired_state) query.set('desired_state', params.desired_state);
    if (params?.search) query.set('search', params.search);
    if (params?.include_deleted) query.set('include_deleted', 'true');
    const qs = query.toString();
    return this.request<{ rules: InstrumentationRule[]; total: number }>('GET', `/instrumentation/rules${qs ? '?' + qs : ''}`)
      .then(res => res.rules || []);
  }

  getInstrumentationRule(ruleId: string): Promise<InstrumentationRule> {
    return this.request<InstrumentationRule>('GET', `/instrumentation/rules/${encodeURIComponent(ruleId)}`);
  }

  createInstrumentationRule(data: CreateInstrumentationRuleRequest): Promise<InstrumentationRule> {
    return this.request<InstrumentationRuleMutationResponse>('POST', '/instrumentation/rules', data)
      .then(res => res.rule);
  }

  updateInstrumentationRule(ruleId: string, data: UpdateInstrumentationRuleRequest): Promise<InstrumentationRule> {
    return this.request<InstrumentationRuleMutationResponse>('PUT', `/instrumentation/rules/${encodeURIComponent(ruleId)}`, data)
      .then(res => res.rule);
  }

  pauseInstrumentationRule(ruleId: string): Promise<InstrumentationRule> {
    return this.request<InstrumentationRuleMutationResponse>('POST', `/instrumentation/rules/${encodeURIComponent(ruleId)}/pause`)
      .then(res => res.rule);
  }

  resumeInstrumentationRule(ruleId: string): Promise<InstrumentationRule> {
    return this.request<InstrumentationRuleMutationResponse>('POST', `/instrumentation/rules/${encodeURIComponent(ruleId)}/resume`)
      .then(res => res.rule);
  }

  deleteInstrumentationRule(ruleId: string): Promise<InstrumentationRule> {
    return this.request<InstrumentationRuleMutationResponse>('DELETE', `/instrumentation/rules/${encodeURIComponent(ruleId)}`)
      .then(res => res.rule);
  }

  getInstrumentationTargets(ruleId: string): Promise<InstrumentationRuleTargetStatus[]> {
    return this.request<InstrumentationTargetListResponse>('GET', `/instrumentation/rules/${encodeURIComponent(ruleId)}/targets`)
      .then(res => res.targets || []);
  }

  getInstrumentationRuntimeSnapshot(ruleId: string): Promise<InstrumentationRuleRuntimeSnapshot> {
    return this.request<InstrumentationRuleRuntimeSnapshot>('GET', `/instrumentation/rules/${encodeURIComponent(ruleId)}/runtime-snapshot`);
  }

  refreshInstrumentationRuntimeSnapshot(ruleId: string): Promise<InstrumentationRuleRuntimeSnapshot> {
    return this.request<InstrumentationRuleRuntimeSnapshot>('POST', `/instrumentation/rules/${encodeURIComponent(ruleId)}/runtime-snapshot/refresh`);
  }

  // ========================================================================
  // Arthas
  // ========================================================================



  /**
   * Attach Arthas 到指定实例（创建 arthas_attach 任务）
   */
  attachArthas(agentId: string): Promise<unknown> {
    return this.createTask({
      task_type_name: 'arthas_attach',
      target_agent_id: agentId,
      parameters_json: { action: 'attach' },
      timeout_millis: 60000,
    } as CreateTaskRequest);
  }

  /**
   * Detach Arthas 从指定实例（创建 arthas_detach 任务）
   */
  detachArthas(agentId: string): Promise<unknown> {
    return this.createTask({
      task_type_name: 'arthas_detach',
      target_agent_id: agentId,
      parameters_json: { action: 'detach' },
      timeout_millis: 60000,
    } as CreateTaskRequest);
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
  // Observability - Trace Query (OTel V2)
  // ========================================================================

  /** 获取所有可用的 Service 列表 */
  getTraceServices(start?: string, end?: string): Promise<{ data: Service[] }> {
    const params = new URLSearchParams();
    if (start) params.set('start', start);
    if (end) params.set('end', end);
    const qs = params.toString();
    return this.request<{ data: Service[] }>('GET', `/observability/traces/services${qs ? `?${qs}` : ''}`);
  }

  /** 获取指定 Service 的所有 Operation */
  getTraceOperations(service: string, start?: string, end?: string): Promise<{ data: Operation[] }> {
    const params = new URLSearchParams();
    if (start) params.set('start', start);
    if (end) params.set('end', end);
    const qs = params.toString();
    return this.request<{ data: Operation[] }>(
      'GET',
      `/observability/traces/services/${encodeURIComponent(service)}/operations${qs ? `?${qs}` : ''}`,
    );
  }

  /** 搜索 Traces */
  searchTraces(params: TraceSearchParams): Promise<TraceSearchResult> {
    const query = new URLSearchParams();
    if (params.service) query.set('service', params.service);
    if (params.operation) query.set('operation', params.operation);
    if (params.tags) query.set('tags', params.tags);
    if (params.limit) query.set('limit', String(params.limit));
    if (params.start) query.set('start', String(params.start));
    if (params.end) query.set('end', String(params.end));
    if (params.minDuration) query.set('minDuration', params.minDuration);
    if (params.maxDuration) query.set('maxDuration', params.maxDuration);
    if (params.lookback) query.set('lookback', params.lookback);
    return this.request<TraceSearchResult>('GET', `/observability/traces?${query.toString()}`);
  }

  /** 获取单个 Trace 的详细信息 */
  getTrace(traceID: string): Promise<OTelTrace> {
    return this.request<OTelTrace>(
      'GET',
      `/observability/traces/${encodeURIComponent(traceID)}`,
    );
  }

  /** 获取服务间依赖关系（用于 Service Map） */
  getDependencies(endTs: number, lookback: number): Promise<{ data: DependencyLink[] }> {
    const params = new URLSearchParams();
    params.set('endTs', String(endTs));
    params.set('lookback', String(lookback));
    return this.request<{ data: DependencyLink[] }>(
      'GET',
      `/observability/dependencies?${params.toString()}`,
    );
  }

  // ========================================================================
  // Observability - Metric Query (OTel V2)
  // ========================================================================

  /** Metric instant query */
  metricQuery(params: MetricQueryParams): Promise<MetricResult> {
    const query = new URLSearchParams();
    query.set('metric', params.metric);
    if (params.service) query.set('service', params.service);
    if (params.labels) query.set('labels', params.labels);
    if (params.time) query.set('time', String(params.time));
    return this.request<MetricResult>('GET', `/observability/metrics/query?${query.toString()}`);
  }

  /** Metric range query */
  metricQueryRange(params: MetricRangeQueryParams): Promise<MetricRangeResult> {
    const query = new URLSearchParams();
    query.set('metric', params.metric);
    if (params.service) query.set('service', params.service);
    if (params.labels) query.set('labels', params.labels);
    query.set('start', String(params.start));
    query.set('end', String(params.end));
    query.set('step', params.step);
    return this.request<MetricRangeResult>('GET', `/observability/metrics/query_range?${query.toString()}`);
  }

  /** 获取所有 metric 名称 */
  getMetricNames(): Promise<{ data: string[] }> {
    return this.request<{ data: string[] }>('GET', '/observability/metrics/names');
  }

  /** 获取所有 label 名称 */
  getMetricLabels(): Promise<{ data: string[] }> {
    return this.request<{ data: string[] }>('GET', '/observability/metrics/labels');
  }

  /** 获取指定 label 的值 */
  getMetricLabelValues(labelName: string): Promise<{ data: string[] }> {
    return this.request<{ data: string[] }>(
      'GET',
      `/observability/metrics/labels/${encodeURIComponent(labelName)}/values`,
    );
  }

  // ========================================================================
  // Observability - Log Query (V2 — Structured)
  // ========================================================================

  /** 搜索日志 */
  searchLogs(params: LogSearchParams): Promise<LogSearchResult> {
    const query = new URLSearchParams();
    if (params.query) query.set('query', params.query);
    if (params.service) query.set('service', params.service);
    if (params.severity) query.set('severity', params.severity);
    if (params.traceId) query.set('traceId', params.traceId);
    if (params.spanId) query.set('spanId', params.spanId);
    if (params.attributes) query.set('attributes', params.attributes);
    if (params.start) query.set('start', String(params.start));
    if (params.end) query.set('end', String(params.end));
    if (params.limit) query.set('limit', String(params.limit));
    if (params.offset) query.set('offset', String(params.offset));
    return this.request<LogSearchResult>('GET', `/observability/logs?${query.toString()}`);
  }

  /** 获取日志上下文 */
  getLogContext(logID: string, lines: number = 10): Promise<LogContext> {
    return this.request<LogContext>(
      'GET',
      `/observability/logs/${encodeURIComponent(logID)}/context?lines=${lines}`,
    );
  }

  /** 获取可用日志字段 */
  getLogFields(start?: number, end?: number): Promise<LogField[]> {
    const query = new URLSearchParams();
    if (start) query.set('start', String(start));
    if (end) query.set('end', String(end));
    const qs = query.toString();
    return this.request<{ data: LogField[] }>('GET', `/observability/logs/fields${qs ? '?' + qs : ''}`)
      .then(res => res.data || []);
  }

  /** 获取日志统计 */
  getLogStats(params: LogStatsParams): Promise<LogStats> {
    const query = new URLSearchParams();
    if (params.service) query.set('service', params.service);
    if (params.start) query.set('start', String(params.start));
    if (params.end) query.set('end', String(params.end));
    if (params.groupBy) query.set('groupBy', params.groupBy);
    return this.request<LogStats>('GET', `/observability/logs/stats?${query.toString()}`);
  }

  // ========================================================================
  // Observability - Storage Admin (V2)
  // ========================================================================

  /** 获取存储状态 */
  getStorageStatus(): Promise<StorageStatus> {
    return this.request<StorageStatus>('GET', '/observability/admin/status');
  }

  /** 获取存储健康状态 */
  getStorageHealth(): Promise<StorageHealth> {
    return this.request<StorageHealth>('GET', '/observability/admin/health');
  }

  /** 获取保留策略 */
  getStorageRetention(): Promise<RetentionPolicies> {
    return this.request<RetentionPolicies>('GET', '/observability/admin/retention');
  }

  /** 设置保留策略 */
  setStorageRetention(signal: SignalType, duration: string): Promise<void> {
    return this.request<void>('PUT', `/observability/admin/retention/${signal}`, { duration });
  }

  /** 触发数据清除 */
  purgeStorage(signal: SignalType, before: string): Promise<PurgeResult> {
    return this.request<PurgeResult>(
      'POST',
      `/observability/admin/purge/${signal}?before=${encodeURIComponent(before)}`,
    );
  }

  /** 获取磁盘使用情况 */
  getStorageDiskUsage(): Promise<DiskUsage> {
    return this.request<DiskUsage>('GET', '/observability/admin/disk-usage');
  }

  /** 获取按天存储量（直接查 ES 索引统计） */
  getStorageDailyUsage(params: DailyStorageRequest = {}): Promise<DailyStorageResponse> {
    const query = new URLSearchParams();
    if (params.start) query.set('start', params.start);
    if (params.end) query.set('end', params.end);
    if (params.appId) query.set('appId', params.appId);
    const qs = query.toString();
    return this.request<DailyStorageResponse>(
      'GET',
      `/observability/admin/disk-usage/daily${qs ? `?${qs}` : ''}`,
    );
  }

  // ========================================================================
  // App Retention (per-app data lifecycle policy)
  // ========================================================================

  /** 获取 App 的 retention 配置 */
  getAppRetention(appId: string): Promise<AppRetentionResponse> {
    return this.request<AppRetentionResponse>('GET', `/apps/${encodeURIComponent(appId)}/retention`);
  }

  /** 设置 App 某 signal 的 retention */
  setAppRetention(appId: string, signal: string, duration: string): Promise<{ message: string; success: boolean }> {
    return this.request('PUT', `/apps/${encodeURIComponent(appId)}/retention/${signal}`, { duration });
  }

  /** 删除 App 某 signal 的 retention override */
  deleteAppRetention(appId: string, signal: string): Promise<{ message: string; success: boolean }> {
    return this.request('DELETE', `/apps/${encodeURIComponent(appId)}/retention/${signal}`);
  }
}

// 单例导出
export const apiClient = new ApiClient();
