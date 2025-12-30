// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/redis/go-redis/v9"
)

// Storage is the interface exposed by the storage extension to other components.
type Storage interface {
	// GetRedis returns a Redis client by name.
	// Returns error if the named connection does not exist.
	GetRedis(name string) (redis.UniversalClient, error)

	// GetDefaultRedis returns the Redis client named "default".
	// Returns error if no "default" connection is configured.
	GetDefaultRedis() (redis.UniversalClient, error)

	// GetNacosConfigClient returns a Nacos config client by name.
	// Returns error if the named connection does not exist.
	GetNacosConfigClient(name string) (config_client.IConfigClient, error)

	// GetDefaultNacosConfigClient returns the Nacos config client named "default".
	// Returns error if no "default" connection is configured.
	GetDefaultNacosConfigClient() (config_client.IConfigClient, error)

	// GetNacosNamingClient returns a Nacos naming client by name.
	// Returns error if the named connection does not exist.
	GetNacosNamingClient(name string) (naming_client.INamingClient, error)

	// GetDefaultNacosNamingClient returns the Nacos naming client named "default".
	// Returns error if no "default" connection is configured.
	GetDefaultNacosNamingClient() (naming_client.INamingClient, error)

	// HasRedis checks if a Redis connection with the given name exists.
	HasRedis(name string) bool

	// HasNacos checks if a Nacos connection with the given name exists.
	HasNacos(name string) bool

	// ListRedisNames returns all configured Redis connection names.
	ListRedisNames() []string

	// ListNacosNames returns all configured Nacos connection names.
	ListNacosNames() []string
}
