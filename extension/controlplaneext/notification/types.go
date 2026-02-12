// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package notification provides artifact analysis notification capabilities.
//
// When an artifact (e.g., profiling data) is persisted to BlobStore,
// the notifier can trigger external analysis services and track notification status.
package notification

import "time"

// ArtifactNotification contains the information needed to notify an analysis service.
type ArtifactNotification struct {
	TaskID       string            `json:"task_id"`
	TaskType     string            `json:"task_type"`
	Profiler     string            `json:"profiler"`      // e.g., "async-profiler", "pprof"
	Event        string            `json:"event"`         // e.g., "cpu", "alloc", "wall"
	ArtifactRef  string            `json:"artifact_ref"`
	ArtifactSize int64             `json:"artifact_size"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// NotifyResult records the outcome of a notification attempt.
type NotifyResult struct {
	Success      bool
	StatusCode   int
	ErrorMessage string
	AttemptCount int
	NotifiedAt   time.Time
}

// Record tracks the lifecycle of a notification for persistence and retry.
type Record struct {
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	TaskType     string    `json:"task_type"`
	Profiler     string    `json:"profiler,omitempty"`
	Event        string    `json:"event,omitempty"`
	ArtifactRef  string    `json:"artifact_ref"`
	Status       Status    `json:"status"`
	AttemptCount int       `json:"attempt_count"`
	LastError    string    `json:"last_error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Status represents the notification lifecycle state.
type Status string

const (
	StatusPending          Status = "pending"
	StatusSent             Status = "sent"
	StatusFailed           Status = "failed"
	StatusRetrying         Status = "retrying"
	StatusCallbackReceived Status = "callback_received"
)

// Config holds the configuration for artifact analysis notification.
type Config struct {
	// Enabled controls whether analysis notification is active.
	Enabled bool `mapstructure:"enabled"`

	// AnalysisServiceURL is the perf-analysis HTTP task submission endpoint.
	// Example: "http://perf-analysis:8080/tasks"
	AnalysisServiceURL string `mapstructure:"analysis_service_url"`

	// CallbackURL is this service's callback endpoint for analysis completion.
	// Example: "http://otel-admin:8888/api/v2/callback/analysis"
	CallbackURL string `mapstructure:"callback_url"`

	// TaskTypes whitelist: only these task types trigger notification.
	// Default: ["async_profiler"]
	TaskTypes []string `mapstructure:"task_types"`

	// Timeout for the notification HTTP request.
	Timeout time.Duration `mapstructure:"timeout"`

	// RedisName is the Redis connection name for notification record storage.
	RedisName string `mapstructure:"redis_name"`

	// KeyPrefix for notification record Redis keys.
	KeyPrefix string `mapstructure:"key_prefix"`

	// RecordTTL is how long notification records are retained.
	RecordTTL time.Duration `mapstructure:"record_ttl"`
}

// DefaultConfig returns sensible defaults for notification.
func DefaultConfig() Config {
	return Config{
		Enabled:   false,
		TaskTypes: []string{"async-profiler"},
		Timeout:   10 * time.Second,
		RedisName: "default",
		KeyPrefix: "otel:notifications",
		RecordTTL: 72 * time.Hour,
	}
}
