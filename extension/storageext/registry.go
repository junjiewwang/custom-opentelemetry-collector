// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"context"
	"fmt"
	"sync"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/storageext/blobstore"
)

type redisClientProvider interface {
	Create(cfg RedisConfig) (redis.UniversalClient, error)
}

type nacosClientProvider interface {
	Create(cfg NacosConfig) (config_client.IConfigClient, naming_client.INamingClient, error)
}

type clientRegistry struct {
	logger *zap.Logger

	redisProvider redisClientProvider
	nacosProvider nacosClientProvider

	redisMu      sync.RWMutex
	redisClients map[string]redis.UniversalClient

	nacosMu            sync.RWMutex
	nacosConfigClients map[string]config_client.IConfigClient
	nacosNamingClients map[string]naming_client.INamingClient

	blobMu    sync.RWMutex
	blobStore blobstore.BlobStore

	mu      sync.Mutex
	started bool
}

func newClientRegistry(logger *zap.Logger, redisProvider redisClientProvider, nacosProvider nacosClientProvider) *clientRegistry {
	if logger == nil {
		logger = zap.NewNop()
	}
	if redisProvider == nil {
		redisProvider = defaultRedisProvider{}
	}
	if nacosProvider == nil {
		nacosProvider = defaultNacosProvider{}
	}

	return &clientRegistry{
		logger:             logger,
		redisProvider:      redisProvider,
		nacosProvider:      nacosProvider,
		redisClients:       make(map[string]redis.UniversalClient),
		nacosConfigClients: make(map[string]config_client.IConfigClient),
		nacosNamingClients: make(map[string]naming_client.INamingClient),
	}
}

func (r *clientRegistry) Start(ctx context.Context, cfg *Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return nil
	}

	r.logger.Info("Starting storage extension")

	// Build Redis clients
	for name, rc := range cfg.Redis {
		client, err := r.redisProvider.Create(rc)
		if err != nil {
			_ = r.shutdownLocked(ctx)
			return fmt.Errorf("failed to create redis client %q: %w", name, err)
		}

		// Test connection
		if err := client.Ping(ctx).Err(); err != nil {
			_ = client.Close()
			_ = r.shutdownLocked(ctx)
			return fmt.Errorf("failed to connect to redis %q: %w", name, err)
		}

		r.redisMu.Lock()
		r.redisClients[name] = client
		r.redisMu.Unlock()
		r.logger.Info("Redis client initialized", zap.String("name", name))
	}

	// Build Nacos clients
	for name, nc := range cfg.Nacos {
		configClient, namingClient, err := r.nacosProvider.Create(nc)
		if err != nil {
			_ = r.shutdownLocked(ctx)
			return fmt.Errorf("failed to create nacos client %q: %w", name, err)
		}

		r.nacosMu.Lock()
		r.nacosConfigClients[name] = configClient
		r.nacosNamingClients[name] = namingClient
		r.nacosMu.Unlock()
		r.logger.Info("Nacos client initialized", zap.String("name", name))
	}

	// Build BlobStore
	bs, err := blobstore.NewBlobStore(r.logger, cfg.BlobStore)
	if err != nil {
		_ = r.shutdownLocked(ctx)
		return fmt.Errorf("failed to create blob store: %w", err)
	}
	r.blobMu.Lock()
	r.blobStore = bs
	r.blobMu.Unlock()
	if cfg.BlobStore.Type != "" && cfg.BlobStore.Type != "noop" {
		r.logger.Info("Blob store initialized", zap.String("type", cfg.BlobStore.Type))
	}

	r.started = true
	r.logger.Info("Storage extension started",
		zap.Int("redis_clients", r.redisCount()),
		zap.Int("nacos_clients", r.nacosCount()),
	)

	return nil
}

func (r *clientRegistry) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.started {
		return nil
	}

	r.logger.Info("Shutting down storage extension")
	return r.shutdownLocked(ctx)
}

