/**
 * API 响应类型定义
 */

// ============================================================================
// 通用
// ============================================================================

/** API 错误响应 */
export interface ApiError {
  status: number;
  message: string;
}

// ============================================================================
// Dashboard
// ============================================================================

export interface DashboardOverview {
  total_apps: number;
  total_instances: number;
  online_instances: number;
  total_services: number;
  total_tasks: number;
  pending_tasks: number;
  running_tasks: number;
}

// ============================================================================
// App (Application)
// ============================================================================

export interface App {
  id: string;
  name: string;
  description: string;
  token: string;
  created_at: string;
  updated_at: string;
}

export interface CreateAppRequest {
  name: string;
  description?: string;
}

// ============================================================================
// Instance (Agent)
// ============================================================================

/** Agent 状态（来自后端 AgentStatus） */
export interface AgentStatus {
  state: 'online' | 'offline' | 'unhealthy';
  state_changed_at?: number;
  health?: Record<string, unknown>;
  current_task?: string;
  config_version?: string;
  metrics?: Record<string, unknown>;
}

/** 后端返回的 AgentInfo 结构 */
export interface Instance {
  agent_id: string;
  app_id: string;
  app_name: string;
  service_name: string;
  hostname: string;
  ip: string;
  version: string;
  pid: number;
  start_time: number;           // Unix 毫秒时间戳
  labels: Record<string, string>;
  status: AgentStatus | null;
  registered_at: number;         // Unix 毫秒时间戳
  last_heartbeat: number;        // Unix 毫秒时间戳
}

/** 前端合并后的实例（包含 Arthas 状态） */
export interface EnrichedInstance extends Instance {
  arthasStatus: {
    state: 'running' | 'stopped';
    arthasVersion: string;
    tunnelReady: boolean;
    tunnelAgentId: string;
  };
}

export interface InstanceStats {
  total: number;
  online: number;
  offline: number;
  by_app: Record<string, number>;
  by_service: Record<string, number>;
}

// ============================================================================
// Service
// ============================================================================

export interface Service {
  app_name: string;
  service_name: string;
  instance_count: number;
  online_count: number;
}

// ============================================================================
// Task
// ============================================================================

export type TaskStatus = 'unknown' | 'pending' | 'running' | 'success' | 'failed' | 'timeout' | 'cancelled';

/** 后端 /api/v2/tasks 返回的 TaskInfoV2 结构 */
export interface TaskInfoV2 {
  task_id: string;
  task?: {
    task_id?: string;
    task_type_name?: string;
    task_type?: string;
    target_agent_id?: string;
    parameters_json?: Record<string, unknown> | string;
    timeout_millis?: number;
    priority_num?: number;
    priority?: number;
    created_at_millis?: number;
  };
  status: number;  // 0=unknown, 1=pending, 2=running, 3=success, 4=failed, 5=timeout, 6=cancelled, 7=failed
  result?: TaskResultRaw | null;
  agent_id?: string;
  app_id?: string;
  app_name?: string;
  service_name?: string;
  agent_state?: string;
  task_type_name?: string;
  task_type?: string;
  target_agent_id?: string;
  created_at_millis?: number;
}

/** 后端 TaskResult 原始结构 */
export interface TaskResultRaw {
  status?: number;
  error_code?: string;
  error_message?: string;
  started_at_millis?: number;
  completed_at_millis?: number;
  execution_time_millis?: number;
  result_json?: unknown;
  result_data?: string;  // base64
  result_data_type?: string;
  compression?: string;
  original_size?: number;
  compressed_size?: number;
  artifact_ref?: string;
  artifact_size?: number;
}

/** 前端标准化后的 Task（在 loadTasks 中加工） */
export interface Task {
  task_id: string;
  task_type: string;
  target_agent_id: string;
  app_id: string;
  app_name: string;
  service_name: string;
  agent_state: string;
  status: TaskStatus;
  created_at_millis: number;
  priority: number;
  timeout_millis: number;
  parameters: Record<string, unknown>;
  _raw: TaskInfoV2;
  _result: NormalizedTaskResult | null;
  _detailLoading: boolean;
  _detailError: string;
}

/** 标准化后的任务结果 */
export interface NormalizedTaskResult {
  status?: number;
  error_code: string;
  error_message: string;
  started_at_millis: number;
  completed_at_millis: number;
  execution_time_millis: number;
  has_execution_info: boolean;
  result_json_obj: unknown;
  result_json_pretty: string;
  result_summary: { key: string; valueText: string }[];
  result_data_base64: string;
  result_data_text: string;
  result_data_type: string;
  artifact_ref: string;
  artifact_size: number;
  // 性能分析字段（async-profiler）
  analysis_view_url: string;
  analysis_status: string;
  analysis_error: string;
  analysis_mode: string;
  analysis_summary: Record<string, unknown> | null;
  _raw: TaskResultRaw;
}

export interface CreateTaskRequest {
  task_type_name: string;
  target_agent_id?: string;
  timeout_millis?: number;
  priority_num?: number;
  parameters_json?: Record<string, unknown>;
}

// ============================================================================
// Config
// ============================================================================

export interface AgentConfig {
  [key: string]: unknown;
}

// ============================================================================
// Arthas
// ============================================================================

export interface ArthasAgent {
  agent_id: string;
  connected_at: string;
}

// ============================================================================
// 菜单项
// ============================================================================

export interface MenuItem {
  id: string;
  label: string;
  icon: string;
  path: string;
  badge?: string | number;
}
