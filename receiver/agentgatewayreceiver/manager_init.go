// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/receiver/agentgatewayreceiver/longpoll"
	"go.opentelemetry.io/collector/custom/taskengine"
)

// initLongPollManager initializes the long poll manager and its handlers.
func (r *agentGatewayReceiver) initLongPollManager(ctx context.Context) error {
	if r.controlPlaneExt == nil {
		r.logger.Debug("Control plane extension not available, skipping long poll manager")
		return nil
	}

	storage := r.controlPlaneExt.GetStorage()
	if storage == nil {
		r.logger.Debug("Storage not available, skipping long poll manager")
		return nil
	}

	// 1. Create long poll manager
	r.longPollManager = longpoll.NewManager(r.logger, longpoll.DefaultManagerConfig())

	// 2. Initialize and register handlers
	if err := r.initConfigPollHandler(); err != nil {
		r.logger.Warn("Failed to initialize config poll handler", zap.Error(err))
	}

	// 3. Initialize task poll handler based on backend type
	taskCfg := r.controlPlaneExt.GetTaskManagerConfig()
	if err := r.initTaskPollHandlerAuto(storage, taskCfg); err != nil {
		r.logger.Warn("Failed to initialize task poll handler", zap.Error(err))
	}

	// 4. Start the manager
	if err := r.longPollManager.Start(ctx); err != nil {
		return err
	}

	r.logger.Info("Long poll manager initialized",
		zap.Int("handlers", len(r.longPollManager.GetRegisteredTypes())))

	return nil
}

// initConfigPollHandler initializes and registers the config poll handler.
func (r *agentGatewayReceiver) initConfigPollHandler() error {
	// Get OnDemandConfigManager (required — no fallback to direct Nacos)
	if r.controlPlaneExt == nil {
		return errors.New("control plane extension not available")
	}
	cfgMgr := r.controlPlaneExt.GetOnDemandConfigManager()
	if cfgMgr == nil {
		return errors.New("OnDemandConfigManager not available")
	}

	configHandler := longpoll.NewConfigPollHandler(r.logger, cfgMgr)

	// Register metadata providers
	configHandler.RegisterMetadataProvider(&gatewayMetadataProvider{config: r.config})

	return r.longPollManager.RegisterHandler(configHandler)
}

// initTaskPollHandlerAuto automatically selects the best task poll handler based on
// the TaskManager backend type. If the TaskManager implements EngineProvider,
// the engine-backed handler is used; otherwise falls back to legacy Redis handler.
func (r *agentGatewayReceiver) initTaskPollHandlerAuto(storage interface {
	GetRedis(name string) (redis.UniversalClient, error)
}, taskCfg taskmanager.Config) error {
	// Try engine-backed path first (preferred)
	if ep, ok := r.controlPlaneExt.GetTaskManager().(taskmanager.EngineProvider); ok {
		engine := ep.GetEngine()
		if engine != nil {
			r.logger.Info("Using engine-backed task poll handler",
				zap.String("task_manager_type", taskCfg.Type))
			return r.initTaskPollHandlerWithEngine(engine)
		}
	}

	// Fallback to legacy Redis handler
	if taskCfg.Type == "redis" || taskCfg.Type == "engine" {
		r.logger.Info("Using legacy Redis-based task poll handler",
			zap.String("task_manager_type", taskCfg.Type))
		return r.initTaskPollHandler(storage, taskCfg)
	}

	r.logger.Debug("Task poll handler not initialized (type not supported for longpoll)",
		zap.String("task_manager_type", taskCfg.Type))
	return nil
}

// initTaskPollHandler initializes and registers the task poll handler.
// Uses the legacy Redis-based handler when engine is not available.
func (r *agentGatewayReceiver) initTaskPollHandler(storage interface {
	GetRedis(name string) (redis.UniversalClient, error)
}, taskCfg taskmanager.Config) error {
	redisName := taskCfg.RedisName
	if redisName == "" {
		redisName = "default"
	}
	keyPrefix := taskCfg.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = "otel:tasks"
	}

	redisClient, err := storage.GetRedis(redisName)
	if err != nil {
		return err
	}

	taskHandler := longpoll.NewTaskPollHandler(r.logger, redisClient, keyPrefix)
	return r.longPollManager.RegisterHandler(taskHandler)
}

// initTaskPollHandlerWithEngine initializes the engine-backed task poll handler.
// This is the Sprint 3 path that replaces direct Redis operations with the unified engine.
func (r *agentGatewayReceiver) initTaskPollHandlerWithEngine(engine taskengine.Engine) error {
	adapter := longpoll.NewEngineAdapter(engine)
	taskHandler := longpoll.NewTaskPollHandlerEngine(r.logger, adapter)
	return r.longPollManager.RegisterHandler(taskHandler)
}
