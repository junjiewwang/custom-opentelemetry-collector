// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager/store"
	"go.opentelemetry.io/collector/custom/taskengine"
)

// RedisClient is a type alias for redis.UniversalClient.
// This allows external packages to reference the Redis client type without importing go-redis directly.
type RedisClient = redis.UniversalClient

// NewTaskManager creates a TaskManager based on the configuration.
// This is the main factory function that should be used to create task managers.
func NewTaskManager(logger *zap.Logger, config Config, redisClient RedisClient) (TaskManager, error) {
	var taskStore store.TaskStore

	switch config.Type {
	case "memory", "":
		taskStore = store.NewMemoryTaskStore(logger.Named("store"), config.ResultTTL)

	case "redis":
		if redisClient == nil {
			return nil, errors.New("redis client is required for redis task manager")
		}
		taskStore = store.NewRedisTaskStore(logger.Named("store"), redisClient, config.KeyPrefix, config.ResultTTL)

	default:
		return nil, errors.New("unsupported task manager type: " + config.Type)
	}

	return NewTaskService(logger.Named("service"), config, taskStore), nil
}

// NewTaskManagerWithEngine creates an engine-backed TaskManager.
// This is the new factory function for Sprint 3 migration, using the unified
// taskengine.Engine as the backend instead of the legacy store.TaskStore.
//
// The returned TaskManager implements the same interface as the legacy
// NewTaskManager, ensuring full backward compatibility for all callers.
func NewTaskManagerWithEngine(logger *zap.Logger, config Config, engine taskengine.Engine) (TaskManager, error) {
	if engine == nil {
		return nil, errors.New("engine is required for engine-backed task manager")
	}
	return NewTaskServiceEngine(engine, logger.Named("service-engine"), config), nil
}
