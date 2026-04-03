package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

func newTestRedisClient(t *testing.T) *redis.Client {
	t.Helper()

	_, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server not found in PATH, skipping Redis integration test")
	}

	port := reserveLocalPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	var serverOutput bytes.Buffer
	cmd := exec.Command(
		"redis-server",
		"--save", "",
		"--appendonly", "no",
		"--bind", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--dir", t.TempDir(),
		"--loglevel", "warning",
	)
	cmd.Stdout = &serverOutput
	cmd.Stderr = &serverOutput
	require.NoError(t, cmd.Start())

	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() {
		_ = client.Close()
	})

	require.Eventually(t, func() bool {
		return client.Ping(context.Background()).Err() == nil
	}, 5*time.Second, 50*time.Millisecond, "redis-server did not start successfully: %s", serverOutput.String())

	return client
}

func newTestRedisStore(t *testing.T) *RedisTaskStore {
	t.Helper()

	client := newTestRedisClient(t)
	store := NewRedisTaskStore(
		zap.NewNop(),
		client,
		fmt.Sprintf("otel:test:%s", sanitizeRedisKeyPart(t.Name())),
		time.Hour,
	)
	require.NoError(t, store.Start(context.Background()))
	t.Cleanup(func() {
		_ = store.Close()
	})

	return store
}

func reserveLocalPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		_ = listener.Close()
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return addr.Port
}

func sanitizeRedisKeyPart(name string) string {
	replacer := strings.NewReplacer("/", "-", " ", "-", ":", "-")
	return replacer.Replace(name)
}

func TestRedisTaskStore_ApplyTaskResult_UsesLuaStateMachine(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &model.Task{
			ID:       "task-apply-result",
			TypeName: "test",
		},
		Status: model.TaskStatusPending,
	}
	require.NoError(t, store.SaveTaskInfo(ctx, info, true))

	res, err := store.ApplyTaskResult(ctx, info.Task.ID, &model.TaskResult{
		TaskID:  info.Task.ID,
		Status:  model.TaskStatusRunning,
		AgentID: "agent-1",
	}, 123)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskUpdated, res.Code)
	assert.Equal(t, model.TaskStatusRunning, res.Status)
	assert.Equal(t, "agent-1", res.AgentID)

	retrieved, err := store.GetTaskInfo(ctx, info.Task.ID)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, model.TaskStatusRunning, retrieved.Status)
	assert.Equal(t, "agent-1", retrieved.AgentID)
	assert.Equal(t, int64(123), retrieved.StartedAtMillis)
	require.NotNil(t, retrieved.Result)
	assert.Equal(t, model.TaskStatusRunning, retrieved.Result.Status)
}

func TestRedisTaskStore_ApplyCancel_RejectsTerminal(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &model.Task{
			ID:       "task-cancel-terminal",
			TypeName: "test",
		},
		Status: model.TaskStatusSuccess,
	}
	require.NoError(t, store.SaveTaskInfo(ctx, info, true))

	res, err := store.ApplyCancel(ctx, info.Task.ID, 456)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskRejected, res.Code)
	assert.Equal(t, model.TaskStatusSuccess, res.Status)

	retrieved, err := store.GetTaskInfo(ctx, info.Task.ID)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, model.TaskStatusSuccess, retrieved.Status)
}

func TestRedisTaskStore_ApplySetRunning_Idempotent(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task: &model.Task{
			ID:       "task-running-idempotent",
			TypeName: "test",
		},
		Status:          model.TaskStatusRunning,
		AgentID:         "agent-1",
		StartedAtMillis: 100,
	}
	require.NoError(t, store.SaveTaskInfo(ctx, info, true))

	res, err := store.ApplySetRunning(ctx, info.Task.ID, "agent-2", 789)
	require.NoError(t, err)
	assert.Equal(t, ApplyTaskNoop, res.Code)
	assert.Equal(t, model.TaskStatusRunning, res.Status)
	assert.Equal(t, "agent-1", res.AgentID)

	retrieved, err := store.GetTaskInfo(ctx, info.Task.ID)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, "agent-1", retrieved.AgentID)
	assert.Equal(t, int64(100), retrieved.StartedAtMillis)
}

