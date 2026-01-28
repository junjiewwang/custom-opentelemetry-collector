// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/receiver/agentgatewayreceiver/longpoll"
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
	if storage.HasNacos("default") {
		if err := r.initConfigPollHandler(storage); err != nil {
			r.logger.Warn("Failed to initialize config poll handler", zap.Error(err))
		}
	}

	taskCfg := r.controlPlaneExt.GetTaskManagerConfig()
	if taskCfg.Type == "redis" {
		if err := r.initTaskPollHandler(storage, taskCfg); err != nil {
			r.logger.Warn("Failed to initialize task poll handler", zap.Error(err))
		}
	}

	// 3. Start the manager
	if err := r.longPollManager.Start(ctx); err != nil {
		return err
	}

	r.logger.Info("Long poll manager initialized",
		zap.Int("handlers", len(r.longPollManager.GetRegisteredTypes())))

	return nil
}

// initConfigPollHandler initializes and registers the config poll handler.
func (r *agentGatewayReceiver) initConfigPollHandler(storage interface {
	GetDefaultNacosConfigClient() (config_client.IConfigClient, error)
}) error {
	nacosClient, err := storage.GetDefaultNacosConfigClient()
	if err != nil {
		return err
	}

	configHandler := longpoll.NewConfigPollHandler(r.logger, nacosClient)

	// Register metadata providers
	configHandler.RegisterMetadataProvider(&gatewayMetadataProvider{config: r.config})

	return r.longPollManager.RegisterHandler(configHandler)
}

// initTaskPollHandler initializes and registers the task poll handler.
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
