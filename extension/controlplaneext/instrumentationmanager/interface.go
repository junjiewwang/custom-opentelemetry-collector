// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import "context"

type ScopeType string

const (
	ScopeTypeService  ScopeType = "service"
	ScopeTypeInstance ScopeType = "instance"
)

type InstrumentType string

const (
	InstrumentTypeTrace  InstrumentType = "trace"
	InstrumentTypeMetric InstrumentType = "metric"
	InstrumentTypeLog    InstrumentType = "log"
)

type RuleDesiredState string

const (
	RuleDesiredStateActive  RuleDesiredState = "active"
	RuleDesiredStatePaused  RuleDesiredState = "paused"
	RuleDesiredStateDeleted RuleDesiredState = "deleted"
)

type OperationType string

const (
	OperationTypeApply  OperationType = "apply"
	OperationTypeRemove OperationType = "remove"
)

type OperationStatus string

const (
	OperationStatusPending        OperationStatus = "pending"
	OperationStatusRunning        OperationStatus = "running"
	OperationStatusSuccess        OperationStatus = "success"
	OperationStatusFailed         OperationStatus = "failed"
	OperationStatusPartialSuccess OperationStatus = "partial_success"
)

type TargetState string

const (
	TargetStatePending    TargetState = "pending"
	TargetStateDispatched TargetState = "dispatched"
	TargetStateRunning    TargetState = "running"
	TargetStateApplied    TargetState = "applied"
	TargetStateRemoved    TargetState = "removed"
	TargetStateFailed     TargetState = "failed"
	TargetStateOffline    TargetState = "offline"
	TargetStateExpired    TargetState = "expired"
)

// InstrumentationManager defines the public interface for dynamic instrumentation rule management.
type InstrumentationManager interface {
	CreateRule(ctx context.Context, req *CreateRuleRequest) (*Rule, error)
	GetRule(ctx context.Context, ruleID string) (*Rule, error)
	ListRules(ctx context.Context, query ListRulesQuery) ([]*Rule, error)
	UpdateRule(ctx context.Context, ruleID string, req *UpdateRuleRequest) (*Rule, error)
	PauseRule(ctx context.Context, ruleID string) (*Rule, error)
	ResumeRule(ctx context.Context, ruleID string) (*Rule, error)
	DeleteRule(ctx context.Context, ruleID string) (*Rule, error)
	ListTargetStatuses(ctx context.Context, ruleID string) ([]*RuleTargetStatus, error)
	GetRuleRuntimeSnapshot(ctx context.Context, ruleID string) (*RuleRuntimeSnapshot, error)
	RefreshRuleRuntimeSnapshot(ctx context.Context, ruleID string) (*RuleRuntimeSnapshot, error)
	VerifyUninstall(ctx context.Context, ruleID string) (*Rule, error)
	PurgeRule(ctx context.Context, ruleID string, reason string) error
	Start(ctx context.Context) error
	Close() error
}

type Config struct {
	Type                              string `mapstructure:"type"`
	RedisName                         string `mapstructure:"redis_name"`
	KeyPrefix                         string `mapstructure:"key_prefix"`
	RuntimeSnapshotTTL                int64  `mapstructure:"runtime_snapshot_ttl_millis"`
	RuntimeSnapshotQueryTimeout       int64  `mapstructure:"runtime_snapshot_query_timeout_millis"`
	RuntimeSnapshotPollInterval       int64  `mapstructure:"runtime_snapshot_poll_interval_millis"`
	RuntimeSnapshotSharedSyncInterval int64  `mapstructure:"runtime_snapshot_shared_sync_interval_millis"`
	RuntimeSnapshotLeaseTTL           int64  `mapstructure:"runtime_snapshot_lease_ttl_millis"`
	ReconcileInterval                 int64  `mapstructure:"reconcile_interval_millis"`
	ReconcileRetryInterval            int64  `mapstructure:"reconcile_retry_interval_millis"`
	AuditRetention                    int    `mapstructure:"audit_retention"`
	GCInterval                        int64  `mapstructure:"gc_interval_millis"`
	DeletedRuleRetention              int64  `mapstructure:"deleted_rule_retention_millis"`
	ReconcileTargetExpireTimeout      int64  `mapstructure:"reconcile_target_expire_timeout_millis"`
	GCUninstallEvidenceRequired       bool   `mapstructure:"gc_uninstall_evidence_required"`
	GCEvidenceScanInterval            int64  `mapstructure:"gc_evidence_scan_interval_millis"`
	GCEvidenceMaxRulesPerScan         int    `mapstructure:"gc_evidence_max_rules_per_scan"`
	GCEvidenceBatchLeaseTTL           int64  `mapstructure:"gc_evidence_batch_lease_ttl_millis"`
	GCUnconfirmedAlertAfter           int64  `mapstructure:"gc_unconfirmed_alert_after_millis"`

	// DefaultTenantInstrumentationEnabled is the deployment-wide fallback used
	// when a tenant has no explicit dynamic-instrumentation capability.
	// nil means true (keep legacy behavior); false opts the whole deployment
	// out unless a tenant explicitly enables it.
	DefaultTenantInstrumentationEnabled *bool `mapstructure:"default_tenant_instrumentation_enabled"`
}