func TestRedisTaskStore_ListRunningTaskInfos_CleansStaleEntries(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	runningInfo := &TaskInfo{
		Task: &model.Task{
			ID:       "task-running",
			TypeName: "test",
		},
		Status: model.TaskStatusRunning,
	}
	pendingInfo := &TaskInfo{
		Task: &model.Task{
			ID:       "task-pending",
			TypeName: "test",
		},
		Status: model.TaskStatusPending,
	}

	require.NoError(t, store.SaveTaskInfo(ctx, runningInfo, true))
	require.NoError(t, store.SaveTaskInfo(ctx, pendingInfo, true))
	require.NoError(t, store.SetRunning(ctx, "task-running", "agent-1"))
	require.NoError(t, store.SetRunning(ctx, "task-pending", "agent-2"))
	require.NoError(t, store.SetRunning(ctx, "task-missing", "agent-3"))

	infos, err := store.ListRunningTaskInfos(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "task-running", infos[0].Task.ID)
	assert.Equal(t, "agent-1", infos[0].AgentID)

	agentID, err := store.GetRunning(ctx, "task-pending")
	require.NoError(t, err)
	assert.Empty(t, agentID)

	agentID, err = store.GetRunning(ctx, "task-missing")
	require.NoError(t, err)
	assert.Empty(t, agentID)
}

func TestRedisTaskStore_ListTaskInfos_ScanBatches(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	const taskCount = 205
	for i := 0; i < taskCount; i++ {
		info := &TaskInfo{
			Task: &model.Task{
				ID:       fmt.Sprintf("task-%03d", i),
				TypeName: "test",
			},
			Status: model.TaskStatusPending,
		}
		require.NoError(t, store.SaveTaskInfo(ctx, info, true))
	}

	infos, err := store.ListTaskInfos(ctx)
	require.NoError(t, err)
	require.Len(t, infos, taskCount)

	seen := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		require.NotNil(t, info)
		require.NotNil(t, info.Task)
		seen[info.Task.ID] = struct{}{}
	}

	assert.Contains(t, seen, "task-000")
	assert.Contains(t, seen, "task-204")
}

func TestRedisTaskStore_ListTaskInfosPage_FilterAndPaginate(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	tasks := []*TaskInfo{
		{
			Task:            &model.Task{ID: "task-a", TypeName: "collect", ParametersJSON: []byte(`{"step":1}`)},
			Status:          model.TaskStatusPending,
			AppID:           "app-1",
			ServiceName:     "svc-1",
			AgentID:         "agent-1",
			CreatedAtMillis: 100,
			Result: &model.TaskResult{
				TaskID:      "task-a",
				Status:      model.TaskStatusPending,
				ResultJSON:  []byte(`{"ok":true}`),
				ResultData:  []byte("payload-a"),
				ArtifactRef: "artifact-a",
			},
		},
		{
			Task:            &model.Task{ID: "task-b", TypeName: "collect"},
			Status:          model.TaskStatusRunning,
			AppID:           "app-1",
			ServiceName:     "svc-1",
			AgentID:         "agent-1",
			CreatedAtMillis: 300,
		},
		{
			Task:            &model.Task{ID: "task-c", TypeName: "deploy"},
			Status:          model.TaskStatusRunning,
			AppID:           "app-2",
			ServiceName:     "svc-2",
			AgentID:         "agent-2",
			CreatedAtMillis: 200,
		},
	}

	for _, info := range tasks {
		require.NoError(t, store.SaveTaskInfo(ctx, info, true))
	}

	filteredPage, err := store.ListTaskInfosPage(ctx, TaskListQuery{
		Statuses: []model.TaskStatus{model.TaskStatusRunning},
		AppID:    "app-1",
		TaskType: "collect",
		Limit:    1,
	})
	require.NoError(t, err)
	require.Len(t, filteredPage.Items, 1)
	assert.Equal(t, "task-b", filteredPage.Items[0].Task.ID)
	assert.False(t, filteredPage.HasMore)
	assert.Empty(t, filteredPage.NextCursor)

	page, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "task-b", page.Items[0].Task.ID)
	assert.True(t, page.HasMore)
	assert.NotEmpty(t, page.NextCursor) // seek cursor 格式

	nextPage, err := store.ListTaskInfosPage(ctx, TaskListQuery{
		Limit:  1,
		Cursor: page.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, nextPage.Items, 1)
	assert.Equal(t, "task-c", nextPage.Items[0].Task.ID)
	assert.True(t, nextPage.HasMore)
	assert.NotEmpty(t, nextPage.NextCursor)

	finalPage, err := store.ListTaskInfosPage(ctx, TaskListQuery{
		Limit:  1,
		Cursor: nextPage.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, finalPage.Items, 1)
	assert.Equal(t, "task-a", finalPage.Items[0].Task.ID)
	assert.False(t, finalPage.HasMore)
	assert.Empty(t, finalPage.NextCursor)
	require.NotNil(t, finalPage.Items[0].Task)
	assert.Equal(t, []byte(`{"step":1}`), []byte(finalPage.Items[0].Task.ParametersJSON))
	require.NotNil(t, finalPage.Items[0].Result)
	assert.Equal(t, []byte(`{"ok":true}`), []byte(finalPage.Items[0].Result.ResultJSON))
	assert.Equal(t, []byte("payload-a"), finalPage.Items[0].Result.ResultData)

	finalPage.Items[0].Task.ParametersJSON[0] = '['
	finalPage.Items[0].Result.ResultData[0] = 'X'
	retrieved, err := store.GetTaskInfo(ctx, "task-a")
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, []byte(`{"step":1}`), []byte(retrieved.Task.ParametersJSON))
	assert.Equal(t, []byte("payload-a"), retrieved.Result.ResultData)

}

