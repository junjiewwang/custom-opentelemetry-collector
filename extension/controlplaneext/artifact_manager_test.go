// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/storageext/blobstore"
)

// --- Mock TaskManager ---

type mockTaskManager struct {
	mu      sync.Mutex
	results map[string]*model.TaskResult
	err     error
}

func newMockTaskManager() *mockTaskManager {
	return &mockTaskManager{
		results: make(map[string]*model.TaskResult),
	}
}

func (m *mockTaskManager) SubmitTask(_ context.Context, _ *model.Task) error          { return nil }
func (m *mockTaskManager) SubmitTaskForAgent(_ context.Context, _ *taskmanager.AgentMeta, _ *model.Task) error {
	return nil
}
func (m *mockTaskManager) FetchTask(_ context.Context, _ string, _ time.Duration) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskManager) GetPendingTasks(_ context.Context, _ string) ([]*model.Task, error) {
	return nil, nil
}
func (m *mockTaskManager) GetGlobalPendingTasks(_ context.Context) ([]*model.Task, error) {
	return nil, nil
}
func (m *mockTaskManager) GetAllTasks(_ context.Context) ([]*taskmanager.TaskInfo, error) {
	return nil, nil
}
func (m *mockTaskManager) ListTasks(_ context.Context, _ taskmanager.ListTasksQuery) (taskmanager.ListTasksPage, error) {
	return taskmanager.ListTasksPage{}, nil
}
func (m *mockTaskManager) CancelTask(_ context.Context, _ string) error    { return nil }
func (m *mockTaskManager) IsTaskCancelled(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (m *mockTaskManager) GetTaskStatus(_ context.Context, _ string) (*taskmanager.TaskInfo, error) {
	return nil, nil
}
func (m *mockTaskManager) SetTaskRunning(_ context.Context, _ string, _ string) error { return nil }
func (m *mockTaskManager) Start(_ context.Context) error                              { return nil }
func (m *mockTaskManager) Close() error                                               { return nil }

func (m *mockTaskManager) ReportTaskResult(_ context.Context, result *model.TaskResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.results[result.TaskID] = result
	return nil
}

func (m *mockTaskManager) GetTaskResult(_ context.Context, taskID string) (*model.TaskResult, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, false, m.err
	}
	r, ok := m.results[taskID]
	return r, ok, nil
}

func (m *mockTaskManager) getResult(taskID string) *model.TaskResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.results[taskID]
}

// --- Helper ---

func newTestMemoryChunkManager(t *testing.T) *ChunkManager {
	t.Helper()
	store := NewMemoryChunkStore(zap.NewNop(), DefaultChunkManagerConfig())
	cm := NewChunkManager(zap.NewNop(), store)
	t.Cleanup(func() { cm.Close() })
	return cm
}

// --- Tests ---

func newTestArtifactManager(t *testing.T) (*ArtifactManager, *ChunkManager, blobstore.BlobStore, *mockTaskManager) {
	t.Helper()

	cm := newTestMemoryChunkManager(t)

	dir := t.TempDir()
	bs, err := blobstore.NewLocalBlobStore(zap.NewNop(), blobstore.Config{
		Type:        "local",
		DataDir:     dir,
		MaxBlobSize: 1024 * 1024,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bs.Close() })

	tm := newMockTaskManager()

	am := NewArtifactManager(zap.NewNop(), cm, bs, tm, nil, nil)
	t.Cleanup(func() { am.Close() })

	return am, cm, bs, tm
}

