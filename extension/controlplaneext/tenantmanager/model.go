// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"errors"
	"time"
)

// DefaultTenantID is the built-in tenant that owns all pre-existing apps.
// It is created idempotently on startup (EnsureDefaultTenant) and never deleted.
const DefaultTenantID = "admin"

// Tenant status values.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Tenant is a multi-tenancy isolation boundary that groups apps. It is the
// read-path counterpart to AppInfo.TenantID: a Tenant API Key resolves to a
// TenantID, which scopes queries to that tenant's data.
type Tenant struct {
	// ID is the unique identifier. The default tenant uses the reserved
	// "admin" ID; all others are generated as Base62 strings.
	ID string `json:"id"`

	// Name is a human-readable, unique name.
	Name string `json:"name"`

	// Description is an optional free-form description.
	Description string `json:"description,omitempty"`

	// Status is "active" or "disabled". A disabled tenant's keys are rejected
	// at authentication time.
	Status string `json:"status"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateTenantRequest is the input for creating a tenant.
type CreateTenantRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Validate checks the request. Name must be non-empty.
func (r *CreateTenantRequest) Validate() error {
	if r == nil {
		return errors.New("request is required")
	}
	if r.Name == "" {
		return errors.New("name is required")
	}
	return nil
}

// UpdateTenantRequest is the input for updating a tenant. Empty fields mean
// "leave unchanged" (name/description are never valid as empty strings, and
// status defaults to its current value when omitted).
type UpdateTenantRequest struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
}

// Validate checks the request. Returns an error if Status is set to an
// unsupported value.
func (r *UpdateTenantRequest) Validate() error {
	if r == nil {
		return errors.New("request is required")
	}
	if r.Status != "" && r.Status != StatusActive && r.Status != StatusDisabled {
		return errors.New("invalid status, must be 'active' or 'disabled'")
	}
	return nil
}