func TestRedisTaskStore_ListTaskInfosPage_MergesStatusUnionWithOtherFilters(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	tasks := []*TaskInfo{
		{
			Task:            &model.Task{ID: "task-pending", TypeName: "collect"},
			Status:          model.TaskStatusPending,
			AppID:           "app-1",
			ServiceName:     "svc-1",
			AgentID:         "agent-1",
			CreatedAtMillis: 100,
		},
		{
			Task:            &model.Task{ID: "task-running", TypeName: "collect"},
			Status:          model.TaskStatusRunning,
			AppID:           "app-1",
			ServiceName:     "svc-1",
			AgentID:         "agent-1",
			CreatedAtMillis: 300,
		},
		{
			Task:            &model.Task{ID: "task-running-other-agent", TypeName: "collect"},
			Status:          model.TaskStatusRunning,
			AppID:           "app-1",
			ServiceName:     "svc-1",
			AgentID:         "agent-2",
			CreatedAtMillis: 400,
		},
		{
			Task:            &model.Task{ID: "task-success", TypeName: "collect"},
			Status:          model.TaskStatusSuccess,
			AppID:           "app-1",
			ServiceName:     "svc-1",
			AgentID:         "agent-1",
			CreatedAtMillis: 500,
		},
		{
			Task:            &model.Task{ID: "task-other-type", TypeName: "deploy"},
			Status:          model.TaskStatusPending,
			AppID:           "app-1",
			ServiceName:     "svc-1",
			AgentID:         "agent-1",
			CreatedAtMillis: 600,
		},
		{
			Task:            &model.Task{ID: "task-other-app", TypeName: "collect"},
			Status:          model.TaskStatusRunning,
			AppID:           "app-2",
			ServiceName:     "svc-1",
			AgentID:         "agent-1",
			CreatedAtMillis: 700,
		},
	}

	for _, info := range tasks {
		require.NoError(t, store.SaveTaskInfo(ctx, info, true))
	}

	page, err := store.ListTaskInfosPage(ctx, TaskListQuery{
		Statuses:    []model.TaskStatus{model.TaskStatusPending, model.TaskStatusRunning, model.TaskStatusPending},
		AppID:       "app-1",
		ServiceName: "svc-1",
		AgentID:     "agent-1",
		TaskType:    "collect",
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, []string{"task-running", "task-pending"}, []string{page.Items[0].Task.ID, page.Items[1].Task.ID})
	assert.False(t, page.HasMore)
	assert.Empty(t, page.NextCursor)
}

func TestRedisTaskStore_ListTaskInfosPage_CleansStaleTaskArtifacts(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	info := &TaskInfo{
		Task:            &model.Task{ID: "task-stale", TypeName: "collect"},
		Status:          model.TaskStatusRunning,
		AppID:           "app-1",
		ServiceName:     "svc-1",
		AgentID:         "agent-1",
		CreatedAtMillis: 123,
	}
	require.NoError(t, store.SaveTaskInfo(ctx, info, true))
	require.NoError(t, store.SaveResult(ctx, &model.TaskResult{TaskID: info.Task.ID, Status: model.TaskStatusRunning}))
	require.NoError(t, store.SetCancelled(ctx, info.Task.ID))
	require.NoError(t, store.SetRunning(ctx, info.Task.ID, info.AgentID))

	client, err := store.getClient()
	require.NoError(t, err)
	require.NoError(t, client.Del(ctx, store.detailKey(info.Task.ID)).Err())

	page, err := store.ListTaskInfosPage(ctx, TaskListQuery{
		Statuses: []model.TaskStatus{model.TaskStatusRunning},
		AppID:    info.AppID,
		AgentID:  info.AgentID,
	})
	require.NoError(t, err)
	assert.Empty(t, page.Items)

	cancelled, err := store.IsCancelled(ctx, info.Task.ID)
	require.NoError(t, err)
	assert.False(t, cancelled)

	runningAgent, err := store.GetRunning(ctx, info.Task.ID)
	require.NoError(t, err)
	assert.Empty(t, runningAgent)

	result, ok, err := store.GetResult(ctx, info.Task.ID)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, result)

	members, err := client.SMembers(ctx, store.taskIndexMembersKey(info.Task.ID)).Result()
	require.NoError(t, err)
	assert.Empty(t, members)

	_, err = client.ZScore(ctx, store.taskCreatedAtIndexKey(), info.Task.ID).Result()
	assert.ErrorIs(t, err, redis.Nil)
	_, err = client.ZScore(ctx, store.taskStatusIndexKey(model.TaskStatusRunning), info.Task.ID).Result()
	assert.ErrorIs(t, err, redis.Nil)
}

