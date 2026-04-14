// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
)

// RedisClient is a type alias for redis.UniversalClient.
type RedisClient = redis.UniversalClient

func NewInstrumentationManager(logger *zap.Logger, config Config, redisClient RedisClient, agentReg agentregistry.AgentRegistry, taskMgr taskmanager.TaskManager) (InstrumentationManager, error) {
	if agentReg == nil {
		return nil, errors.New("agent registry is required for instrumentation manager")
	}
	if taskMgr == nil {
		return nil, errors.New("task manager is required for instrumentation manager")
	}

	instanceID := newRuntimeSnapshotInstanceID()
	switch config.Type {
	case "", "memory":
		return newInstrumentationServiceWithRuntimeSnapshotStore(
			logger.Named("instrumentation"),
			config,
			NewMemoryRuleStore(),
			agentReg,
			taskMgr,
			newMemoryRuntimeSnapshotStore(),
			instanceID,
		), nil
	case "redis":
		if redisClient == nil {
			return nil, errors.New("redis client is required when instrumentation manager type is redis")
		}
		store := NewRedisRuleStore(logger.Named("instrumentation.store"), redisClient, config.KeyPrefix)
		runtimeSnapshots := newRedisRuntimeSnapshotStore(
			logger.Named("instrumentation.runtime_snapshot"),
			redisClient,
			config.KeyPrefix,
			instanceID,
			runtimeSnapshotSharedSyncIntervalFromConfig(config),
		)
		return newInstrumentationServiceWithRuntimeSnapshotStore(
			logger.Named("instrumentation"),
			config,
			store,
			agentReg,
			taskMgr,
			runtimeSnapshots,
			instanceID,
		), nil
	default:
		return nil, errors.New("unsupported instrumentation manager type: " + config.Type)
	}
}