func DefaultConfig() Config {
	return Config{
		Type:                              "memory",
		RedisName:                         "default",
		KeyPrefix:                         "otel:instrumentation",
		RuntimeSnapshotTTL:                20000,
		RuntimeSnapshotQueryTimeout:       5000,
		RuntimeSnapshotPollInterval:       200,
		RuntimeSnapshotSharedSyncInterval: 1000,
		RuntimeSnapshotLeaseTTL:           2000,
		ReconcileInterval:                 5000,
		ReconcileRetryInterval:            15000,
		AuditRetention:                    20,
		GCInterval:                        60000,
		DeletedRuleRetention:              7 * 24 * 3600 * 1000,
		ReconcileTargetExpireTimeout:      7 * 24 * 3600 * 1000,
		GCUninstallEvidenceRequired:       true,
		GCEvidenceScanInterval:            300000,
		GCEvidenceMaxRulesPerScan:         50,
		GCEvidenceBatchLeaseTTL:           120000,
		GCUnconfirmedAlertAfter:           7 * 24 * 3600 * 1000,
	}
}

type Rule struct {
	ID                 string           `json:"rule_id"`
	Name               string           `json:"name"`
	Description        string           `json:"description,omitempty"`
	AppID              string           `json:"app_id"`
	ServiceName        string           `json:"service_name"`
	ScopeType          ScopeType        `json:"scope_type"`
	TargetAgentIDs     []string         `json:"target_agent_ids,omitempty"`
	ClassName          string           `json:"class_name"`
	MethodName         string           `json:"method_name"`
	ParameterTypes     string           `json:"parameter_types,omitempty"`
	MethodDescriptor   string           `json:"method_descriptor,omitempty"`
	InstrumentType     InstrumentType   `json:"instrument_type"`
	SpanName           string           `json:"span_name,omitempty"`
	CaptureArgs        string           `json:"capture_args,omitempty"`
	CaptureReturn      string           `json:"capture_return,omitempty"`
	CaptureMaxLength   int              `json:"capture_max_length,omitempty"`
	Force              bool             `json:"force,omitempty"`
	DesiredState       RuleDesiredState `json:"desired_state"`
	EverApplySucceeded bool             `json:"ever_apply_succeeded"`
	// Revision is a monotonic CAS token. Legacy stored rules have Revision 0.
	Revision int64 `json:"revision,omitempty"`
	// Rule-level GC evidence. Deleted rules with no targets carry it directly
	// because there is no target status to attach evidence to.
	UninstallConfirmed         bool              `json:"uninstall_confirmed,omitempty"`
	UninstallEvidenceSource    string            `json:"uninstall_evidence_source,omitempty"`
	UninstallConfirmedAtMillis int64             `json:"uninstall_confirmed_at_millis,omitempty"`
	CreatedAtMillis            int64             `json:"created_at_millis"`
	UpdatedAtMillis            int64             `json:"updated_at_millis"`
	CreatedBy                  string            `json:"created_by,omitempty"`
	UpdatedBy                  string            `json:"updated_by,omitempty"`
	LastOperation              *OperationSummary `json:"last_operation,omitempty"`
	Summary                    RuleSummary       `json:"summary"`
	RecentAudits               []*RuleAuditEntry `json:"recent_audits,omitempty"`
}

type RuleSummary struct {
	Status         OperationStatus `json:"status"`
	TotalTargets   int             `json:"total_targets"`
	AppliedTargets int             `json:"applied_targets"`
	RunningTargets int             `json:"running_targets"`
	PendingTargets int             `json:"pending_targets"`
	FailedTargets  int             `json:"failed_targets"`
	OfflineTargets int             `json:"offline_targets"`
	ExpiredTargets int             `json:"expired_targets"`
}

