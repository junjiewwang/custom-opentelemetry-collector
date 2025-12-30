// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/redis/go-redis/v9"
)

// Ensure Extension implements the required interfaces.
var (
	_ extension.Extension = (*Extension)(nil)
	_ Storage             = (*Extension)(nil)
)

// Extension implements the storage extension.
type Extension struct {
	config   *Config
	settings extension.Settings
	logger   *zap.Logger

	// Redis clients
	redisClients map[string]redis.UniversalClient
	redisMu      sync.RWMutex

	// Nacos clients
	nacosConfigClients map[string]config_client.IConfigClient
	nacosNamingClients map[string]naming_client.INamingClient
	nacosMu            sync.RWMutex

	// Lifecycle
	started bool
	mu      sync.Mutex
}

// newStorageExtension creates a new storage extension.
func newStorageExtension(
	_ context.Context,
	set extension.Settings,
	config *Config,
) (*Extension, error) {
	return &Extension{
		config:             config,
		settings:           set,
		logger:             set.Logger,
		redisClients:       make(map[string]redis.UniversalClient),
		nacosConfigClients: make(map[string]config_client.IConfigClient),
		nacosNamingClients: make(map[string]naming_client.INamingClient),
	}, nil
}

// Start implements component.Component.
func (e *Extension) Start(ctx context.Context, _ component.Host) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.started {
		return nil
	}

	e.logger.Info("Starting storage extension")

	// Initialize Redis clients
	for name, cfg := range e.config.Redis {
		client, err := e.createRedisClient(cfg)
		if err != nil {
			return fmt.Errorf("failed to create redis client %q: %w", name, err)
		}

		// Test connection
		if err := client.Ping(ctx).Err(); err != nil {
			_ = client.Close()
			return fmt.Errorf("failed to connect to redis %q: %w", name, err)
		}

		e.redisClients[name] = client
		e.logger.Info("Redis client initialized", zap.String("name", name))
	}

	// Initialize Nacos clients
	for name, cfg := range e.config.Nacos {
		configClient, namingClient, err := e.createNacosClients(cfg)
		if err != nil {
			return fmt.Errorf("failed to create nacos client %q: %w", name, err)
		}

		e.nacosConfigClients[name] = configClient
		e.nacosNamingClients[name] = namingClient
		e.logger.Info("Nacos client initialized", zap.String("name", name))
	}

	e.started = true
	e.logger.Info("Storage extension started",
		zap.Int("redis_clients", len(e.redisClients)),
		zap.Int("nacos_clients", len(e.nacosConfigClients)),
	)

	return nil
}

// Shutdown implements component.Component.
func (e *Extension) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.started {
		return nil
	}

	e.logger.Info("Shutting down storage extension")

	var lastErr error

	// Close Redis clients
	e.redisMu.Lock()
	for name, client := range e.redisClients {
		if err := client.Close(); err != nil {
			e.logger.Warn("Failed to close redis client", zap.String("name", name), zap.Error(err))
			lastErr = err
		}
	}
	e.redisClients = make(map[string]redis.UniversalClient)
	e.redisMu.Unlock()

	// Close Nacos clients
	e.nacosMu.Lock()
	for name, client := range e.nacosConfigClients {
		client.CloseClient()
		e.logger.Debug("Nacos config client closed", zap.String("name", name))
	}
	for name, client := range e.nacosNamingClients {
		client.CloseClient()
		e.logger.Debug("Nacos naming client closed", zap.String("name", name))
	}
	e.nacosConfigClients = make(map[string]config_client.IConfigClient)
	e.nacosNamingClients = make(map[string]naming_client.INamingClient)
	e.nacosMu.Unlock()

	e.started = false
	return lastErr
}

// GetRedis implements Storage.
func (e *Extension) GetRedis(name string) (redis.UniversalClient, error) {
	e.redisMu.RLock()
	defer e.redisMu.RUnlock()

	client, ok := e.redisClients[name]
	if !ok {
		return nil, fmt.Errorf("redis client %q not found", name)
	}
	return client, nil
}

// GetDefaultRedis implements Storage.
func (e *Extension) GetDefaultRedis() (redis.UniversalClient, error) {
	return e.GetRedis("default")
}

// GetNacosConfigClient implements Storage.
func (e *Extension) GetNacosConfigClient(name string) (config_client.IConfigClient, error) {
	e.nacosMu.RLock()
	defer e.nacosMu.RUnlock()

	client, ok := e.nacosConfigClients[name]
	if !ok {
		return nil, fmt.Errorf("nacos config client %q not found", name)
	}
	return client, nil
}

// GetDefaultNacosConfigClient implements Storage.
func (e *Extension) GetDefaultNacosConfigClient() (config_client.IConfigClient, error) {
	return e.GetNacosConfigClient("default")
}

// GetNacosNamingClient implements Storage.
func (e *Extension) GetNacosNamingClient(name string) (naming_client.INamingClient, error) {
	e.nacosMu.RLock()
	defer e.nacosMu.RUnlock()

	client, ok := e.nacosNamingClients[name]
	if !ok {
		return nil, fmt.Errorf("nacos naming client %q not found", name)
	}
	return client, nil
}

// GetDefaultNacosNamingClient implements Storage.
func (e *Extension) GetDefaultNacosNamingClient() (naming_client.INamingClient, error) {
	return e.GetNacosNamingClient("default")
}

// HasRedis implements Storage.
func (e *Extension) HasRedis(name string) bool {
	e.redisMu.RLock()
	defer e.redisMu.RUnlock()
	_, ok := e.redisClients[name]
	return ok
}

// HasNacos implements Storage.
func (e *Extension) HasNacos(name string) bool {
	e.nacosMu.RLock()
	defer e.nacosMu.RUnlock()
	_, ok := e.nacosConfigClients[name]
	return ok
}

// ListRedisNames implements Storage.
func (e *Extension) ListRedisNames() []string {
	e.redisMu.RLock()
	defer e.redisMu.RUnlock()

	names := make([]string, 0, len(e.redisClients))
	for name := range e.redisClients {
		names = append(names, name)
	}
	return names
}

// ListNacosNames implements Storage.
func (e *Extension) ListNacosNames() []string {
	e.nacosMu.RLock()
	defer e.nacosMu.RUnlock()

	names := make([]string, 0, len(e.nacosConfigClients))
	for name := range e.nacosConfigClients {
		names = append(names, name)
	}
	return names
}
