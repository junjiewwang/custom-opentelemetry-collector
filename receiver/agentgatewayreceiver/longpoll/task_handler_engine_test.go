// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// ═══════════════════════════════════════════════════
// Mock TaskClaimEngine for testing
// ═══════════════════════════════════════════════════

type mockTaskClaimEngine struct {
	pendingTasks []*model.Task
	claimIndex   int
	cancelledIDs map[string]bool
}

func newMockTaskClaimEngine() *mockTaskClaimEngine {
	return &mockTaskClaimEngine{
		cancelledIDs: make(map[string]bool),
	}
}

func (m *mockTaskClaimEngine) GetPendingTasks(_ context.Context, _ string) ([]*model.Task, error) {
	return m.pendingTasks, nil
}

func (m *mockTaskClaimEngine) ClaimTaskForAgent(_ context.Context, _ string) (*model.Task, error) {
	if m.claimIndex >= len(m.pendingTasks) {
		return nil, nil
	}
	task := m.pendingTasks[m.claimIndex]
	m.claimIndex++
	return task, nil
}

func (m *mockTaskClaimEngine) IsTaskCancelled(_ context.Context, taskID string) (bool, error) {
	return m.cancelledIDs[taskID], nil
}

// ═══════════════════════════════════════════════════
// Tests for TaskPollHandlerEngine
// ═══════════════════════════════════════════════════

func TestTaskPollHandlerEngine_GetType(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockTaskClaimEngine()
	handler := NewTaskPollHandlerEngine(logger, engine)

	assert.Equal(t, LongPollTypeTask, handler.GetType())
}

func TestTaskPollHandlerEngine_StartStop(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockTaskClaimEngine()
	handler := NewTaskPollHandlerEngine(logger, engine)

	require.NoError(t, handler.Start(context.Background()))
	assert.True(t, handler.ShouldContinue())

	require.NoError(t, handler.Stop())
	assert.False(t, handler.ShouldContinue())
}

func TestTaskPollHandlerEngine_CheckImmediate_NoTasks(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockTaskClaimEngine()
	handler := NewTaskPollHandlerEngine(logger, engine)

	require.NoError(t, handler.Start(context.Background()))
	defer handler.Stop()

	req := &PollRequest{AgentID: "agent-1"}
	hasChanges, result, err := handler.CheckImmediate(context.Background(), req)

	require.NoError(t, err)
	assert.False(t, hasChanges)
	assert.Nil(t, result)
}

func TestTaskPollHandlerEngine_CheckImmediate_WithTasks(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockTaskClaimEngine()
	engine.pendingTasks = []*model.Task{
		{ID: "task-1", TypeName: "arthas_attach"},
		{ID: "task-2", TypeName: "arthas_detach"},
	}

	handler := NewTaskPollHandlerEngine(logger, engine)
	require.NoError(t, handler.Start(context.Background()))
	defer handler.Stop()

	req := &PollRequest{AgentID: "agent-1"}
	hasChanges, result, err := handler.CheckImmediate(context.Background(), req)

	require.NoError(t, err)
	assert.True(t, hasChanges)
	require.NotNil(t, result)
	assert.True(t, result.HasChanges)
	assert.NotNil(t, result.Response)
	assert.Equal(t, LongPollTypeTask, result.Response.Type)
	assert.Len(t, result.Response.Tasks, 2)
}

func TestTaskPollHandlerEngine_Poll_Timeout(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockTaskClaimEngine()
	handler := NewTaskPollHandlerEngine(logger, engine)

	require.NoError(t, handler.Start(context.Background()))
	defer handler.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req := &PollRequest{AgentID: "agent-1"}
	result, err := handler.Poll(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.HasChanges)
}

func TestTaskPollHandlerEngine_Poll_ImmediateTask(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockTaskClaimEngine()
	engine.pendingTasks = []*model.Task{
		{ID: "task-imm", TypeName: "arthas_attach"},
	}

	handler := NewTaskPollHandlerEngine(logger, engine)
	require.NoError(t, handler.Start(context.Background()))
	defer handler.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &PollRequest{AgentID: "agent-1"}
	result, err := handler.Poll(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.HasChanges)
	assert.Len(t, result.Response.Tasks, 1)
	assert.Equal(t, "task-imm", result.Response.Tasks[0].ID)
}

func TestTaskPollHandlerEngine_NotifyTaskSubmitted(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockTaskClaimEngine()
	handler := NewTaskPollHandlerEngine(logger, engine)

	require.NoError(t, handler.Start(context.Background()))
	defer handler.Stop()

	// Add a pending task that will be claimed when notified
	engine.pendingTasks = []*model.Task{
		{ID: "task-notify", TypeName: "arthas_attach"},
	}

	// Start a poll in a goroutine
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan *HandlerResult, 1)
	go func() {
		// Reset claim index for the first poll's CheckImmediate
		engine.claimIndex = 0
		engine.pendingTasks = nil // No tasks initially

		result, _ := handler.Poll(ctx, &PollRequest{AgentID: "agent-notify"})
		resultCh <- result
	}()

	// Give the goroutine time to register the waiter
	time.Sleep(50 * time.Millisecond)

	// Now add the task and notify
	engine.pendingTasks = []*model.Task{
		{ID: "task-notify", TypeName: "arthas_attach"},
	}
	engine.claimIndex = 0
	handler.NotifyTaskSubmitted("task-notify", "agent-notify")

	// Wait for result
	select {
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.True(t, result.HasChanges)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for notification result")
	}
}

func TestTaskPollHandlerEngine_WaiterCount(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockTaskClaimEngine()
	handler := NewTaskPollHandlerEngine(logger, engine)

	require.NoError(t, handler.Start(context.Background()))
	defer handler.Stop()

	assert.Equal(t, 0, handler.GetWaiterCount())

	// Start a poll — should register a waiter
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		handler.Poll(ctx, &PollRequest{AgentID: "agent-w"})
		close(done)
	}()

	// Give time for waiter to register
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, handler.GetWaiterCount())

	// Wait for timeout
	<-done
	// After poll returns, waiter is cleaned up
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 0, handler.GetWaiterCount())
}
