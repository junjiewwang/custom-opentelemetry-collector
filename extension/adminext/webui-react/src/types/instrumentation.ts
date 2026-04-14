export type InstrumentationScopeType = 'service' | 'instance';
export type InstrumentationInstrumentType = 'trace' | 'metric' | 'log';
export type InstrumentationRuleDesiredState = 'active' | 'paused' | 'deleted';
export type InstrumentationOperationStatus = 'pending' | 'running' | 'success' | 'failed' | 'partial_success';
export type InstrumentationTargetState = 'pending' | 'dispatched' | 'running' | 'applied' | 'removed' | 'failed' | 'offline';
export type InstrumentationAuditSource = 'manual' | 'reconcile';
export type InstrumentationAuditAction = 'apply' | 'remove' | 'target_discovered' | 'target_pruned';
export type InstrumentationAuditStatus = 'success' | 'failed' | 'skipped';

export interface InstrumentationRuleSummary {
  status: InstrumentationOperationStatus;
  total_targets: number;
  applied_targets: number;
  running_targets: number;
  pending_targets: number;
  failed_targets: number;
  offline_targets: number;
}

export interface InstrumentationOperationSummary {
  operation_id: string;
  type: 'apply' | 'remove';
  status: InstrumentationOperationStatus;
  started_at_millis: number;
  completed_at_millis?: number;
  total_targets: number;
  applied_targets: number;
  running_targets: number;
  pending_targets: number;
  failed_targets: number;
  offline_targets: number;
}

export interface InstrumentationRuleAuditEntry {
  audit_id: string;
  source: InstrumentationAuditSource;
  action: InstrumentationAuditAction;
  status: InstrumentationAuditStatus;
  agent_id?: string;
  task_id?: string;
  message?: string;
  created_at_millis: number;
}

export interface InstrumentationRule {
  rule_id: string;
  name: string;
  description?: string;
  app_id: string;
  service_name: string;
  scope_type: InstrumentationScopeType;
  target_agent_ids?: string[];
  class_name: string;
  method_name: string;
  parameter_types?: string;
  method_descriptor?: string;
  instrument_type: InstrumentationInstrumentType;
  span_name?: string;
  capture_args?: string;
  capture_return?: string;
  capture_max_length?: number;
  force?: boolean;
  desired_state: InstrumentationRuleDesiredState;
  created_at_millis: number;
  updated_at_millis: number;
  created_by?: string;
  updated_by?: string;
  last_operation?: InstrumentationOperationSummary;
  summary: InstrumentationRuleSummary;
  recent_audits?: InstrumentationRuleAuditEntry[];
}

export interface InstrumentationRuleTargetStatus {
  rule_id: string;
  agent_id: string;
  hostname?: string;
  ip?: string;
  desired_state: InstrumentationRuleDesiredState;
  state: InstrumentationTargetState;
  task_id?: string;
  task_type?: string;
  task_status?: string;
  last_error_message?: string;
  last_dispatch_at_millis?: number;
  updated_at_millis: number;
}

export type InstrumentationRuntimeRefreshStatus = 'idle' | 'success' | 'failed' | 'timeout' | 'skipped';
export type InstrumentationRuntimeDriftReason =
  | 'missing'
  | 'ineffective'
  | 'deleted_residual'
  | 'paused_residual'
  | 'instrumentation_unavailable'
  | 'enhancement_unavailable';

export interface InstrumentationRuleRuntimeSnapshotSummary {
  total_targets: number;
  snapshot_available_targets: number;
  runtime_found_targets: number;
  effective_targets: number;
  drifted_targets: number;
  missing_targets: number;
  stale_targets: number;
  refresh_failed_targets: number;
  instrumentation_unavailable_targets: number;
  enhancement_unavailable_targets: number;
}

export interface InstrumentationRuleRuntimeSnapshotTarget {
  rule_id: string;
  agent_id: string;
  hostname?: string;
  ip?: string;
  desired_state: InstrumentationRuleDesiredState;
  controlplane_state: InstrumentationTargetState;
  controlplane_task_status?: string;
  snapshot_available: boolean;
  runtime_found: boolean;
  runtime_status?: string;
  is_applied: boolean;
  is_effective: boolean;
  instrumentation_available: boolean;
  enhancement_capability: boolean;
  active_transformer_count?: number;
  diagnostic_message?: string;
  instrumentation_source?: string;
  refreshed_at_millis?: number;
  expires_at_millis?: number;
  is_stale: boolean;
  dirty: boolean;
  last_refresh_status?: InstrumentationRuntimeRefreshStatus;
  last_error_message?: string;
  drift_reasons?: InstrumentationRuntimeDriftReason[];
}

export interface InstrumentationRuleRuntimeSnapshot {
  rule_id: string;
  desired_state: InstrumentationRuleDesiredState;
  generated_at_millis: number;
  summary: InstrumentationRuleRuntimeSnapshotSummary;
  targets: InstrumentationRuleRuntimeSnapshotTarget[];
}

export interface ListInstrumentationRulesParams {
  app_id?: string;
  service_name?: string;
  instrument_type?: InstrumentationInstrumentType;
  desired_state?: InstrumentationRuleDesiredState;
  search?: string;
  include_deleted?: boolean;
}

export interface CreateInstrumentationRuleRequest {
  name: string;
  description?: string;
  app_id: string;
  service_name: string;
  scope_type?: InstrumentationScopeType;
  target_agent_ids?: string[];
  class_name: string;
  method_name: string;
  parameter_types?: string;
  method_descriptor?: string;
  instrument_type: InstrumentationInstrumentType;
  span_name?: string;
  capture_args?: string;
  capture_return?: string;
  capture_max_length?: number;
  force?: boolean;
  created_by?: string;
}

export interface UpdateInstrumentationRuleRequest {
  name: string;
  description?: string;
  scope_type?: InstrumentationScopeType;
  target_agent_ids?: string[];
  class_name: string;
  method_name: string;
  parameter_types?: string;
  method_descriptor?: string;
  instrument_type: InstrumentationInstrumentType;
  span_name?: string;
  capture_args?: string;
  capture_return?: string;
  capture_max_length?: number;
  force?: boolean;
  updated_by?: string;
}
