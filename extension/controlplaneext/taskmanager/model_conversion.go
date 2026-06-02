// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"time"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/taskengine"
	"go.opentelemetry.io/collector/custom/taskengine/node"
)

// ═══════════════════════════════════════════════════
// Model Conversion: controlplane/model ↔ taskengine
// ═══════════════════════════════════════════════════
//
// These functions provide bidirectional mapping between the legacy
// controlplane/model types and the unified taskengine types.
// They are used by TaskServiceEngine (Facade) to bridge the two APIs.

// ─── Task Conversion ───

// controlplaneTaskToEngine converts a controlplane model.Task to a taskengine.Task.
func controlplaneTaskToEngine(task *model.Task) *taskengine.Task {
	if task == nil {
		return nil
	}

	engineTask := &taskengine.Task{
		ID:        task.ID,
		Type:      controlplaneTypeToEngine(task.TypeName),
		Payload:   task.ParametersJSON,
		Priority:  task.PriorityNum,
		CreatedAt: task.CreatedAtMillis,
		ExpiresAt: task.ExpiresAtMillis,
		Routing:   controlplaneRoutingToEngine(task.TargetAgentID),
	}

	// Convert timeout from millis to Duration
	if task.TimeoutMillis > 0 {
		engineTask.Timeout = time.Duration(task.TimeoutMillis) * time.Millisecond
	}

	return engineTask
}

// engineTaskToControlplane converts a taskengine.Task back to a controlplane model.Task.
func engineTaskToControlplane(task *taskengine.Task) *model.Task {
	if task == nil {
		return nil
	}

	modelTask := &model.Task{
		ID:              task.ID,
		TypeName:        engineTypeToControlplane(task.Type),
		ParametersJSON:  task.Payload,
		PriorityNum:     task.Priority,
		CreatedAtMillis: task.CreatedAt,
		ExpiresAtMillis: task.ExpiresAt,
	}

	// Convert timeout from Duration to millis
	if task.Timeout > 0 {
		modelTask.TimeoutMillis = task.Timeout.Milliseconds()
	}

	// Extract target agent ID from routing
	if task.Routing.Strategy == taskengine.RoutingDirect {
		modelTask.TargetAgentID = task.Routing.TargetNodeID
	}

	return modelTask
}

// ─── Task Result Conversion ───

// controlplaneResultToEngine converts a controlplane model.TaskResult to a taskengine.TaskResult.
func controlplaneResultToEngine(result *model.TaskResult) *taskengine.TaskResult {
	if result == nil {
		return nil
	}

	return &taskengine.TaskResult{
		TaskID:      result.TaskID,
		NodeID:      result.AgentID,
		Status:      controlplaneStatusToEngine(result.Status),
		Output:      result.ResultJSON,
		Error:       buildErrorMessage(result.ErrorCode, result.ErrorMessage),
		StartedAt:   result.StartedAtMillis,
		CompletedAt: result.CompletedAtMillis,
		RetryCount:  int(result.RetryCount),
	}
}

// engineResultToControlplane converts a taskengine.TaskResult back to a controlplane model.TaskResult.
func engineResultToControlplane(result *taskengine.TaskResult) *model.TaskResult {
	if result == nil {
		return nil
	}

	return &model.TaskResult{
		TaskID:            result.TaskID,
		AgentID:           result.NodeID,
		Status:            engineStatusToControlplane(result.Status),
		ErrorMessage:      result.Error,
		ResultJSON:        result.Output,
		StartedAtMillis:   result.StartedAt,
		CompletedAtMillis: result.CompletedAt,
		RetryCount:        int32(result.RetryCount),
	}
}

// ─── Status Conversion ───

// controlplaneStatusToEngine maps model.TaskStatus (int32) → taskengine.TaskStatus (string).
func controlplaneStatusToEngine(status model.TaskStatus) taskengine.TaskStatus {
	switch status {
	case model.TaskStatusPending:
		return taskengine.StatusPending
	case model.TaskStatusRunning:
		return taskengine.StatusRunning
	case model.TaskStatusSuccess:
		return taskengine.StatusSuccess
	case model.TaskStatusFailed:
		return taskengine.StatusFailed
	case model.TaskStatusTimeout:
		return taskengine.StatusTimeout
	case model.TaskStatusCancelled:
		return taskengine.StatusCancelled
	case model.TaskStatusResultTooLarge:
		// ResultTooLarge maps to Failed in the unified engine (no direct equivalent)
		return taskengine.StatusFailed
	default:
		return taskengine.StatusPending
	}
}

