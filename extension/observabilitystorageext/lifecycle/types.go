// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"time"
)

// ═══════════════════════════════════════════════════
// Purge Types
// ═══════════════════════════════════════════════════

// PurgeResult holds the result of a data purge operation.
type PurgeResult struct {
	Signal       SignalType `json:"signal"`
	DeletedDocs  int64     `json:"deletedDocs"`
	DeletedUnits int       `json:"deletedUnits"` // indices / partitions / collections deleted
	FreedBytes   int64     `json:"freedBytes,omitempty"`
	Message      string    `json:"message,omitempty"`
	Duration     time.Duration `json:"duration"`
}

// PurgeEstimate previews a purge operation without executing.
type PurgeEstimate struct {
	Signal         SignalType `json:"signal"`
	EstimatedDocs  int64     `json:"estimatedDocs"`
	EstimatedBytes int64     `json:"estimatedBytes"`
	AffectedUnits  []string  `json:"affectedUnits"` // index/partition/collection names
}

// DataBoundary represents the time range of existing data for a signal.
type DataBoundary struct {
	Signal   SignalType  `json:"signal"`
	OldestAt *time.Time `json:"oldestAt,omitempty"`
	NewestAt *time.Time `json:"newestAt,omitempty"`
	IsEmpty  bool       `json:"isEmpty"`
}

// ═══════════════════════════════════════════════════
// Retention Types
// ═══════════════════════════════════════════════════

// EffectiveRetention holds the resolved retention with provenance metadata.
type EffectiveRetention struct {
	Duration   time.Duration `json:"duration"`
	Source     RetentionSource `json:"source"`
	MaxAllowed time.Duration `json:"maxAllowed"`
	Clamped    bool          `json:"clamped"` // true if original request exceeded max
}

// RetentionSource describes where a retention policy came from.
type RetentionSource string

const (
	SourceAppOverride    RetentionSource = "app_override"
	SourcePlatformDefault RetentionSource = "platform_default"
	SourceBuiltinDefault RetentionSource = "builtin"
)

// RetentionDefaults holds the platform-level default retention durations.
type RetentionDefaults struct {
	Trace  time.Duration
	Metric time.Duration
	Log    time.Duration
}

// RetentionLimits holds the platform-level maximum retention durations.
type RetentionLimits struct {
	MaxTrace  time.Duration
	MaxMetric time.Duration
	MaxLog    time.Duration
}

// AppRetentionEntry represents one app's retention overrides.
type AppRetentionEntry struct {
	AppID     string                      `json:"appId"`
	Overrides map[SignalType]time.Duration `json:"overrides"`
}

// ═══════════════════════════════════════════════════
// Usage Types
// ═══════════════════════════════════════════════════

// StorageUsage represents current storage resource consumption.
type StorageUsage struct {
	TotalBytes     int64                `json:"totalBytes"`
	UsedBytes      int64                `json:"usedBytes"`
	AvailableBytes int64                `json:"availableBytes"`
	BySignal       map[SignalType]int64 `json:"bySignal,omitempty"`
	UsageRatio     float64              `json:"usageRatio"` // usedBytes/totalBytes
}

// UsageSnapshot is a point-in-time storage usage record for trend tracking.
type UsageSnapshot struct {
	Timestamp  time.Time            `json:"timestamp"`
	TotalBytes int64                `json:"totalBytes"`
	UsedBytes  int64                `json:"usedBytes"`
	BySignal   map[SignalType]int64 `json:"bySignal,omitempty"`
}

// ═══════════════════════════════════════════════════
// Audit Types
// ═══════════════════════════════════════════════════

// LifecycleAction describes the type of lifecycle operation.
type LifecycleAction string

const (
	ActionAutoPurge    LifecycleAction = "auto_purge"
	ActionManualPurge  LifecycleAction = "manual_purge"
	ActionSetRetention LifecycleAction = "set_retention"
	ActionEstimate     LifecycleAction = "estimate"
	ActionAlert        LifecycleAction = "alert"
	ActionDistPlan     LifecycleAction = "distributed_plan"
	ActionDistVerify   LifecycleAction = "distributed_verify"
)

