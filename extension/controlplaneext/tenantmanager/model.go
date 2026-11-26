// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"errors"
	"fmt"
	"time"
)

// DefaultTenantID is the built-in tenant that owns all pre-existing apps.
// It is created idempotently on startup (EnsureDefaultTenant) and never deleted.
const DefaultTenantID = "admin"

// DefaultAccountID is the VictoriaMetrics account reserved for the built-in
// "admin" tenant and for global (tenant-less) data. Real tenants get account
// IDs allocated monotonically from 1 upward via TenantRepository.NextAccountID.
const DefaultAccountID uint32 = 0

// Tenant status values.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// TenantObservabilityQueryConfig overrides the deployment-level MCP
// observability query limits for a tenant. Each pointer field is tri-state:
// nil inherits the deployment default, an explicit value overrides it. Values
// may exceed the deployment defaults (operator-managed, risk accepted).
type TenantObservabilityQueryConfig struct {
	QueryTimeout    *int    `json:"query_timeout,omitempty"`
	MaxTimeRange    *string `json:"max_time_range,omitempty"`
	MetricSeries    *int    `json:"metric_series,omitempty"`
	Traces          *int    `json:"traces,omitempty"`
	Logs            *int    `json:"logs,omitempty"`
	LogContextLines *int    `json:"log_context_lines,omitempty"`
}

// TenantCapabilities holds per-tenant feature switches managed from the
// operator console. Pointer fields are tri-state: nil means "inherit the
// deployment default" (currently allowed), an explicit value overrides it.
type TenantCapabilities struct {
	// DynamicInstrumentation controls whether agents owned by this tenant may
	// be instrumented by the dynamic instrumentation rule manager.
	DynamicInstrumentation *bool `json:"dynamic_instrumentation,omitempty"`

	// ObservabilityQuery overrides MCP read-only query limits for callers
	// authenticated as this tenant. It is a cost/context control, not an
	// authorization of the queried data (which is isolated separately).
	ObservabilityQuery *TenantObservabilityQueryConfig `json:"observability_query,omitempty"`
}

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

	// Capabilities are optional per-tenant feature switches. Legacy records
	// without this field inherit the deployment default.
	Capabilities *TenantCapabilities `json:"capabilities,omitempty"`

	// AccountID is the VictoriaMetrics native-multitenancy account that hard-
	// isolates this tenant's metrics. 0 is reserved for the default "admin"
	// tenant / global data; real tenants are allocated monotonically from 1.
	// A zero value also keeps the current label-based soft isolation as the
	// fallback path during migration.
	AccountID uint32 `json:"account_id,omitempty"`

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

	// DynamicInstrumentation is a tri-state override: nil leaves the current
	// capability unchanged, false disables instrumentation for this tenant's
	// agents, true enables it explicitly.
	DynamicInstrumentation *bool `json:"dynamic_instrumentation,omitempty"`

	// ClearDynamicInstrumentation removes the tenant override so the value is
	// inherited from the deployment default. It takes precedence over
	// DynamicInstrumentation when both are supplied.
	ClearDynamicInstrumentation bool `json:"clear_dynamic_instrumentation,omitempty"`

	// ObservabilityQuery replaces the whole per-tenant query-limit object.
	// Fields present in the object are set (and may exceed deployment
	// defaults); fields omitted are cleared back to "inherit".
	ObservabilityQuery *TenantObservabilityQueryConfig `json:"observability_query,omitempty"`

	// ClearObservabilityQuery removes the whole override so all six limits
	// inherit the deployment defaults. It takes precedence over
	// ObservabilityQuery when both are supplied.
	ClearObservabilityQuery bool `json:"clear_observability_query,omitempty"`
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
	if err := validateObservabilityQueryConfig(r.ObservabilityQuery); err != nil {
		return err
	}
	return nil
}

// validateObservabilityQueryConfig fails fast at save time so a bad operator
// value never surfaces as a runtime query error. Values are allowed to exceed
// deployment defaults (operator-managed, risk accepted).
func validateObservabilityQueryConfig(cfg *TenantObservabilityQueryConfig) error {
	if cfg == nil {
		return nil
	}
	positive := map[string]*int{
		"query_timeout":     cfg.QueryTimeout,
		"metric_series":     cfg.MetricSeries,
		"traces":            cfg.Traces,
		"logs":              cfg.Logs,
		"log_context_lines": cfg.LogContextLines,
	}
	for name, value := range positive {
		if value != nil && *value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if cfg.MaxTimeRange != nil {
		d, err := time.ParseDuration(*cfg.MaxTimeRange)
		if err != nil || d <= 0 {
			return fmt.Errorf("max_time_range must be a positive Go duration: %q", *cfg.MaxTimeRange)
		}
	}
	return nil
}
