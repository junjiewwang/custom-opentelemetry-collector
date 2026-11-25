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

func NewInstrumentationManager(logger *zap.Logger, config Config, redisClient RedisClient, agentReg agentregistry.AgentRegistry, taskMgr taskmanager.TaskManager, opts ...Option) (InstrumentationManager, error) {
	if agentReg == nil {
		return nil, errors.New("agent registry is required for instrumentation manager")
	}
	if taskMgr == nil {
		return nil, errors.New("task manager is required for instrumentation manager")
	}

	instanceID := newRuntimeSnapshotInstanceID()
	var service *InstrumentationService
	switch config.Type {
	case "", "memory":
		service = newInstrumentationServiceWithRuntimeSnapshotStore(
			logger.Named("instrumentation"),
			config,
			NewMemoryRuleStore(),
			agentReg,
			taskMgr,
			newMemoryRuntimeSnapshotStore(),
			instanceID,
		)
	case "redis":
		if redisClient == nil {
			return nil, errors.New("redis client is required when instrumentation manager type is redis")
		}
		cmd := NewRedisCmd(redisClient.(*redis.Client))
		store := NewRedisRuleStore(logger.Named("instrumentation.store"), cmd, config.KeyPrefix)
		runtimeSnapshots := newRedisRuntimeSnapshotStore(
			logger.Named("instrumentation.runtime_snapshot"),
			cmd,
			config.KeyPrefix,
			instanceID,
			runtimeSnapshotSharedSyncIntervalFromConfig(config),
		)
		service = newInstrumentationServiceWithRuntimeSnapshotStore(
			logger.Named("instrumentation"),
			config,
			store,
			agentReg,
			taskMgr,
			runtimeSnapshots,
			instanceID,
		)
	default:
		return nil, errors.New("unsupported instrumentation manager type: " + config.Type)
	}

	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service, nil
}
