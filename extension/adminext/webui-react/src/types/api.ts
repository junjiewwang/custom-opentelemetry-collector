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

export interface Instance {
  agent_id: string;
  app_name: string;
  service_name: string;
  hostname: string;
  ip: string;
  pid: number;
  status: 'online' | 'offline';
  last_heartbeat: string;
  registered_at: string;
  metadata: Record<string, string>;
  arthas_tunnel_connected?: boolean;
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

export type TaskStatus = 'PENDING' | 'RUNNING' | 'SUCCESS' | 'FAILED' | 'TIMEOUT' | 'CANCELLED';

export interface Task {
  id: string;
  type: string;
  target_agent_id: string;
  app_name: string;
  service_name: string;
  status: TaskStatus;
  params: Record<string, unknown>;
  result: Record<string, unknown> | null;
  error_message: string;
  created_at: string;
  updated_at: string;
  completed_at: string | null;
}

export interface CreateTaskRequest {
  type: string;
  target_agent_ids: string[];
  params?: Record<string, unknown>;
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