func TestRedisTaskStore_Start_BackfillsTaskIndexes(t *testing.T) {
	ctx := context.Background()
	client := newTestRedisClient(t)
	store := NewRedisTaskStore(
		zap.NewNop(),
		client,
		fmt.Sprintf("otel:test:%s", sanitizeRedisKeyPart(t.Name())),
		time.Hour,
	)

	info := &TaskInfo{
		Task:            &model.Task{ID: "task-backfill", TypeName: "collect"},
		Status:          model.TaskStatusPending,
		AppID:           "app-1",
		ServiceName:     "svc-1",
		AgentID:         "agent-1",
		CreatedAtMillis: 456,
	}
	payload, err := json.Marshal(info)
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, store.detailKey(info.Task.ID), payload, time.Hour).Err())

	require.NoError(t, store.Start(ctx))
	t.Cleanup(func() {
		_ = store.Close()
	})

	// backfill 是异步的，等待索引回填完成
	require.Eventually(t, func() bool {
		score, err := client.ZScore(ctx, store.taskCreatedAtIndexKey(), info.Task.ID).Result()
		return err == nil && score > 0
	}, 5*time.Second, 50*time.Millisecond, "backfill did not complete")

	page, err := store.ListTaskInfosPage(ctx, TaskListQuery{
		Statuses:    []model.TaskStatus{model.TaskStatusPending},
		AppID:       info.AppID,
		ServiceName: info.ServiceName,
		AgentID:     info.AgentID,
		TaskType:    info.Task.TypeName,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, info.Task.ID, page.Items[0].Task.ID)

	_, err = client.ZScore(ctx, store.taskCreatedAtIndexKey(), info.Task.ID).Result()
	require.NoError(t, err)
}

