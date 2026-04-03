// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

func TestValidateStateTransition(t *testing.T) {
	tests := []struct {
		name       string
		current    model.TaskStatus
		next       model.TaskStatus
		allowed    bool
		idempotent bool
		conflict   bool
	}{
		{
			name:       "same status is idempotent",
			current:    model.TaskStatusRunning,
			next:       model.TaskStatusRunning,
			allowed:    true,
			idempotent: true,
		},
		{
			name:    "pending to running is allowed",
			current: model.TaskStatusPending,
			next:    model.TaskStatusRunning,
			allowed: true,
		},
		{
			name:    "running to pending is rejected",
			current: model.TaskStatusRunning,
			next:    model.TaskStatusPending,
			allowed: false,
		},
		{
			name:     "terminal conflict is rejected",
			current:  model.TaskStatusSuccess,
			next:     model.TaskStatusFailed,
			allowed:  false,
			conflict: true,
		},
		{
			name:    "terminal to non terminal is rejected",
			current: model.TaskStatusTimeout,
			next:    model.TaskStatusRunning,
			allowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ValidateStateTransition(tt.current, tt.next)
			assert.Equal(t, tt.allowed, res.Allowed)
			assert.Equal(t, tt.idempotent, res.Idempotent)
			assert.Equal(t, tt.conflict, res.Conflict)
		})
	}
}

func TestApplyTaskResultUpdate(t *testing.T) {
	nowMillis := int64(123456)
	info := &TaskInfo{
		Task:   &model.Task{ID: "task-1"},
		Status: model.TaskStatusPending,
	}
	result := &model.TaskResult{
		TaskID:  "task-1",
		Status:  model.TaskStatusRunning,
		AgentID: "agent-1",
	}

	res, err := applyTaskResultUpdate(info, result, nowMillis)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskUpdated, res.Code)
	assert.Equal(t, model.TaskStatusRunning, info.Status)
	assert.Equal(t, "agent-1", info.AgentID)
	assert.Equal(t, nowMillis, info.StartedAtMillis)
	assert.Equal(t, nowMillis, info.LastUpdatedAtMillis)
	assert.Equal(t, int64(1), info.Version)
	assert.Same(t, result, info.Result)
}

func TestApplyTaskResultUpdate_TerminalIsNoop(t *testing.T) {
	info := &TaskInfo{
		Task:    &model.Task{ID: "task-1"},
		Status:  model.TaskStatusSuccess,
		AgentID: "agent-1",
	}
	result := &model.TaskResult{
		TaskID: "task-1",
		Status: model.TaskStatusFailed,
	}

	res, err := applyTaskResultUpdate(info, result, 123)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskNoop, res.Code)
	assert.Equal(t, model.TaskStatusSuccess, info.Status)
	assert.Nil(t, info.Result)
}

func TestApplyCancelUpdate(t *testing.T) {
	info := &TaskInfo{
		Task:   &model.Task{ID: "task-1"},
		Status: model.TaskStatusPending,
	}

	res, err := applyCancelUpdate(info, 456)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskUpdated, res.Code)
	assert.Equal(t, model.TaskStatusCancelled, info.Status)
	assert.Equal(t, int64(456), info.LastUpdatedAtMillis)
	assert.Equal(t, int64(1), info.Version)
}

func TestApplyCancelUpdate_RejectsTerminal(t *testing.T) {
	info := &TaskInfo{
		Task:   &model.Task{ID: "task-1"},
		Status: model.TaskStatusSuccess,
	}

	res, err := applyCancelUpdate(info, 456)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskRejected, res.Code)
	assert.Equal(t, model.TaskStatusSuccess, info.Status)
}

func TestApplySetRunningUpdate(t *testing.T) {
	info := &TaskInfo{
		Task:   &model.Task{ID: "task-1"},
		Status: model.TaskStatusPending,
	}

	res, err := applySetRunningUpdate(info, "agent-9", 789)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskUpdated, res.Code)
	assert.Equal(t, model.TaskStatusRunning, info.Status)
	assert.Equal(t, "agent-9", info.AgentID)
	assert.Equal(t, int64(789), info.StartedAtMillis)
	assert.Equal(t, int64(789), info.LastUpdatedAtMillis)
	assert.Equal(t, int64(1), info.Version)
}

func TestApplySetRunningUpdate_RejectsTerminal(t *testing.T) {
	info := &TaskInfo{
		Task:   &model.Task{ID: "task-1"},
		Status: model.TaskStatusCancelled,
	}

	res, err := applySetRunningUpdate(info, "agent-9", 789)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskRejected, res.Code)
	assert.Equal(t, model.TaskStatusCancelled, info.Status)
}
