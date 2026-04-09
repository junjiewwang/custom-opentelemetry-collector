// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package servicemanager

import "context"

// ServiceManager defines the public interface for service entity management.
// It encapsulates business logic such as idempotent creation (EnsureService),
// delete preconditions, metadata immutability rules, and runtime aggregation.
type ServiceManager interface {
	// CreateService explicitly creates a new service record.
	// Returns an error if the (appID, serviceName) pair already exists.
	CreateService(ctx context.Context, req *CreateServiceRequest) (*ServiceInfo, error)

	// EnsureService guarantees a service record exists for the given (appID, serviceName).
	// If it already exists, returns the existing record. If not, creates a new one.
	// This is the primary entry point called from the agent registration path.
	// Failures must NOT block the agent registration flow.
	EnsureService(ctx context.Context, appID, serviceName string) (*ServiceInfo, error)

	// GetService retrieves a service by its business key (appID, serviceName).
	// Returns (nil, ErrServiceNotFound) if not found.
	GetService(ctx context.Context, appID, serviceName string) (*ServiceInfo, error)

	// GetServiceByID retrieves a service by its internal ID.
	// Returns (nil, ErrServiceNotFound) if not found.
	GetServiceByID(ctx context.Context, serviceID string) (*ServiceInfo, error)

	// UpdateServiceMetadata updates the mutable metadata (description, tags) of a service.
	// AppID, ServiceName, and ID are immutable and cannot be changed.
	UpdateServiceMetadata(ctx context.Context, appID, serviceName string, req *UpdateServiceRequest) (*ServiceInfo, error)

	// DeleteService removes a service record.
	// Default precondition: instance_count == 0. Does NOT cascade-delete configuration.
	DeleteService(ctx context.Context, appID, serviceName string) error

	// ListServicesByApp returns all services belonging to a given app.
	ListServicesByApp(ctx context.Context, appID string, q ListServicesQuery) ([]*ServiceInfo, error)

	// ListAllServices returns all services across all apps.
	ListAllServices(ctx context.Context, q ListServicesQuery) ([]*ServiceInfo, error)

	// BackfillServices creates service records from existing data sources
	// (AgentRegistry, configuration data) using idempotent semantics.
	BackfillServices(ctx context.Context, opts BackfillOptions) (*BackfillResult, error)

	// SetBackfillDataSource injects the data source used by BackfillServices.
	// This is called after all components are initialized (e.g., in extension.Start).
	SetBackfillDataSource(ds BackfillDataSource)

	// Start initializes the service manager.
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}

// BackfillDataSource provides the data needed for service backfill.
// It is defined as a minimal interface to avoid circular dependencies
// between servicemanager and agentregistry/tokenmanager packages.
// The extension layer (extension.go) is responsible for creating an adapter
// that satisfies this interface using the real AgentRegistry and TokenManager.
type BackfillDataSource interface {
	// GetAllAppIDs returns all known application IDs.
	GetAllAppIDs(ctx context.Context) ([]string, error)

	// GetServiceNamesByApp returns all known service names for a given app.
	// This typically comes from AgentRegistry's hierarchy index.
	GetServiceNamesByApp(ctx context.Context, appID string) ([]string, error)

	// GetConfiguredServiceNamesByApp returns all service names that have
	// configuration data under the given appID. This comes from the
	// ConfigManager's enumeration API (e.g., ListServiceConfigs).
	// Returns nil, nil if the config data source is not available.
	GetConfiguredServiceNamesByApp(ctx context.Context, appID string) ([]string, error)
}

// Config holds configuration for ServiceManager.
type Config struct {
	// Type specifies the backend type: "memory" or "redis".
	Type string `mapstructure:"type"`

	// RedisName is the name of the Redis connection from storage extension.
	RedisName string `mapstructure:"redis_name"`

	// KeyPrefix is the prefix for all Redis keys used by ServiceManager.
	KeyPrefix string `mapstructure:"key_prefix"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Type:      "memory",
		RedisName: "default",
		KeyPrefix: "otel:services",
	}
}