// LifecycleEvent is an immutable record of a lifecycle operation.
type LifecycleEvent struct {
	Timestamp time.Time       `json:"timestamp"`
	Action    LifecycleAction `json:"action"`
	Signal    SignalType       `json:"signal"`
	AppID     string           `json:"appId,omitempty"`
	Operator  string           `json:"operator"` // "scheduler" | "api:admin" | "api:{user}"
	DryRun    bool             `json:"dryRun"`
	Input     any              `json:"input,omitempty"`
	Result    any              `json:"result,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// ═══════════════════════════════════════════════════
// Scheduler Config
// ═══════════════════════════════════════════════════

// SchedulerConfig holds scheduler behavior configuration.
// Completely decoupled from any storage-specific settings.
type SchedulerConfig struct {
	// Enabled controls whether the scheduler is active.
	Enabled bool `mapstructure:"enabled"`

	// Interval is the check frequency (default: 1h).
	Interval time.Duration `mapstructure:"interval"`

	// DryRun previews what would be deleted without executing.
	DryRun bool `mapstructure:"dry_run"`

	// UsageWarningRatio triggers WARN-level alerts (default: 0.75).
	UsageWarningRatio float64 `mapstructure:"usage_warning_ratio"`

	// UsageCriticalRatio triggers ERROR-level alerts (default: 0.90).
	UsageCriticalRatio float64 `mapstructure:"usage_critical_ratio"`

	// TrendBufferSize is how many usage snapshots to keep (default: 168 = 7d @ 1h).
	TrendBufferSize int `mapstructure:"trend_buffer_size"`

	// Distributed enables multi-node cooperative purge mode.
	// Requires Redis. Falls back to local mode if Redis is unavailable.
	Distributed bool `mapstructure:"distributed"`

	// DistributedThreshold: only use distributed mode when expired index count
	// exceeds this value. Below this, single-node is more efficient.
	// Default: 50.
	DistributedThreshold int `mapstructure:"distributed_threshold"`

	// WorkerConcurrency: max concurrent delete operations per node per cycle.
	// Controls ES pressure. Default: 10.
	WorkerConcurrency int `mapstructure:"worker_concurrency"`

	// TaskTimeout: max time a single task can take before considered timed-out.
	// Default: 30s.
	TaskTimeout time.Duration `mapstructure:"task_timeout"`

	// MaxRetries: max retry attempts for a failed task. Default: 3.
	MaxRetries int `mapstructure:"max_retries"`

	// VerifyTimeout: max time the leader waits for all tasks to complete
	// during the verification phase. Default: 2m.
	VerifyTimeout time.Duration `mapstructure:"verify_timeout"`

	// VerifyPollInterval: polling interval during verification. Default: 2s.
	VerifyPollInterval time.Duration `mapstructure:"verify_poll_interval"`

	// NodeID: unique identifier for this node. Auto-generated if empty.
	NodeID string `mapstructure:"node_id"`
}

// ApplyDefaults sets reasonable default values for unset fields.
func (c *SchedulerConfig) ApplyDefaults() {
	if c.Interval <= 0 {
		c.Interval = time.Hour
	}
	if c.UsageWarningRatio <= 0 {
		c.UsageWarningRatio = 0.75
	}
	if c.UsageCriticalRatio <= 0 {
		c.UsageCriticalRatio = 0.90
	}
	if c.TrendBufferSize <= 0 {
		c.TrendBufferSize = 168 // 7 days at 1h interval
	}
	if c.DistributedThreshold <= 0 {
		c.DistributedThreshold = 50
	}
	if c.WorkerConcurrency <= 0 {
		c.WorkerConcurrency = 10
	}
	if c.TaskTimeout <= 0 {
		c.TaskTimeout = 30 * time.Second
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 3
	}
	if c.VerifyTimeout <= 0 {
		c.VerifyTimeout = 2 * time.Minute
	}
	if c.VerifyPollInterval <= 0 {
		c.VerifyPollInterval = 2 * time.Second
	}
}

// Validate checks the config is valid.
func (c *SchedulerConfig) Validate() error {
	if c.Interval < time.Minute {
		c.Interval = time.Minute // minimum 1 minute
	}
	if c.UsageWarningRatio <= 0 || c.UsageWarningRatio >= 1 {
		c.UsageWarningRatio = 0.75
	}
	if c.UsageCriticalRatio <= c.UsageWarningRatio {
		c.UsageCriticalRatio = c.UsageWarningRatio + 0.10
	}
	if c.UsageCriticalRatio > 1 {
		c.UsageCriticalRatio = 0.95
	}
	return nil
}

// ═══════════════════════════════════════════════════
// Distributed Purge Types
// ═══════════════════════════════════════════════════

// PurgeTask represents a single unit of work in distributed purge.
// Granularity: 1 task = 1 ES index (or 1 PG partition) deletion.
type PurgeTask struct {
	ID        string     `json:"id"`        // unique: "{epoch}:{signal}:{indexName}"
	Epoch     int64      `json:"epoch"`     // batch epoch (unix millis)
	Signal    SignalType `json:"signal"`    // trace/metric/log
	IndexName string     `json:"indexName"` // exact index/partition to delete
	Cutoff    time.Time  `json:"cutoff"`    // retention cutoff time (for audit)
	Retry     int        `json:"retry"`     // retry attempt number (0-based)
}

// TaskResult records the outcome of executing a single PurgeTask.
type TaskResult struct {
	Status    TaskStatus `json:"status"`
	NodeID    string     `json:"nodeId"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	DoneAt    time.Time  `json:"doneAt"`
}

// TaskStatus represents the execution state of a task.
type TaskStatus string

const (
	TaskStatusSuccess TaskStatus = "success"
	TaskStatusFailed  TaskStatus = "failed"
	TaskStatusSkipped TaskStatus = "skipped" // index already gone (idempotent)
	TaskStatusTimeout TaskStatus = "timeout"
)

// PurgeProgress aggregates the execution state for an epoch.
type PurgeProgress struct {
	Epoch      int64  `json:"epoch"`
	TotalTasks int    `json:"totalTasks"`
	Completed  int    `json:"completed"` // success + skipped
	Failed     int    `json:"failed"`
	Remaining  int    `json:"remaining"` // still in queue
	Status     string `json:"status"`    // "executing" | "done" | "timeout"
}