func TestArtifactManager_HandleUploadChunk_SingleChunk(t *testing.T) {
	am, _, bs, tm := newTestArtifactManager(t)
	ctx := context.Background()

	data := []byte("profiling data payload")
	resp, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-1",
		UploadID:    "upload-1",
		ChunkIndex:  0,
		TotalChunks: 1,
		ChunkData:   data,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, model.ChunkUploadStatusUploadComplete, resp.Status)

	// Wait for async persistence
	assert.Eventually(t, func() bool {
		result := tm.getResult("task-1")
		return result != nil && result.ArtifactRef != ""
	}, 5*time.Second, 50*time.Millisecond)

	// Verify blob was persisted (key is just taskID, no "artifacts/" prefix)
	result := tm.getResult("task-1")
	require.NotNil(t, result)
	blobKey := result.ArtifactRef

	reader, err := bs.Get(ctx, blobKey)
	require.NoError(t, err)
	defer reader.Close()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	assert.Equal(t, int64(len(data)), result.ArtifactSize)
}

func TestArtifactManager_HandleUploadChunk_MultiChunk(t *testing.T) {
	am, _, bs, tm := newTestArtifactManager(t)
	ctx := context.Background()

	// Chunk 1 of 2
	resp1, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-2",
		UploadID:    "upload-2",
		ChunkIndex:  0,
		TotalChunks: 2,
		ChunkData:   []byte("part1-"),
	})
	require.NoError(t, err)
	assert.Equal(t, model.ChunkUploadStatusChunkReceived, resp1.Status)

	// Chunk 2 of 2
	resp2, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-2",
		UploadID:    "upload-2",
		ChunkIndex:  1,
		TotalChunks: 2,
		ChunkData:   []byte("part2"),
	})
	require.NoError(t, err)
	assert.Equal(t, model.ChunkUploadStatusUploadComplete, resp2.Status)

	// Wait for async persistence
	assert.Eventually(t, func() bool {
		result := tm.getResult("task-2")
		return result != nil && result.ArtifactRef != ""
	}, 5*time.Second, 50*time.Millisecond)

	// Verify assembled data
	result := tm.getResult("task-2")
	reader, err := bs.Get(ctx, result.ArtifactRef)
	require.NoError(t, err)
	defer reader.Close()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, []byte("part1-part2"), got)
}

func TestArtifactManager_HandleUploadChunk_ChunkReceivedDoesNotTriggerPersist(t *testing.T) {
	am, _, _, _ := newTestArtifactManager(t)
	ctx := context.Background()

	resp, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-3",
		UploadID:    "upload-3",
		ChunkIndex:  0,
		TotalChunks: 3, // Only sending 1 of 3
		ChunkData:   []byte("partial"),
	})
	require.NoError(t, err)
	assert.Equal(t, model.ChunkUploadStatusChunkReceived, resp.Status)
}

func TestArtifactManager_HandleUploadChunk_NilRequest(t *testing.T) {
	am, _, _, _ := newTestArtifactManager(t)
	_, err := am.HandleUploadChunk(context.Background(), nil)
	require.Error(t, err)
}

func TestArtifactManager_HandleUploadChunk_EmptyTaskID(t *testing.T) {
	am, _, _, _ := newTestArtifactManager(t)
	_, err := am.HandleUploadChunk(context.Background(), &model.ChunkUpload{
		TaskID:      "",
		ChunkIndex:  0,
		TotalChunks: 1,
		ChunkData:   []byte("data"),
	})
	require.Error(t, err)
}

func TestArtifactManager_PersistArtifact_TaskResultNotFound(t *testing.T) {
	am, _, bs, tm := newTestArtifactManager(t)
	ctx := context.Background()

	resp, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-no-result",
		UploadID:    "upload-no-result",
		ChunkIndex:  0,
		TotalChunks: 1,
		ChunkData:   []byte("data"),
	})
	require.NoError(t, err)
	assert.Equal(t, model.ChunkUploadStatusUploadComplete, resp.Status)

	// Wait for async persistence — should create a minimal result
	assert.Eventually(t, func() bool {
		result := tm.getResult("task-no-result")
		return result != nil
	}, 5*time.Second, 50*time.Millisecond)

	// Verify blob was still persisted
	result := tm.getResult("task-no-result")
	reader, err := bs.Get(ctx, result.ArtifactRef)
	require.NoError(t, err)
	_ = reader.Close()

	// Verify minimal result was created
	require.NotNil(t, result)
	assert.NotEmpty(t, result.ArtifactRef)
}

