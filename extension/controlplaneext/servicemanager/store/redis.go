// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Redis key patterns for ServiceManager.
// Main storage: Hash per app  — otel:services:{appID}  (field=serviceName, value=ServiceInfo JSON)
// ID index:     Global Hash   — otel:services:_id_index (field=serviceID, value="appID:serviceName")
const (
	keyServiceApp   = "%s:%s"        // {keyPrefix}:{appID} — Hash: serviceName -> JSON
	keyServiceIndex = "%s:_id_index" // {keyPrefix}:_id_index — Hash: serviceID -> "appID:serviceName"
)

// createIfAbsentScript atomically creates a service record if it does not exist.
// KEYS[1] = app hash key (e.g., otel:services:{appID})
// KEYS[2] = ID index key (e.g., otel:services:_id_index)
// ARGV[1] = serviceName (hash field)
// ARGV[2] = serviceID
// ARGV[3] = serviceInfo JSON
// ARGV[4] = "appID:serviceName" (index value)
// Returns:
//
//	1 = created successfully
//	0 = already exists (returns existing JSON as second value)
var createIfAbsentScript = redis.NewScript(`
local existing = redis.call('HGET', KEYS[1], ARGV[1])
if existing then
    return {0, existing}
end

redis.call('HSET', KEYS[1], ARGV[1], ARGV[3])
redis.call('HSET', KEYS[2], ARGV[2], ARGV[4])
return {1, ARGV[3]}
`)

// RedisServiceStore implements ServiceStore using Redis as backend.
// Uses Hash-per-app storage with a global ID index, both maintained atomically via Lua scripts.
type RedisServiceStore struct {
	logger    *zap.Logger
	client    redis.UniversalClient
	keyPrefix string

	mu      sync.RWMutex
	started bool
}

// NewRedisServiceStore creates a new Redis-based service store.
func NewRedisServiceStore(logger *zap.Logger, client redis.UniversalClient, keyPrefix string) *RedisServiceStore {
	if keyPrefix == "" {
		keyPrefix = "otel:services"
	}
	return &RedisServiceStore{
		logger:    logger,
		client:    client,
		keyPrefix: keyPrefix,
	}
}

// Ensure RedisServiceStore implements ServiceStore.
var _ ServiceStore = (*RedisServiceStore)(nil)

// ===== Key Helpers =====

func (s *RedisServiceStore) appKey(appID string) string {
	return fmt.Sprintf(keyServiceApp, s.keyPrefix, appID)
}

func (s *RedisServiceStore) idIndexKey() string {
	return fmt.Sprintf(keyServiceIndex, s.keyPrefix)
}

func (s *RedisServiceStore) getClient() (redis.UniversalClient, error) {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}
	return client, nil
}

// ===== CRUD Operations =====

// CreateIfAbsent implements ServiceStore with atomic Lua script.
func (s *RedisServiceStore) CreateIfAbsent(ctx context.Context, svc *ServiceInfo) (bool, *ServiceInfo, error) {
	if svc == nil {
		return false, nil, errors.New("service info cannot be nil")
	}

	client, err := s.getClient()
	if err != nil {
		return false, nil, err
	}

	data, err := json.Marshal(svc)
	if err != nil {
		return false, nil, fmt.Errorf("marshal service info: %w", err)
	}

	appHashKey := s.appKey(svc.AppID)
	idIdxKey := s.idIndexKey()
	indexValue := svc.AppID + ":" + svc.ServiceName

	result, err := createIfAbsentScript.Run(ctx, client,
		[]string{appHashKey, idIdxKey},
		svc.ServiceName, svc.ID, string(data), indexValue,
	).Slice()
	if err != nil {
		return false, nil, fmt.Errorf("execute createIfAbsent script: %w", err)
	}

	if len(result) < 2 {
		return false, nil, fmt.Errorf("unexpected script result length: %d", len(result))
	}

	code, ok := result[0].(int64)
	if !ok {
		return false, nil, fmt.Errorf("unexpected script result code type: %T", result[0])
	}

	jsonStr, ok := result[1].(string)
	if !ok {
		return false, nil, fmt.Errorf("unexpected script result data type: %T", result[1])
	}

	var info ServiceInfo
	if err := json.Unmarshal([]byte(jsonStr), &info); err != nil {
		return false, nil, fmt.Errorf("unmarshal service info: %w", err)
	}

	return code == 1, &info, nil
}

// Get implements ServiceStore.
func (s *RedisServiceStore) Get(ctx context.Context, appID, serviceName string) (*ServiceInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	data, err := client.HGet(ctx, s.appKey(appID), serviceName).Result()
	if err == redis.Nil {
		return nil, ServiceNotFound(appID, serviceName)
	}
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}

	var info ServiceInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, fmt.Errorf("unmarshal service info: %w", err)
	}
	return &info, nil
}

