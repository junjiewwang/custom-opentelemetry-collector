// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"errors"

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

	if r.controlPlaneExt.GetStorage() == nil {
		r.logger.Debug("Storage not available, skipping long poll manager")
		return nil
	}

	// 1. Create long poll manager
	r.longPollManager = longpoll.NewManager(r.logger, longpoll.DefaultManagerConfig())

	// 2. Initialize and register handlers
	if err := r.initConfigPollHandler(); err != nil {
		r.logger.Warn("Failed to initialize config poll handler", zap.Error(err))
	}

	// 3. Initialize task poll handler (engine-backed)
	taskCfg := r.controlPlaneExt.GetTaskManagerConfig()
	if err := r.initTaskPollHandlerAuto(taskCfg); err != nil {
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

// initTaskPollHandlerAuto initializes the engine-backed task poll handler.
// Since all TaskManager types now route through taskengine.Engine,
// this always uses the engine path via EngineProvider.
func (r *agentGatewayReceiver) initTaskPollHandlerAuto(taskCfg taskmanager.Config) error {
	ep, ok := r.controlPlaneExt.GetTaskManager().(taskmanager.EngineProvider)
	if !ok {
		r.logger.Debug("TaskManager does not implement EngineProvider, skipping task poll handler")
		return nil
	}

	engine := ep.GetEngine()
	if engine == nil {
		r.logger.Warn("EngineProvider returned nil engine, skipping task poll handler")
		return nil
	}

	r.logger.Info("Using engine-backed task poll handler",
		zap.String("task_manager_type", taskCfg.Type))
	return r.initTaskPollHandlerWithEngine(engine)
}

// initTaskPollHandlerWithEngine initializes the engine-backed task poll handler.
func (r *agentGatewayReceiver) initTaskPollHandlerWithEngine(engine taskengine.Engine) error {
	adapter := longpoll.NewEngineAdapter(engine)
	taskHandler := longpoll.NewTaskPollHandlerEngine(r.logger, adapter)

	// Subscribe to task events for real-time long poll notification.
	// If the engine supports TaskEventSubscriber, we bridge submitted events
	// to the handler's NotifyTaskSubmitted, waking waiters immediately.
	if sub, ok := engine.(taskengine.TaskEventSubscriber); ok {
		eventCh, err := sub.SubscribeEvents(context.Background())
		if err != nil {
			r.logger.Warn("Failed to subscribe to task events, falling back to timeout poll",
				zap.Error(err),
			)
		} else {
			r.shutdownWG.Add(1)
			go r.bridgeTaskEvents(eventCh, taskHandler)
			r.logger.Info("Task event bridge started (real-time notification enabled)")
		}
	} else {
		r.logger.Debug("Engine does not support TaskEventSubscriber, using timeout poll only")
	}

	return r.longPollManager.RegisterHandler(taskHandler)
}

// bridgeTaskEvents reads task lifecycle events and routes "submitted" events
// to the long poll handler for real-time waiter wake-up.
func (r *agentGatewayReceiver) bridgeTaskEvents(
	eventCh <-chan taskengine.TaskEvent,
	handler *longpoll.TaskPollHandlerEngine,
) {
	defer r.shutdownWG.Done()
	for event := range eventCh {
		if event.Type == taskengine.EventTaskSubmitted {
			handler.NotifyTaskSubmitted(event.TaskID, event.TargetNodeID)
		}
	}
	r.logger.Debug("Task event bridge stopped")
}