func (r *clientRegistry) shutdownLocked(_ context.Context) error {
	var lastErr error

	r.redisMu.Lock()
	for name, client := range r.redisClients {
		if err := client.Close(); err != nil {
			r.logger.Warn("Failed to close redis client", zap.String("name", name), zap.Error(err))
			lastErr = err
		}
	}
	r.redisClients = make(map[string]redis.UniversalClient)
	r.redisMu.Unlock()

	r.nacosMu.Lock()
	for name, client := range r.nacosConfigClients {
		client.CloseClient()
		r.logger.Debug("Nacos config client closed", zap.String("name", name))
	}
	for name, client := range r.nacosNamingClients {
		client.CloseClient()
		r.logger.Debug("Nacos naming client closed", zap.String("name", name))
	}
	r.nacosConfigClients = make(map[string]config_client.IConfigClient)
	r.nacosNamingClients = make(map[string]naming_client.INamingClient)
	r.nacosMu.Unlock()

	// Close BlobStore
	r.blobMu.Lock()
	if r.blobStore != nil {
		if err := r.blobStore.Close(); err != nil {
			r.logger.Warn("Failed to close blob store", zap.Error(err))
			lastErr = err
		}
		r.blobStore = nil
	}
	r.blobMu.Unlock()

	r.started = false
	return lastErr
}

func (r *clientRegistry) GetRedis(name string) (redis.UniversalClient, error) {
	r.redisMu.RLock()
	defer r.redisMu.RUnlock()

	client, ok := r.redisClients[name]
	if !ok {
		return nil, fmt.Errorf("redis client %q not found", name)
	}
	return client, nil
}

func (r *clientRegistry) GetNacosConfigClient(name string) (config_client.IConfigClient, error) {
	r.nacosMu.RLock()
	defer r.nacosMu.RUnlock()

	client, ok := r.nacosConfigClients[name]
	if !ok {
		return nil, fmt.Errorf("nacos config client %q not found", name)
	}
	return client, nil
}

func (r *clientRegistry) GetNacosNamingClient(name string) (naming_client.INamingClient, error) {
	r.nacosMu.RLock()
	defer r.nacosMu.RUnlock()

	client, ok := r.nacosNamingClients[name]
	if !ok {
		return nil, fmt.Errorf("nacos naming client %q not found", name)
	}
	return client, nil
}

func (r *clientRegistry) HasRedis(name string) bool {
	r.redisMu.RLock()
	defer r.redisMu.RUnlock()
	_, ok := r.redisClients[name]
	return ok
}

func (r *clientRegistry) HasNacos(name string) bool {
	r.nacosMu.RLock()
	defer r.nacosMu.RUnlock()
	_, ok := r.nacosConfigClients[name]
	return ok
}

func (r *clientRegistry) ListRedisNames() []string {
	r.redisMu.RLock()
	defer r.redisMu.RUnlock()

	names := make([]string, 0, len(r.redisClients))
	for name := range r.redisClients {
		names = append(names, name)
	}
	return names
}

func (r *clientRegistry) ListNacosNames() []string {
	r.nacosMu.RLock()
	defer r.nacosMu.RUnlock()

	names := make([]string, 0, len(r.nacosConfigClients))
	for name := range r.nacosConfigClients {
		names = append(names, name)
	}
	return names
}

func (r *clientRegistry) redisCount() int {
	r.redisMu.RLock()
	defer r.redisMu.RUnlock()
	return len(r.redisClients)
}

func (r *clientRegistry) nacosCount() int {
	r.nacosMu.RLock()
	defer r.nacosMu.RUnlock()
	return len(r.nacosConfigClients)
}

func (r *clientRegistry) GetBlobStore() blobstore.BlobStore {
	r.blobMu.RLock()
	defer r.blobMu.RUnlock()
	if r.blobStore != nil {
		return r.blobStore
	}
	return blobstore.NewNoopBlobStore()
}

func (r *clientRegistry) HasBlobStore() bool {
	r.blobMu.RLock()
	defer r.blobMu.RUnlock()
	if r.blobStore == nil {
		return false
	}
	_, isNoop := r.blobStore.(*blobstore.NoopBlobStore)
	return !isNoop
}
