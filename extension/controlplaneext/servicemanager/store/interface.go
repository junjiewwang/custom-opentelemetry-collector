// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrServiceNotFound is returned when a service record does not exist.
var ErrServiceNotFound = errors.New("service not found")

// ServiceNotFound wraps ErrServiceNotFound with a context description.
func ServiceNotFound(appID, serviceName string) error {
	return fmt.Errorf("%w: appID=%s, serviceName=%s", ErrServiceNotFound, appID, serviceName)
}

// ServiceNotFoundByID wraps ErrServiceNotFound with an ID context.
func ServiceNotFoundByID(serviceID string) error {
	return fmt.Errorf("%w: id=%s", ErrServiceNotFound, serviceID)
}

// ServiceInfo is the Store-layer internal representation of a service entity.
// It mirrors the public ServiceInfo but may carry store-specific fields.
type ServiceInfo struct {
	// ID is the internal unique identifier (Base62).
	ID string `json:"id"`

	// AppID is the application group this service belongs to.
	AppID string `json:"app_id"`

	// ServiceName is the business-level service name.
	ServiceName string `json:"service_name"`

	// Description is an optional human-readable description.
	Description string `json:"description"`

	// Tags holds arbitrary key-value metadata.
	Tags map[string]string `json:"tags"`

	// CreatedAt is when the service record was first created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the service metadata was last modified.
	UpdatedAt time.Time `json:"updated_at"`

	// LastSeenAt is the last time an instance reported for this service.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

// ListServiceFilter defines filtering options for list operations at the Store layer.
type ListServiceFilter struct {
	// NamePattern is an optional prefix/substring filter on service name.
	NamePattern string
}

// ServiceStore defines the persistence boundary for service entity management.
//
// Implementations are responsible for storage-specific atomicity (e.g., Lua scripts
// for Redis, sync.Mutex for memory). Business logic such as delete preconditions,
// backfill policy, and runtime aggregation belongs in the Service layer.
type ServiceStore interface {
	// CreateIfAbsent atomically creates a service record if the (appID, serviceName)
	// does not already exist. If it already exists, returns (false, existingRecord, nil).
	// If created, returns (true, newRecord, nil).
	// Both the main record and the ID index must be created atomically.
	CreateIfAbsent(ctx context.Context, svc *ServiceInfo) (created bool, existing *ServiceInfo, err error)

	// Get retrieves a service by its business key (appID, serviceName).
	// Returns (nil, ErrServiceNotFound) if not found.
	Get(ctx context.Context, appID, serviceName string) (*ServiceInfo, error)

	// GetByID retrieves a service by its internal ID.
	// Returns (nil, ErrServiceNotFound) if not found.
	GetByID(ctx context.Context, serviceID string) (*ServiceInfo, error)

	// Update persists changes to an existing service record.
	// The caller must ensure only mutable fields are modified.
	// Returns ErrServiceNotFound if the service does not exist.
	Update(ctx context.Context, svc *ServiceInfo) error

	// Delete removes a service record and its ID index entry atomically.
	// Returns ErrServiceNotFound if the service does not exist.
	Delete(ctx context.Context, appID, serviceName string) error

	// ListByApp returns all services belonging to a given app.
	ListByApp(ctx context.Context, appID string, filter ListServiceFilter) ([]*ServiceInfo, error)

	// ListAll returns all services across all apps.
	ListAll(ctx context.Context, filter ListServiceFilter) ([]*ServiceInfo, error)

	// Start initializes the store.
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}
