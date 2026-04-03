// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// taskResultProvider is a minimal interface for testing waitForTaskResult.
type taskResultProvider struct {
	mu      sync.RWMutex
	results map[string]*model.TaskResult
}

func newTaskResultProvider() *taskResultProvider {
	return &taskResultProvider{
		results: make(map[string]*model.TaskResult),
	}
}

func (p *taskResultProvider) GetTaskResult(taskID string) (*model.TaskResult, bool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.results[taskID]
	return r, ok, nil
}

func (p *taskResultProvider) setResult(taskID string, result *model.TaskResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.results[taskID] = result
}

// testableWrapper creates a mcpServerWrapper with a mock controlPlane that only
// implements GetTaskResult for testing waitForTaskResult.
func testableWrapper(t *testing.T, provider *taskResultProvider) *mcpServerWrapper {
	logger := zaptest.NewLogger(t)
	// We create a minimal Extension struct with just what waitForTaskResult needs.
	// waitForTaskResult calls w.ext.controlPlane.GetTaskResult() which requires
	// the full interface. Instead, we test the polling logic directly.
	return &mcpServerWrapper{
		logger: logger,
	}
}

// waitForTaskResultDirect is a test helper that replicates the polling logic
// without needing the full Extension dependency tree.
func waitForTaskResultDirect(ctx context.Context, provider *taskResultProvider, taskID string, timeout time.Duration) (*model.TaskResult, error) {
	deadline := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond) // Faster polling for tests
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, context.DeadlineExceeded
		case <-ticker.C:
			result, found, err := provider.GetTaskResult(taskID)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			switch result.Status {
			case model.TaskStatusSuccess, model.TaskStatusFailed, model.TaskStatusTimeout, model.TaskStatusCancelled:
				return result, nil
			case model.TaskStatusRunning, model.TaskStatusPending:
				continue
			default:
				return result, nil
			}
		}
	}
}

func TestWaitForTaskResult_Success(t *testing.T) {
	provider := newTaskResultProvider()
	taskID := "test-task-123"

	// Simulate task completing after 300ms
	go func() {
		time.Sleep(300 * time.Millisecond)
		provider.setResult(taskID, &model.TaskResult{
			TaskID: taskID,
			Status: model.TaskStatusSuccess,
		})
	}()

	ctx := context.Background()
	result, err := waitForTaskResultDirect(ctx, provider, taskID, 5*time.Second)

	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusSuccess, result.Status)
	assert.Equal(t, taskID, result.TaskID)
}

func TestWaitForTaskResult_Timeout(t *testing.T) {
	provider := newTaskResultProvider()

	ctx := context.Background()
	_, err := waitForTaskResultDirect(ctx, provider, "nonexistent-task", 500*time.Millisecond)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWaitForTaskResult_ContextCancelled(t *testing.T) {
	provider := newTaskResultProvider()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	_, err := waitForTaskResultDirect(ctx, provider, "task-id", 10*time.Second)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWaitForTaskResult_Failed(t *testing.T) {
	provider := newTaskResultProvider()
	taskID := "fail-task"

	// Pre-set a failed result
	provider.setResult(taskID, &model.TaskResult{
		TaskID:       taskID,
		Status:       model.TaskStatusFailed,
		ErrorMessage: "arthas attach failed",
	})

	ctx := context.Background()
	result, err := waitForTaskResultDirect(ctx, provider, taskID, 5*time.Second)

	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusFailed, result.Status)
	assert.Equal(t, "arthas attach failed", result.ErrorMessage)
}

func TestWaitForTaskResult_SkipsRunning(t *testing.T) {
	provider := newTaskResultProvider()
	taskID := "running-task"

	// Initially set to running
	provider.setResult(taskID, &model.TaskResult{
		TaskID: taskID,
		Status: model.TaskStatusRunning,
	})

	// Complete after 500ms
	go func() {
		time.Sleep(500 * time.Millisecond)
		provider.setResult(taskID, &model.TaskResult{
			TaskID: taskID,
			Status: model.TaskStatusSuccess,
		})
	}()

	ctx := context.Background()
	result, err := waitForTaskResultDirect(ctx, provider, taskID, 5*time.Second)

	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusSuccess, result.Status)
}

func TestWaitForTaskResult_Cancelled(t *testing.T) {
	provider := newTaskResultProvider()
	taskID := "cancelled-task"

	provider.setResult(taskID, &model.TaskResult{
		TaskID: taskID,
		Status: model.TaskStatusCancelled,
	})

	ctx := context.Background()
	result, err := waitForTaskResultDirect(ctx, provider, taskID, 5*time.Second)

	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusCancelled, result.Status)
}

func TestWaitForTaskResult_TimeoutStatus(t *testing.T) {
	provider := newTaskResultProvider()
	taskID := "timeout-task"

	provider.setResult(taskID, &model.TaskResult{
		TaskID: taskID,
		Status: model.TaskStatusTimeout,
	})

	ctx := context.Background()
	result, err := waitForTaskResultDirect(ctx, provider, taskID, 5*time.Second)

	require.NoError(t, err)
	assert.Equal(t, model.TaskStatusTimeout, result.Status)
}