// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"errors"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/taskengine"
)

// NewTaskManager creates an engine-backed TaskManager.
// This is the unified factory function using taskengine.Engine as the backend.
//
// For engine creation logic, see ComponentFactory.createTaskEngine() in component_factory.go.
func NewTaskManager(logger *zap.Logger, config Config, engine taskengine.Engine) (TaskManager, error) {
	if engine == nil {
		return nil, errors.New("engine is required for task manager")
	}
	return NewTaskServiceEngine(engine, logger.Named("service-engine"), config), nil
}

// NewTaskManagerWithEngine is an alias for NewTaskManager.
// Kept for backward compatibility with callers that explicitly used this name.
func NewTaskManagerWithEngine(logger *zap.Logger, config Config, engine taskengine.Engine) (TaskManager, error) {
	return NewTaskManager(logger, config, engine)
}