type OperationSummary struct {
	OperationID       string          `json:"operation_id"`
	Type              OperationType   `json:"type"`
	Status            OperationStatus `json:"status"`
	StartedAtMillis   int64           `json:"started_at_millis"`
	CompletedAtMillis int64           `json:"completed_at_millis,omitempty"`
	TotalTargets      int             `json:"total_targets"`
	AppliedTargets    int             `json:"applied_targets"`
	RunningTargets    int             `json:"running_targets"`
	PendingTargets    int             `json:"pending_targets"`
	FailedTargets     int             `json:"failed_targets"`
	OfflineTargets    int             `json:"offline_targets"`
	ExpiredTargets    int             `json:"expired_targets"`
}

type RuleTargetStatus struct {
	RuleID               string           `json:"rule_id"`
	AgentID              string           `json:"agent_id"`
	Hostname             string           `json:"hostname,omitempty"`
	IP                   string           `json:"ip,omitempty"`
	DesiredState         RuleDesiredState `json:"desired_state"`
	State                TargetState      `json:"state"`
	TaskID               string           `json:"task_id,omitempty"`
	TaskType             string           `json:"task_type,omitempty"`
	TaskStatus           string           `json:"task_status,omitempty"`
	LastErrorMessage     string           `json:"last_error_message,omitempty"`
	LastDispatchAtMillis int64            `json:"last_dispatch_at_millis,omitempty"`
	UpdatedAtMillis      int64            `json:"updated_at_millis"`
	// UninstallTaskAcked is a weak signal: the uninstall task reported success.
	UninstallTaskAcked bool `json:"uninstall_task_acked,omitempty"`
	// RuntimeClean is the strong evidence that the probe really reverted the
	// enhancement. Only the evidence verifier may set it.
	RuntimeClean         bool   `json:"runtime_clean,omitempty"`
	RuntimeCleanAtMillis int64  `json:"runtime_clean_at_millis,omitempty"`
	EvidenceSource       string `json:"evidence_source,omitempty"`
}

const (
	EvidenceSourceRuntimeSnapshot   = "runtime_snapshot"
	EvidenceSourceNeverInstrumented = "never_instrumented"
)

type ListRulesQuery struct {
	AppID          string
	ServiceName    string
	InstrumentType InstrumentType
	DesiredState   RuleDesiredState
	Search         string
	IncludeDeleted bool
	// UnconfirmedOnly keeps deleted rules whose uninstall evidence is missing.
	UnconfirmedOnly bool
}

type CreateRuleRequest struct {
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	AppID            string         `json:"app_id"`
	ServiceName      string         `json:"service_name"`
	ScopeType        ScopeType      `json:"scope_type,omitempty"`
	TargetAgentIDs   []string       `json:"target_agent_ids,omitempty"`
	ClassName        string         `json:"class_name"`
	MethodName       string         `json:"method_name"`
	ParameterTypes   string         `json:"parameter_types,omitempty"`
	MethodDescriptor string         `json:"method_descriptor,omitempty"`
	InstrumentType   InstrumentType `json:"instrument_type"`
	SpanName         string         `json:"span_name,omitempty"`
	CaptureArgs      string         `json:"capture_args,omitempty"`
	CaptureReturn    string         `json:"capture_return,omitempty"`
	CaptureMaxLength int            `json:"capture_max_length,omitempty"`
	Force            bool           `json:"force,omitempty"`
	CreatedBy        string         `json:"created_by,omitempty"`
}

type UpdateRuleRequest struct {
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	ScopeType        ScopeType      `json:"scope_type,omitempty"`
	TargetAgentIDs   []string       `json:"target_agent_ids,omitempty"`
	ClassName        string         `json:"class_name"`
	MethodName       string         `json:"method_name"`
	ParameterTypes   string         `json:"parameter_types,omitempty"`
	MethodDescriptor string         `json:"method_descriptor,omitempty"`
	InstrumentType   InstrumentType `json:"instrument_type"`
	SpanName         string         `json:"span_name,omitempty"`
	CaptureArgs      string         `json:"capture_args,omitempty"`
	CaptureReturn    string         `json:"capture_return,omitempty"`
	CaptureMaxLength int            `json:"capture_max_length,omitempty"`
	Force            bool           `json:"force,omitempty"`
	UpdatedBy        string         `json:"updated_by,omitempty"`
}
