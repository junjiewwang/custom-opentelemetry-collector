// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/taskengine"
	"go.opentelemetry.io/collector/custom/taskengine/node"
)

// EngineAdapter adapts a taskengine.Engine to the TaskClaimEngine interface
// used by TaskPollHandlerEngine. This bridges the longpoll handler's needs
// with the unified task engine.
type EngineAdapter struct {
	engine taskengine.Engine
}

// NewEngineAdapter creates a new EngineAdapter wrapping the given engine.
func NewEngineAdapter(engine taskengine.Engine) *EngineAdapter {
	return &EngineAdapter{engine: engine}
}

// Ensure EngineAdapter implements TaskClaimEngine at compile time.
var _ TaskClaimEngine = (*EngineAdapter)(nil)

// GetPendingTasks returns pending tasks available for the given agent.
func (a *EngineAdapter) GetPendingTasks(ctx context.Context, agentID string) ([]*model.Task, error) {
	page, err := a.engine.ListTasks(ctx, taskengine.ListQuery{
		Status: taskengine.StatusPending,
		Limit:  100,
	})
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, nil
	}

	var tasks []*model.Task
	for _, t := range page.Tasks {
		// Include tasks directed at this agent OR broadcast tasks
		if t.Routing.Strategy == taskengine.RoutingDirect && t.Routing.TargetNodeID != agentID {
			continue
		}
		tasks = append(tasks, engineTaskToModelTask(t))
	}
	return tasks, nil
}

// ClaimTaskForAgent atomically claims a pending task for the given agent.
func (a *EngineAdapter) ClaimTaskForAgent(ctx context.Context, agentID string) (*model.Task, error) {
	consumer := &taskengine.ConsumerDescriptor{
		ID:           agentID,
		Roles:        []node.Role{node.RoleAgent},
		Capabilities: node.NewCapabilitySet(node.CapArthasExec),
	}

	task, err := a.engine.Claim(ctx, consumer)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, nil
	}
	return engineTaskToModelTask(task), nil
}

// IsTaskCancelled checks if a task has been cancelled.
func (a *EngineAdapter) IsTaskCancelled(ctx context.Context, taskID string) (bool, error) {
	task, err := a.engine.GetTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	if task == nil {
		return false, nil
	}
	return task.Status == taskengine.StatusCancelled, nil
}

// engineTaskToModelTask converts a taskengine.Task to controlplane model.Task.
// This is a local conversion helper within the longpoll package.
func engineTaskToModelTask(task *taskengine.Task) *model.Task {
	if task == nil {
		return nil
	}

	mt := &model.Task{
		ID:              task.ID,
		TypeName:        engineTypeNameToControlplane(task.Type),
		ParametersJSON:  task.Payload,
		PriorityNum:     task.Priority,
		CreatedAtMillis: task.CreatedAt,
		ExpiresAtMillis: task.ExpiresAt,
	}

	if task.Timeout > 0 {
		mt.TimeoutMillis = task.Timeout.Milliseconds()
	}

	if task.Routing.Strategy == taskengine.RoutingDirect {
		mt.TargetAgentID = task.Routing.TargetNodeID
	}

	return mt
}

// engineTypeNameToControlplane converts engine TaskType to controlplane type name.
// Convention: engine uses "domain:action" (colon), controlplane uses "domain_action" (underscore).
func engineTypeNameToControlplane(taskType taskengine.TaskType) string {
	// Known mappings
	switch taskType {
	case taskengine.TaskTypeArthasAttach:
		return "arthas_attach"
	case taskengine.TaskTypeArthasDetach:
		return "arthas_detach"
	case taskengine.TaskTypeArthasExecSync:
		return "arthas_exec_sync"
	case taskengine.TaskTypeArthasSessionOpen:
		return "arthas_session_open"
	case taskengine.TaskTypeArthasSessionExec:
		return "arthas_session_exec"
	case taskengine.TaskTypeArthasSessionPull:
		return "arthas_session_pull"
	case taskengine.TaskTypeArthasSessionClose:
		return "arthas_session_close"
	default:
		return string(taskType)
	}
}