func TestRedisTaskStore_ListTaskInfosPage_InvalidCursor(t *testing.T) {
	store := newTestRedisStore(t)
	_, err := store.ListTaskInfosPage(context.Background(), TaskListQuery{Cursor: "bad-cursor"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cursor")
}

// TestRedisTaskStore_ListTaskInfosPage_SeekCursor 验证 seek cursor 翻页链路。
func TestRedisTaskStore_ListTaskInfosPage_SeekCursor(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	// 创建 5 个 task，created_at 分别为 100, 200, 300, 400, 500
	for i := 1; i <= 5; i++ {
		info := &TaskInfo{
			Task:            &model.Task{ID: fmt.Sprintf("task-%d", i), TypeName: "test"},
			Status:          model.TaskStatusPending,
			CreatedAtMillis: int64(i * 100),
		}
		require.NoError(t, store.SaveTaskInfo(ctx, info, true))
	}

	// 第一页：limit=2，无 cursor
	page1, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1.Items, 2)
	assert.Equal(t, "task-5", page1.Items[0].Task.ID)
	assert.Equal(t, "task-4", page1.Items[1].Task.ID)
	assert.True(t, page1.HasMore)
	assert.NotEmpty(t, page1.NextCursor)

	// 第二页：使用 seek cursor
	page2, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 2, Cursor: page1.NextCursor})
	require.NoError(t, err)
	require.Len(t, page2.Items, 2)
	assert.Equal(t, "task-3", page2.Items[0].Task.ID)
	assert.Equal(t, "task-2", page2.Items[1].Task.ID)
	assert.True(t, page2.HasMore)
	assert.NotEmpty(t, page2.NextCursor)

	// 第三页：最后一条
	page3, err := store.ListTaskInfosPage(ctx, TaskListQuery{Limit: 2, Cursor: page2.NextCursor})
	require.NoError(t, err)
	require.Len(t, page3.Items, 1)
	assert.Equal(t, "task-1", page3.Items[0].Task.ID)
	assert.False(t, page3.HasMore)
	assert.Empty(t, page3.NextCursor)
}

// TestRedisTaskStore_RunningZSET_DualWrite 验证 running 结构的 Hash + ZSET 双写。
func TestRedisTaskStore_RunningZSET_DualWrite(t *testing.T) {
	store := newTestRedisStore(t)
	ctx := context.Background()

	// 创建 task 并设置 running
	info := &TaskInfo{
		Task:            &model.Task{ID: "task-running-zset", TypeName: "test"},
		Status:          model.TaskStatusRunning,
		StartedAtMillis: 12345,
	}
	require.NoError(t, store.SaveTaskInfo(ctx, info, true))
	require.NoError(t, store.SetRunning(ctx, "task-running-zset", "agent-1"))

	client, err := store.getClient()
	require.NoError(t, err)

	// 验证 Hash 写入
	agentID, err := client.HGet(ctx, store.runningKey(), "task-running-zset").Result()
	require.NoError(t, err)
	assert.Equal(t, "agent-1", agentID)

	// 验证 ZSET 写入
	score, err := client.ZScore(ctx, store.runningByStartedAtKey(), "task-running-zset").Result()
	require.NoError(t, err)
	assert.Equal(t, float64(12345), score)

	// ClearRunning 应该同时清除 Hash 和 ZSET
	require.NoError(t, store.ClearRunning(ctx, "task-running-zset"))

	exists, err := client.HExists(ctx, store.runningKey(), "task-running-zset").Result()
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = client.ZScore(ctx, store.runningByStartedAtKey(), "task-running-zset").Result()
	assert.ErrorIs(t, err, redis.Nil)
}

// TestRedisTaskStore_BackfillSkipsWhenIndexExists 验证索引已存在时 backfill 被跳过。
func TestRedisTaskStore_BackfillSkipsWhenIndexExists(t *testing.T) {
	ctx := context.Background()
	client := newTestRedisClient(t)
	prefix := fmt.Sprintf("otel:test:%s", sanitizeRedisKeyPart(t.Name()))

	// 先创建一个 store 并写入数据（建立索引）
	store1 := NewRedisTaskStore(zap.NewNop(), client, prefix, time.Hour)
	require.NoError(t, store1.Start(ctx))

	info := &TaskInfo{
		Task:            &model.Task{ID: "task-existing", TypeName: "test"},
		Status:          model.TaskStatusPending,
		CreatedAtMillis: 100,
	}
	require.NoError(t, store1.SaveTaskInfo(ctx, info, true))
	_ = store1.Close()

	// 再创建一个新 store（模拟重启），backfill 应该被跳过
	store2 := NewRedisTaskStore(zap.NewNop(), client, prefix, time.Hour)
	start := time.Now()
	require.NoError(t, store2.Start(ctx))
	elapsed := time.Since(start)
	t.Cleanup(func() { _ = store2.Close() })

	// 启动应该很快（< 100ms），因为 backfill 被跳过了
	assert.Less(t, elapsed, 100*time.Millisecond, "Start should be fast when indexes exist")

	// 数据仍然可查
	page, err := store2.ListTaskInfosPage(ctx, TaskListQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "task-existing", page.Items[0].Task.ID)
}
