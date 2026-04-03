package taskmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager/store"
)

type observedTaskStore struct {
	store.TaskStore
	listAllCalled     bool
	listRunningCalled bool
}

func (s *observedTaskStore) ListTaskInfos(ctx context.Context) ([]*store.TaskInfo, error) {
	s.listAllCalled = true
	return s.TaskStore.ListTaskInfos(ctx)
}

func (s *observedTaskStore) ListRunningTaskInfos(ctx context.Context) ([]*store.TaskInfo, error) {
	s.listRunningCalled = true
	return s.TaskStore.ListRunningTaskInfos(ctx)
}

func TestStaleTaskReaper_ScanUsesRunningIndex(t *testing.T) {
	ctx := context.Background()
	baseStore := store.NewMemoryTaskStore(zap.NewNop(), time.Hour)
	require.NoError(t, baseStore.Start(ctx))
	t.Cleanup(func() {
		_ = baseStore.Close()
	})

	nowMillis := time.Now().UnixMilli()
	info := &store.TaskInfo{
		Task: &model.Task{
			ID:            "stale-task",
			TypeName:      "test",
			TimeoutMillis: 1,
		},
		Status:              model.TaskStatusRunning,
		AgentID:             "agent-1",
		CreatedAtMillis:     nowMillis - 10_000,
		StartedAtMillis:     nowMillis - 10_000,
		LastUpdatedAtMillis: nowMillis - 10_000,
	}
	require.NoError(t, baseStore.SaveTaskInfo(ctx, info, true))
	require.NoError(t, baseStore.SetRunning(ctx, "stale-task", "agent-1"))

	observedStore := &observedTaskStore{TaskStore: baseStore}
	reaper := NewStaleTaskReaper(zap.NewNop(), StaleTaskReaperConfig{
		Enabled:        true,
		ScanInterval:   time.Second,
		RunningTimeout: time.Millisecond,
	}, observedStore)

	reaper.scan()

	assert.True(t, observedStore.listRunningCalled)
	assert.False(t, observedStore.listAllCalled)

	updatedInfo, err := baseStore.GetTaskInfo(ctx, "stale-task")
	require.NoError(t, err)
	require.NotNil(t, updatedInfo)
	assert.Equal(t, model.TaskStatusTimeout, updatedInfo.Status)

	result, found, err := baseStore.GetResult(ctx, "stale-task")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, model.TaskStatusTimeout, result.Status)

	agentID, err := baseStore.GetRunning(ctx, "stale-task")
	require.NoError(t, err)
	assert.Empty(t, agentID)
}
