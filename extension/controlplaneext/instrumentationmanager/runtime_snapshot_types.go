// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

type RuntimeRefreshStatus string

const (
	RuntimeRefreshStatusIdle    RuntimeRefreshStatus = "idle"
	RuntimeRefreshStatusSuccess RuntimeRefreshStatus = "success"
	RuntimeRefreshStatusFailed  RuntimeRefreshStatus = "failed"
	RuntimeRefreshStatusTimeout RuntimeRefreshStatus = "timeout"
	RuntimeRefreshStatusSkipped RuntimeRefreshStatus = "skipped"
)

type RuntimeDriftReason string

const (
	RuntimeDriftReasonMissing                    RuntimeDriftReason = "missing"
	RuntimeDriftReasonIneffective                RuntimeDriftReason = "ineffective"
	RuntimeDriftReasonDeletedResidual            RuntimeDriftReason = "deleted_residual"
	RuntimeDriftReasonPausedResidual             RuntimeDriftReason = "paused_residual"
	RuntimeDriftReasonInstrumentationUnavailable RuntimeDriftReason = "instrumentation_unavailable"
	RuntimeDriftReasonEnhancementUnavailable     RuntimeDriftReason = "enhancement_unavailable"
)

type RuleRuntimeSnapshot struct {
	RuleID            string                       `json:"rule_id"`
	DesiredState      RuleDesiredState             `json:"desired_state"`
	GeneratedAtMillis int64                        `json:"generated_at_millis"`
	Summary           RuleRuntimeSnapshotSummary   `json:"summary"`
	Targets           []*RuleRuntimeSnapshotTarget `json:"targets"`
}

type RuleRuntimeSnapshotSummary struct {
	TotalTargets                      int `json:"total_targets"`
	SnapshotAvailableTargets          int `json:"snapshot_available_targets"`
	RuntimeFoundTargets               int `json:"runtime_found_targets"`
	EffectiveTargets                  int `json:"effective_targets"`
	DriftedTargets                    int `json:"drifted_targets"`
	MissingTargets                    int `json:"missing_targets"`
	StaleTargets                      int `json:"stale_targets"`
	RefreshFailedTargets              int `json:"refresh_failed_targets"`
	InstrumentationUnavailableTargets int `json:"instrumentation_unavailable_targets"`
	EnhancementUnavailableTargets     int `json:"enhancement_unavailable_targets"`
}

type RuleRuntimeSnapshotTarget struct {
	RuleID                   string               `json:"rule_id"`
	AgentID                  string               `json:"agent_id"`
	Hostname                 string               `json:"hostname,omitempty"`
	IP                       string               `json:"ip,omitempty"`
	DesiredState             RuleDesiredState     `json:"desired_state"`
	ControlplaneState        TargetState          `json:"controlplane_state"`
	ControlplaneTaskStatus   string               `json:"controlplane_task_status,omitempty"`
	SnapshotAvailable        bool                 `json:"snapshot_available"`
	RuntimeFound             bool                 `json:"runtime_found"`
	RuntimeStatus            string               `json:"runtime_status,omitempty"`
	IsApplied                bool                 `json:"is_applied"`
	IsEffective              bool                 `json:"is_effective"`
	InstrumentationAvailable bool                 `json:"instrumentation_available"`
	EnhancementCapability    bool                 `json:"enhancement_capability"`
	ActiveTransformerCount   int                  `json:"active_transformer_count,omitempty"`
	DiagnosticMessage        string               `json:"diagnostic_message,omitempty"`
	InstrumentationSource    string               `json:"instrumentation_source,omitempty"`
	RefreshedAtMillis        int64                `json:"refreshed_at_millis,omitempty"`
	ExpiresAtMillis          int64                `json:"expires_at_millis,omitempty"`
	IsStale                  bool                 `json:"is_stale"`
	Dirty                    bool                 `json:"dirty"`
	LastRefreshStatus        RuntimeRefreshStatus `json:"last_refresh_status,omitempty"`
	LastErrorMessage         string               `json:"last_error_message,omitempty"`
	DriftReasons             []RuntimeDriftReason `json:"drift_reasons,omitempty"`
}