// GetByID implements ServiceStore.
func (s *RedisServiceStore) GetByID(ctx context.Context, serviceID string) (*ServiceInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	// Step 1: Look up the composite key from the ID index
	indexValue, err := client.HGet(ctx, s.idIndexKey(), serviceID).Result()
	if err == redis.Nil {
		return nil, ServiceNotFoundByID(serviceID)
	}
	if err != nil {
		return nil, fmt.Errorf("get service ID index: %w", err)
	}

	// Step 2: Parse "appID:serviceName"
	parts := strings.SplitN(indexValue, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid ID index value: %s", indexValue)
	}
	appID, serviceName := parts[0], parts[1]

	// Step 3: Fetch the actual record
	info, err := s.Get(ctx, appID, serviceName)
	if err != nil {
		// Stale index entry — the record was deleted but the index wasn't cleaned.
		// Best-effort cleanup.
		if errors.Is(err, ErrServiceNotFound) {
			_ = client.HDel(ctx, s.idIndexKey(), serviceID)
			return nil, ServiceNotFoundByID(serviceID)
		}
		return nil, err
	}

	return info, nil
}

// Update implements ServiceStore.
func (s *RedisServiceStore) Update(ctx context.Context, svc *ServiceInfo) error {
	if svc == nil {
		return errors.New("service info cannot be nil")
	}

	client, err := s.getClient()
	if err != nil {
		return err
	}

	// Check existence first
	exists, err := client.HExists(ctx, s.appKey(svc.AppID), svc.ServiceName).Result()
	if err != nil {
		return fmt.Errorf("check service existence: %w", err)
	}
	if !exists {
		return ServiceNotFound(svc.AppID, svc.ServiceName)
	}

	data, err := json.Marshal(svc)
	if err != nil {
		return fmt.Errorf("marshal service info: %w", err)
	}

	return client.HSet(ctx, s.appKey(svc.AppID), svc.ServiceName, string(data)).Err()
}

// Delete implements ServiceStore.
func (s *RedisServiceStore) Delete(ctx context.Context, appID, serviceName string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	// Step 1: Get the existing record to find the serviceID for index cleanup
	data, err := client.HGet(ctx, s.appKey(appID), serviceName).Result()
	if err == redis.Nil {
		return ServiceNotFound(appID, serviceName)
	}
	if err != nil {
		return fmt.Errorf("get service for delete: %w", err)
	}

	var info ServiceInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return fmt.Errorf("unmarshal service info for delete: %w", err)
	}

	// Step 2: Atomic delete of both record and ID index
	pipe := client.TxPipeline()
	pipe.HDel(ctx, s.appKey(appID), serviceName)
	if info.ID != "" {
		pipe.HDel(ctx, s.idIndexKey(), info.ID)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// ===== List Operations =====

// ListByApp implements ServiceStore.
func (s *RedisServiceStore) ListByApp(ctx context.Context, appID string, filter ListServiceFilter) ([]*ServiceInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	entries, err := client.HGetAll(ctx, s.appKey(appID)).Result()
	if err != nil {
		return nil, fmt.Errorf("list services by app: %w", err)
	}

	result := make([]*ServiceInfo, 0, len(entries))
	for _, data := range entries {
		var info ServiceInfo
		if err := json.Unmarshal([]byte(data), &info); err != nil {
			s.logger.Warn("Failed to unmarshal service info", zap.String("app_id", appID), zap.Error(err))
			continue
		}
		if filter.NamePattern != "" && !strings.Contains(info.ServiceName, filter.NamePattern) {
			continue
		}
		result = append(result, &info)
	}
	return result, nil
}

// ListAll implements ServiceStore.
// This scans all app hash keys matching the prefix pattern.
func (s *RedisServiceStore) ListAll(ctx context.Context, filter ListServiceFilter) ([]*ServiceInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	// Scan for all app hash keys: {keyPrefix}:* but exclude _id_index
	pattern := s.keyPrefix + ":*"
	idIdxKey := s.idIndexKey()

	result := make([]*ServiceInfo, 0)
	iter := client.Scan(ctx, 0, pattern, 100).Iterator()

	for iter.Next(ctx) {
		key := iter.Val()

		// Skip the ID index key
		if key == idIdxKey {
			continue
		}

		entries, err := client.HGetAll(ctx, key).Result()
		if err != nil {
			s.logger.Warn("Failed to list services from hash", zap.String("key", key), zap.Error(err))
			continue
		}

		for _, data := range entries {
			var info ServiceInfo
			if err := json.Unmarshal([]byte(data), &info); err != nil {
				s.logger.Warn("Failed to unmarshal service info", zap.String("key", key), zap.Error(err))
				continue
			}
			if filter.NamePattern != "" && !strings.Contains(info.ServiceName, filter.NamePattern) {
				continue
			}
			result = append(result, &info)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan service keys: %w", err)
	}

	return result, nil
}

// ===== Lifecycle =====

// Start implements ServiceStore.
func (s *RedisServiceStore) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return nil
	}

	if s.client == nil {
		return errors.New("redis client not provided")
	}

	// Test connection
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	s.logger.Info("Starting Redis service store", zap.String("key_prefix", s.keyPrefix))
	s.started = true
	return nil
}

// Close implements ServiceStore.
func (s *RedisServiceStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return nil
	}

	// Note: We don't close the Redis client here because it's managed externally.
	s.started = false
	return nil
}
