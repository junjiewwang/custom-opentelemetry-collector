// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package servicemanager

import "time"

// ServiceInfo represents a service entity with metadata and runtime aggregated fields.
// Persistent fields are stored in the Store layer; runtime fields are dynamically aggregated
// by the Service layer and are never persisted.
type ServiceInfo struct {
	// ===== Persistent Fields =====

	// ID is the internal unique identifier (Base62, generated once on creation).
	ID string `json:"id"`

	// AppID is the application group this service belongs to.
	AppID string `json:"app_id"`

	// ServiceName is the business-level service name (immutable after creation).
	ServiceName string `json:"service_name"`

	// Description is an optional human-readable description.
	Description string `json:"description"`

	// Tags holds arbitrary key-value metadata for the service.
	Tags map[string]string `json:"tags"`

	// CreatedAt is when the service record was first created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the service metadata was last modified.
	UpdatedAt time.Time `json:"updated_at"`

	// LastSeenAt is the last time an instance reported for this service.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`

	// ===== Runtime Aggregated Fields (not persisted) =====

	// InstanceCount is the total number of registered instances.
	InstanceCount int `json:"instance_count,omitempty"`

	// OnlineCount is the number of currently online instances.
	OnlineCount int `json:"online_count,omitempty"`

	// HasConfig indicates whether this service has associated configuration.
	HasConfig bool `json:"has_config,omitempty"`

	// ConfigSource describes the configuration source (e.g., "nacos", "on_demand").
	ConfigSource string `json:"config_source,omitempty"`
}

// CreateServiceRequest is the request to explicitly create a service.
type CreateServiceRequest struct {
	// AppID is the application group the service belongs to (required).
	AppID string `json:"app_id"`

	// ServiceName is the business-level service name (required, immutable).
	ServiceName string `json:"service_name"`

	// Description is an optional human-readable description.
	Description string `json:"description,omitempty"`

	// Tags holds arbitrary key-value metadata.
	Tags map[string]string `json:"tags,omitempty"`
}

// UpdateServiceRequest is the request to update a service's mutable metadata.
// Only Description and Tags are updatable; AppID, ServiceName, and ID are immutable.
type UpdateServiceRequest struct {
	// Description is the new description (empty string clears it).
	Description *string `json:"description,omitempty"`

	// Tags replaces the entire tag map. nil means no change; empty map clears all tags.
	Tags map[string]string `json:"tags,omitempty"`
}

// ListServicesQuery defines query parameters for listing services at the Service layer.
type ListServicesQuery struct {
	// NamePattern is an optional prefix/substring filter on service name.
	NamePattern string `json:"name_pattern,omitempty"`

	// IncludeRuntime controls whether runtime aggregated fields are populated.
	IncludeRuntime bool `json:"include_runtime,omitempty"`
}

// BackfillOptions configures the service backfill process.
type BackfillOptions struct {
	// FromRegistry backfills services from AgentRegistry (currently visible instances).
	FromRegistry bool `json:"from_registry,omitempty"`

	// FromConfig backfills services from existing configuration data.
	FromConfig bool `json:"from_config,omitempty"`

	// DryRun only reports what would be created without actually persisting.
	DryRun bool `json:"dry_run,omitempty"`
}

// BackfillResult holds the outcome of a backfill operation.
type BackfillResult struct {
	// Created is the number of new service records created.
	Created int `json:"created"`

	// Skipped is the number of services that already existed and were not overwritten.
	Skipped int `json:"skipped"`

	// Errors is the number of services that failed to backfill.
	Errors int `json:"errors"`

	// Details contains per-service backfill results for debugging.
	Details []BackfillDetail `json:"details,omitempty"`
}

// BackfillDetail records the outcome for a single service during backfill.
type BackfillDetail struct {
	AppID       string `json:"app_id"`
	ServiceName string `json:"service_name"`
	Action      string `json:"action"` // "created", "skipped", "error"
	Error       string `json:"error,omitempty"`
}
