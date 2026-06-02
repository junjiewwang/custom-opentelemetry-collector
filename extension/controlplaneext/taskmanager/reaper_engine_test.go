// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.opentelemetry.io/collector/custom/taskengine"
)

func TestStaleTaskReaperEngine_Disabled(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockEngine()

	config := StaleTaskReaperConfig{Enabled: false}
	reaper := NewStaleTaskReaperEngine(logger, config, engine)

	require.NoError(t, reaper.Start(context.Background()))
	// Should immediately close doneChan without running background goroutine
	reaper.Stop()
}

func TestStaleTaskReaperEngine_ReapsStaleTask(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockEngine()

	// Add a stale RUNNING task (created long ago)
	staleTask := &taskengine.Task{
		ID:        "stale-task-1",
		Type:      taskengine.TaskTypeArthasAttach,
		Status:    taskengine.StatusRunning,
		ClaimedBy: "agent-stale",
		CreatedAt: time.Now().Add(-10 * time.Minute).UnixMilli(), // 10 minutes ago
		Timeout:   30 * time.Second,
	}
	engine.tasks[staleTask.ID] = staleTask

	config := StaleTaskReaperConfig{
		Enabled:        true,
		ScanInterval:   100 * time.Millisecond, // Fast for test
		RunningTimeout: 30 * time.Second,
	}

	reaper := NewStaleTaskReaperEngine(logger, config, engine)
	require.NoError(t, reaper.Start(context.Background()))

	// Wait for at least one scan cycle
	time.Sleep(300 * time.Millisecond)
	reaper.Stop()

	// Verify the stale task was reported as timeout
	assert.Equal(t, 1, engine.reportCalled)
	result := engine.results["stale-task-1"]
	require.NotNil(t, result)
	assert.Equal(t, taskengine.StatusTimeout, result.Status)
	assert.Equal(t, "agent-stale", result.NodeID)
	assert.Contains(t, result.Error, "TIMEOUT by reaper")
}

func TestStaleTaskReaperEngine_DoesNotReapFreshTask(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockEngine()

	// Add a fresh RUNNING task (just started)
	freshTask := &taskengine.Task{
		ID:        "fresh-task-1",
		Type:      taskengine.TaskTypeArthasAttach,
		Status:    taskengine.StatusRunning,
		ClaimedBy: "agent-fresh",
		CreatedAt: time.Now().UnixMilli(), // Just now
		Timeout:   30 * time.Second,
	}
	engine.tasks[freshTask.ID] = freshTask

	config := StaleTaskReaperConfig{
		Enabled:        true,
		ScanInterval:   100 * time.Millisecond,
		RunningTimeout: 30 * time.Second,
	}

	reaper := NewStaleTaskReaperEngine(logger, config, engine)
	require.NoError(t, reaper.Start(context.Background()))

	// Wait for at least one scan cycle
	time.Sleep(300 * time.Millisecond)
	reaper.Stop()

	// Verify the fresh task was NOT reaped
	assert.Equal(t, 0, engine.reportCalled)
}

func TestStaleTaskReaperEngine_IgnoresNonRunningTasks(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := newMockEngine()

	// Add tasks in non-RUNNING states
	engine.tasks["pending-1"] = &taskengine.Task{
		ID:        "pending-1",
		Status:    taskengine.StatusPending,
		CreatedAt: time.Now().Add(-10 * time.Minute).UnixMilli(),
	}
	engine.tasks["success-1"] = &taskengine.Task{
		ID:        "success-1",
		Status:    taskengine.StatusSuccess,
		CreatedAt: time.Now().Add(-10 * time.Minute).UnixMilli(),
	}

	config := StaleTaskReaperConfig{
		Enabled:        true,
		ScanInterval:   100 * time.Millisecond,
		RunningTimeout: 30 * time.Second,
	}

	reaper := NewStaleTaskReaperEngine(logger, config, engine)
	require.NoError(t, reaper.Start(context.Background()))
	time.Sleep(300 * time.Millisecond)
	reaper.Stop()

	// Nothing should be reaped (mock engine ListTasks returns by status filter,
	// but the reaper also checks task.Status == Running in the scan loop)
	assert.Equal(t, 0, engine.reportCalled)
}
