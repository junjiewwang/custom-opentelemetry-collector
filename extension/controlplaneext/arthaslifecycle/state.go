// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthaslifecycle

import "context"

// RuntimeState describes the Arthas runtime inside the target JVM.
type RuntimeState string

const (
	RuntimeUnknown RuntimeState = "UNKNOWN"
	RuntimeRunning RuntimeState = "RUNNING"
	RuntimeStopped RuntimeState = "STOPPED"
)

// State is the Control Plane's observation of one Agent's Arthas lifecycle.
// All timestamps are Unix milliseconds.
type State struct {
	AgentID               string       `json:"agent_id"`
	Runtime               RuntimeState `json:"runtime"`
	RuntimeObservedAt     int64        `json:"runtime_observed_at,omitempty"`
	AttachedAt            int64        `json:"attached_at,omitempty"`
	LastActivityAt        int64        `json:"last_activity_at,omitempty"`
	LastActivitySource    string       `json:"last_activity_source,omitempty"`
	ActiveSessions        int          `json:"active_sessions,omitempty"`
	LastDetachSubmittedAt int64        `json:"last_detach_submitted_at,omitempty"`
}

// Tracker stores per-Agent lifecycle observations. The memory implementation is
// used for single-replica deployments; the Redis implementation shares state
// across replicas.
type Tracker interface {
	Touch(ctx context.Context, agentID, source string, now int64) error
	SetRuntime(ctx context.Context, agentID string, runtime RuntimeState, now int64) error
	ChangeSessions(ctx context.Context, agentID string, delta int, now int64) error
	Reset(ctx context.Context, agentID string, now int64) error
	Get(ctx context.Context, agentID string) (State, bool, error)
	ListIdle(ctx context.Context, now, idleThresholdMillis int64, limit int) ([]State, error)
	TryAcquireDetach(ctx context.Context, agentID string, cooldownMillis, now int64) (bool, error)
}