func TestArtifactManager_PersistArtifact_WithExistingResult(t *testing.T) {
	am, _, _, tm := newTestArtifactManager(t)
	ctx := context.Background()

	// Pre-populate a task result
	existing := &model.TaskResult{
		TaskID:     "task-existing",
		AgentID:    "agent-1",
		Status:     model.TaskStatusSuccess,
		ResultJSON: []byte(`{"key":"value"}`),
	}
	tm.mu.Lock()
	tm.results["task-existing"] = existing
	tm.mu.Unlock()

	resp, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-existing",
		UploadID:    "upload-existing",
		ChunkIndex:  0,
		TotalChunks: 1,
		ChunkData:   []byte("artifact data"),
	})
	require.NoError(t, err)
	assert.Equal(t, model.ChunkUploadStatusUploadComplete, resp.Status)

	// Wait for update
	assert.Eventually(t, func() bool {
		result := tm.getResult("task-existing")
		return result != nil && result.ArtifactRef != ""
	}, 5*time.Second, 50*time.Millisecond)

	// Verify existing fields are preserved
	result := tm.getResult("task-existing")
	assert.Equal(t, "agent-1", result.AgentID)
	assert.Equal(t, model.TaskStatusSuccess, result.Status)
	assert.NotEmpty(t, result.ArtifactRef)
	assert.Equal(t, int64(len("artifact data")), result.ArtifactSize)
}

func TestArtifactManager_Close_Idempotent(t *testing.T) {
	am, _, _, _ := newTestArtifactManager(t)
	am.Close()
	am.Close() // Should not panic
}

func TestArtifactManager_PersistArtifact_BlobStoreError(t *testing.T) {
	cm := newTestMemoryChunkManager(t)

	// Use a blob store that always fails on Put
	failBS := &failingBlobStore{}
	tm := newMockTaskManager()

	am := NewArtifactManager(zap.NewNop(), cm, failBS, tm, nil, nil)
	defer am.Close()

	ctx := context.Background()
	resp, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-fail",
		UploadID:    "upload-fail",
		ChunkIndex:  0,
		TotalChunks: 1,
		ChunkData:   []byte("data"),
	})
	require.NoError(t, err)
	assert.Equal(t, model.ChunkUploadStatusUploadComplete, resp.Status)

	// Wait and verify that task result was NOT updated (due to blob store error)
	time.Sleep(200 * time.Millisecond)
	result := tm.getResult("task-fail")
	assert.Nil(t, result)
}

// failingBlobStore always returns an error on Put.
type failingBlobStore struct{}

func (f *failingBlobStore) Put(_ context.Context, _ string, r io.Reader, _ map[string]string) (int64, error) {
	_, _ = io.Copy(io.Discard, r)
	return 0, errors.New("blob store write failure")
}
func (f *failingBlobStore) Get(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, blobstore.ErrNotFound
}
func (f *failingBlobStore) GetMeta(_ context.Context, _ string) (*blobstore.BlobMeta, error) {
	return nil, blobstore.ErrNotFound
}
func (f *failingBlobStore) Delete(_ context.Context, _ string) error { return nil }
func (f *failingBlobStore) FullKey(key string) string                { return key }
func (f *failingBlobStore) Close() error                             { return nil }