// engineStatusToControlplane maps taskengine.TaskStatus (string) → model.TaskStatus (int32).
func engineStatusToControlplane(status taskengine.TaskStatus) model.TaskStatus {
	switch status {
	case taskengine.StatusPending:
		return model.TaskStatusPending
	case taskengine.StatusRunning:
		return model.TaskStatusRunning
	case taskengine.StatusSuccess:
		return model.TaskStatusSuccess
	case taskengine.StatusFailed:
		return model.TaskStatusFailed
	case taskengine.StatusTimeout:
		return model.TaskStatusTimeout
	case taskengine.StatusCancelled:
		return model.TaskStatusCancelled
	case taskengine.StatusSkipped:
		// Skipped maps to Success in the controlplane model (no direct equivalent)
		return model.TaskStatusSuccess
	default:
		return model.TaskStatusUnspecified
	}
}

// controlplaneStatusesToEngine converts a slice of model.TaskStatus to engine statuses.
// Used for list/filter queries.
func controlplaneStatusesToEngine(statuses []model.TaskStatus) []taskengine.TaskStatus {
	if len(statuses) == 0 {
		return nil
	}
	result := make([]taskengine.TaskStatus, 0, len(statuses))
	for _, s := range statuses {
		result = append(result, controlplaneStatusToEngine(s))
	}
	return result
}

// ─── Task Type Conversion ───

// taskTypeMapping maps controlplane TypeName → engine TaskType.
// Convention: controlplane uses underscore (arthas_attach), engine uses colon (arthas:attach).
var taskTypeToEngineMap = map[string]taskengine.TaskType{
	"arthas_attach":        taskengine.TaskTypeArthasAttach,
	"arthas_detach":        taskengine.TaskTypeArthasDetach,
	"arthas_exec_sync":     taskengine.TaskTypeArthasExecSync,
	"arthas_session_open":  taskengine.TaskTypeArthasSessionOpen,
	"arthas_session_exec":  taskengine.TaskTypeArthasSessionExec,
	"arthas_session_pull":  taskengine.TaskTypeArthasSessionPull,
	"arthas_session_close": taskengine.TaskTypeArthasSessionClose,
}

// engineTypeToControlplaneMap is the reverse mapping.
var engineTypeToControlplaneMap map[taskengine.TaskType]string

func init() {
	engineTypeToControlplaneMap = make(map[taskengine.TaskType]string, len(taskTypeToEngineMap))
	for k, v := range taskTypeToEngineMap {
		engineTypeToControlplaneMap[v] = k
	}
}

// controlplaneTypeToEngine maps a controlplane task type name to an engine TaskType.
// Falls back to wrapping the type name as-is if not in the known mapping.
func controlplaneTypeToEngine(typeName string) taskengine.TaskType {
	if mapped, ok := taskTypeToEngineMap[typeName]; ok {
		return mapped
	}
	// Fallback: use the original name as TaskType (supports custom/unknown types)
	return taskengine.TaskType(typeName)
}

// engineTypeToControlplane maps an engine TaskType back to a controlplane type name.
func engineTypeToControlplane(taskType taskengine.TaskType) string {
	if mapped, ok := engineTypeToControlplaneMap[taskType]; ok {
		return mapped
	}
	// Fallback: use string representation as-is
	return string(taskType)
}

// ─── Routing Conversion ───

// controlplaneRoutingToEngine derives engine TaskRouting from the TargetAgentID field.
// - Non-empty agentID → RoutingDirect (routes to specific node)
// - Empty agentID    → RoutingBroadcast (goes to global queue)
func controlplaneRoutingToEngine(targetAgentID string) taskengine.TaskRouting {
	if targetAgentID != "" {
		return taskengine.TaskRouting{
			Strategy:     taskengine.RoutingDirect,
			TargetNodeID: targetAgentID,
		}
	}
	return taskengine.TaskRouting{
		Strategy: taskengine.RoutingBroadcast,
	}
}

// ─── Consumer Descriptor ───

// agentToConsumerDescriptor creates a ConsumerDescriptor for a remote agent.
// Remote agents have RoleAgent and CapArthasExec capability.
func agentToConsumerDescriptor(agentID string) *taskengine.ConsumerDescriptor {
	return &taskengine.ConsumerDescriptor{
		ID:           agentID,
		Roles:        []node.Role{node.RoleAgent},
		Capabilities: node.NewCapabilitySet(node.CapArthasExec),
	}
}

// ─── Helper Functions ───

// buildErrorMessage combines error code and message into a single string.
func buildErrorMessage(code, message string) string {
	if code == "" {
		return message
	}
	if message == "" {
		return code
	}
	return code + ": " + message
}
