// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package lifecycle provides backend-agnostic data lifecycle management.
//
// Design Principles:
//   - DIP: All components depend on abstractions, not concrete implementations
//   - ISP: Fine-grained interfaces, each consumer depends only on what it needs
//   - SRP: Each interface has a single, well-defined responsibility
//   - OCP: New storage backends only implement interfaces, no scheduler changes
//   - Strategy Pattern: Providers choose their optimal purge algorithm internally
package lifecycle

import (
	"context"
	"time"
)

// SignalType identifies the kind of observability signal.
type SignalType string

const (
	SignalTrace  SignalType = "trace"
	SignalMetric SignalType = "metric"
	SignalLog    SignalType = "log"
)

// AllSignals returns all supported signal types for iteration.
func AllSignals() []SignalType {
	return []SignalType{SignalTrace, SignalMetric, SignalLog}
}

// ═══════════════════════════════════════════════════
// LifecyclePurger — Data Expiration Executor
// ═══════════════════════════════════════════════════

// LifecyclePurger is the sole abstraction for data expiration execution.
// Each storage backend implements this interface with its native cleanup mechanism.
//
// The implementation decides the most efficient strategy internally:
//   - Elasticsearch: delete entire date-based indices, fallback to delete_by_query
//   - PostgreSQL: DROP PARTITION or DELETE WHERE timestamp < cutoff
//   - MongoDB: drop time-bucketed collections or rely on TTL indexes
//
// Callers (the Scheduler) never need to know the underlying strategy.
type LifecyclePurger interface {
	// PurgeExpired removes all data for the given signal older than `before`.
	// Returns the result of the purge operation.
	PurgeExpired(ctx context.Context, signal SignalType, before time.Time) (*PurgeResult, error)

	// PurgeByApp removes expired data scoped to a specific application.
	PurgeByApp(ctx context.Context, appID string, signal SignalType, before time.Time) (*PurgeResult, error)

	// EstimatePurge returns a preview of what would be deleted WITHOUT executing.
	// Used for dry-run and UI preview scenarios.
	EstimatePurge(ctx context.Context, signal SignalType, before time.Time) (*PurgeEstimate, error)

	// GetDataBoundary returns the oldest and newest data timestamp per signal.
	// Used by the scheduler to determine if cleanup is needed.
	GetDataBoundary(ctx context.Context, signal SignalType) (*DataBoundary, error)
}

// ═══════════════════════════════════════════════════
// RetentionResolver — Policy Resolution Chain
// ═══════════════════════════════════════════════════

// RetentionResolver resolves the effective retention policy for a given context.
// It follows the Chain-of-Responsibility pattern:
//
//	AppOverride → PlatformDefault → BuiltinFallback
//
// New resolution sources (e.g., TenantTier) can be added as new nodes
// without modifying existing ones (OCP).
type RetentionResolver interface {
	// Resolve returns the effective retention for a signal, optionally scoped to an app.
	// If appID is empty, returns the platform-level default.
	Resolve(ctx context.Context, signal SignalType, appID string) (EffectiveRetention, error)

	// ResolveAll returns retention for all signals, optionally scoped to an app.
	ResolveAll(ctx context.Context, appID string) (map[SignalType]EffectiveRetention, error)
}

// ═══════════════════════════════════════════════════
// RetentionStore — Policy Persistence
// ═══════════════════════════════════════════════════

// RetentionStore abstracts the persistence of retention policies.
// Decoupled from how policies are stored (config file, DB, KV store).
type RetentionStore interface {
	// GetForApp returns the per-app override (nil if no override exists).
	GetForApp(ctx context.Context, appID string, signal SignalType) (*time.Duration, error)

	// SetForApp sets a per-app retention override.
	SetForApp(ctx context.Context, appID string, signal SignalType, retention time.Duration) error

	// DeleteForApp removes a per-app override, falling back to platform default.
	DeleteForApp(ctx context.Context, appID string, signal SignalType) error

	// ListAppOverrides returns all apps that have custom retention settings.
	ListAppOverrides(ctx context.Context) ([]AppRetentionEntry, error)
}

// ═══════════════════════════════════════════════════
// UsageReporter — Storage Observation
// ═══════════════════════════════════════════════════

// UsageReporter provides storage usage information in a backend-agnostic way.
type UsageReporter interface {
	// GetUsage returns current storage usage.
	GetUsage(ctx context.Context) (*StorageUsage, error)
}

// ═══════════════════════════════════════════════════
// AuditEmitter — Lifecycle Event Emission
// ═══════════════════════════════════════════════════

// AuditEmitter emits lifecycle audit events.
// Implementations can log to structured logger, write to storage, or send webhooks.
type AuditEmitter interface {
	// Emit records a lifecycle event.
	Emit(ctx context.Context, event LifecycleEvent)
}

// ═══════════════════════════════════════════════════
// IndexLister — Read-Only Index Scanning
// ═══════════════════════════════════════════════════

// IndexLister extends LifecyclePurger with the ability to list expired indices
// without deleting them. Used by the Leader to plan distributed tasks.
//
// This is an optional interface — if the purger implements it, the scheduler
// uses it for distributed planning. Otherwise falls back to single-node mode.
type IndexLister interface {
	// ListExpired returns the index/partition names that are expired (before cutoff).
	// Does NOT delete anything — read-only operation.
	ListExpired(ctx context.Context, signal SignalType, before time.Time) ([]string, error)
}

// ═══════════════════════════════════════════════════
// SingleIndexPurger — Fine-Grained Deletion
// ═══════════════════════════════════════════════════

// SingleIndexPurger extends LifecyclePurger with single-index deletion capability.
// Used by Workers in distributed mode to execute one task at a time.
//
// The operation MUST be idempotent — deleting an already-deleted index returns nil.
type SingleIndexPurger interface {
	// DeleteSingleIndex deletes a single index/partition by exact name.
	// Returns nil if the index doesn't exist (idempotent).
	DeleteSingleIndex(ctx context.Context, indexName string) error
}