func TestArtifactManager_PersistArtifact_NilTaskManager(t *testing.T) {
	cm := newTestMemoryChunkManager(t)

	dir := t.TempDir()
	bs, err := blobstore.NewLocalBlobStore(zap.NewNop(), blobstore.Config{
		Type:    "local",
		DataDir: dir,
	})
	require.NoError(t, err)
	defer func() { _ = bs.Close() }()

	// Create with nil TaskManager
	am := NewArtifactManager(zap.NewNop(), cm, bs, nil, nil, nil)
	defer am.Close()

	ctx := context.Background()
	resp, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-nil-tm",
		UploadID:    "upload-nil-tm",
		ChunkIndex:  0,
		TotalChunks: 1,
		ChunkData:   []byte("data"),
	})
	require.NoError(t, err)
	assert.Equal(t, model.ChunkUploadStatusUploadComplete, resp.Status)

	// Wait for async persistence — blob should still be saved even without TaskManager
	time.Sleep(200 * time.Millisecond)

	// Key is just "task-nil-tm" (no "artifacts/" prefix, no extension)
	reader, err := bs.Get(ctx, "task-nil-tm")
	require.NoError(t, err)
	defer reader.Close()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), got)
}

// --- Mock BlobStore for Put size verification ---

type recordingBlobStore struct {
	blobstore.BlobStore
	mu       sync.Mutex
	putCalls []putCall
}

type putCall struct {
	key      string
	data     []byte
	metadata map[string]string
}

func (r *recordingBlobStore) Put(_ context.Context, key string, reader io.Reader, metadata map[string]string) (int64, error) {
	data, _ := io.ReadAll(reader)
	r.mu.Lock()
	r.putCalls = append(r.putCalls, putCall{key: key, data: data, metadata: metadata})
	r.mu.Unlock()
	return int64(len(data)), nil
}

func (r *recordingBlobStore) getPutCalls() []putCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]putCall{}, r.putCalls...)
}

func TestArtifactManager_PersistArtifact_BlobKeyFormat(t *testing.T) {
	cm := newTestMemoryChunkManager(t)

	rbs := &recordingBlobStore{BlobStore: blobstore.NewNoopBlobStore()}
	tm := newMockTaskManager()

	am := NewArtifactManager(zap.NewNop(), cm, rbs, tm, nil, nil)
	defer am.Close()

	ctx := context.Background()
	_, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-key-test",
		UploadID:    "upload-key-test",
		ChunkIndex:  0,
		TotalChunks: 1,
		ChunkData:   []byte("data"),
	})
	require.NoError(t, err)

	// Wait for async persistence
	assert.Eventually(t, func() bool {
		calls := rbs.getPutCalls()
		return len(calls) > 0
	}, 5*time.Second, 50*time.Millisecond)

	calls := rbs.getPutCalls()
	require.Len(t, calls, 1)
	// Key should be just taskID (no "artifacts/" prefix)
	assert.Equal(t, "task-key-test", calls[0].key)
	assert.Equal(t, []byte("data"), calls[0].data)
	assert.Equal(t, "task-key-test", calls[0].metadata["task_id"])
	assert.Equal(t, "upload-key-test", calls[0].metadata["upload_key"])
}

func TestArtifactManager_PersistArtifact_WithFileExtension(t *testing.T) {
	cm := newTestMemoryChunkManager(t)

	rbs := &recordingBlobStore{BlobStore: blobstore.NewNoopBlobStore()}
	tm := newMockTaskManager()

	am := NewArtifactManager(zap.NewNop(), cm, rbs, tm, nil, nil)
	defer am.Close()

	ctx := context.Background()
	_, err := am.HandleUploadChunk(ctx, &model.ChunkUpload{
		TaskID:      "task-ext",
		UploadID:    "upload-ext",
		ChunkIndex:  0,
		TotalChunks: 1,
		ChunkData:   []byte("collapsed data"),
		FileName:    "7556ebed.collapsed",
	})
	require.NoError(t, err)

	// Wait for async persistence
	assert.Eventually(t, func() bool {
		calls := rbs.getPutCalls()
		return len(calls) > 0
	}, 5*time.Second, 50*time.Millisecond)

	calls := rbs.getPutCalls()
	require.Len(t, calls, 1)
	// Key should include extension from fileName
	assert.Equal(t, "task-ext.collapsed", calls[0].key)
	assert.Equal(t, "7556ebed.collapsed", calls[0].metadata["filename"])
}