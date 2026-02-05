// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"context"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"
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

	registry *clientRegistry
}

// newStorageExtension creates a new storage extension.
func newStorageExtension(
	_ context.Context,
	set extension.Settings,
	config *Config,
) (*Extension, error) {
	ext := &Extension{
		config:   config,
		settings: set,
		logger:   set.Logger,
	}
	ext.registry = newClientRegistry(ext.logger, nil, nil)
	return ext, nil
}

// Start implements component.Component.
func (e *Extension) Start(ctx context.Context, _ component.Host) error {
	return e.registry.Start(ctx, e.config)
}

// Shutdown implements component.Component.
func (e *Extension) Shutdown(ctx context.Context) error {
	return e.registry.Shutdown(ctx)
}

// GetRedis implements Storage.
func (e *Extension) GetRedis(name string) (redis.UniversalClient, error) {
	return e.registry.GetRedis(name)
}

// GetDefaultRedis implements Storage.
func (e *Extension) GetDefaultRedis() (redis.UniversalClient, error) {
	return e.GetRedis("default")
}

// GetNacosConfigClient implements Storage.
func (e *Extension) GetNacosConfigClient(name string) (config_client.IConfigClient, error) {
	return e.registry.GetNacosConfigClient(name)
}

// GetDefaultNacosConfigClient implements Storage.
func (e *Extension) GetDefaultNacosConfigClient() (config_client.IConfigClient, error) {
	return e.GetNacosConfigClient("default")
}

// GetNacosNamingClient implements Storage.
func (e *Extension) GetNacosNamingClient(name string) (naming_client.INamingClient, error) {
	return e.registry.GetNacosNamingClient(name)
}

// GetDefaultNacosNamingClient implements Storage.
func (e *Extension) GetDefaultNacosNamingClient() (naming_client.INamingClient, error) {
	return e.GetNacosNamingClient("default")
}

// HasRedis implements Storage.
func (e *Extension) HasRedis(name string) bool {
	return e.registry.HasRedis(name)
}

// HasNacos implements Storage.
func (e *Extension) HasNacos(name string) bool {
	return e.registry.HasNacos(name)
}

// ListRedisNames implements Storage.
func (e *Extension) ListRedisNames() []string {
	return e.registry.ListRedisNames()
}

// ListNacosNames implements Storage.
func (e *Extension) ListNacosNames() []string {
	return e.registry.ListNacosNames()
}
