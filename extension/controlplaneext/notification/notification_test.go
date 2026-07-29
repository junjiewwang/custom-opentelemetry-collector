// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package notification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ── HTTPNotifier ────────────────────────────────────────────────────────

func TestHTTPNotifier_ShouldNotify(t *testing.T) {
	cfg := Config{
		TaskTypes: []string{"profiling", "heap_dump"},
	}
	n := NewHTTPNotifier(zaptest.NewLogger(t), cfg)
	assert.True(t, n.ShouldNotify("profiling"))
	assert.True(t, n.ShouldNotify("heap_dump"))
	assert.False(t, n.ShouldNotify("unknown_type"))
	assert.False(t, n.ShouldNotify(""))
}

func TestHTTPNotifier_Notify_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req perfAnalysisRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "task-123", req.TID)
		assert.Equal(t, "async-profiler", req.Profiler)
		assert.Equal(t, "cpu", req.Event)
		assert.Equal(t, "cos://bucket/result.jfr", req.ResultFile)
		assert.Equal(t, "http://cb.example.com", req.CallbackURL)
		assert.Equal(t, "otel-collector", req.Metadata["origin"])
		assert.Equal(t, "async-profiler", req.Metadata["original_task_type"])

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{
		AnalysisServiceURL: server.URL,
		CallbackURL:        "http://cb.example.com",
		TaskTypes:          []string{"async-profiler"},
	}
	n := NewHTTPNotifier(zaptest.NewLogger(t), cfg)

	result := n.Notify(context.Background(), &ArtifactNotification{
		TaskID:      "task-123",
		TaskType:    "async-profiler",
		Profiler:    "async-profiler",
		Event:       "cpu",
		ArtifactRef: "cos://bucket/result.jfr",
	})
	assert.True(t, result.Success)
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.Empty(t, result.ErrorMessage)
	assert.Equal(t, 1, result.AttemptCount)
}

func TestHTTPNotifier_Notify_MetadataMerge(t *testing.T) {
	var receivedMeta map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req perfAnalysisRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedMeta = req.Metadata
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{AnalysisServiceURL: server.URL, TaskTypes: []string{"profiling"}}
	n := NewHTTPNotifier(zaptest.NewLogger(t), cfg)

	n.Notify(context.Background(), &ArtifactNotification{
		TaskID:   "t1",
		TaskType: "profiling",
		Metadata: map[string]string{"custom_key": "custom_val"},
	})
	assert.Equal(t, "custom_val", receivedMeta["custom_key"])
	assert.Equal(t, "otel-collector", receivedMeta["origin"]) // built-in preserved
}

func TestHTTPNotifier_Notify_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := Config{AnalysisServiceURL: server.URL, TaskTypes: []string{"profiling"}}
	n := NewHTTPNotifier(zaptest.NewLogger(t), cfg)

	result := n.Notify(context.Background(), &ArtifactNotification{TaskID: "t1", TaskType: "profiling"})
	assert.False(t, result.Success)
	assert.Equal(t, http.StatusInternalServerError, result.StatusCode)
	assert.Contains(t, result.ErrorMessage, "500")
}

func TestHTTPNotifier_Notify_ConnectionRefused(t *testing.T) {
	cfg := Config{
		AnalysisServiceURL: "http://127.0.0.1:19999", // no server
		TaskTypes:          []string{"profiling"},
		Timeout:            50 * time.Millisecond,
	}
	n := NewHTTPNotifier(zaptest.NewLogger(t), cfg)

	result := n.Notify(context.Background(), &ArtifactNotification{TaskID: "t1", TaskType: "profiling"})
	assert.False(t, result.Success)
	assert.Contains(t, result.ErrorMessage, "send request")
	assert.Equal(t, 1, result.AttemptCount)
}

func TestHTTPNotifier_Notify_InvalidURL(t *testing.T) {
	cfg := Config{
		AnalysisServiceURL: "://bad-url",
		TaskTypes:          []string{"profiling"},
	}
	n := NewHTTPNotifier(zaptest.NewLogger(t), cfg)

	result := n.Notify(context.Background(), &ArtifactNotification{TaskID: "t1", TaskType: "profiling"})
	assert.False(t, result.Success)
	assert.Contains(t, result.ErrorMessage, "create request")
}

func TestHTTPNotifier_DefaultTimeout(t *testing.T) {
	cfg := Config{
		AnalysisServiceURL: "http://example.com",
		TaskTypes:          []string{"profiling"},
		Timeout:            0, // should default to 10s
	}
	n := NewHTTPNotifier(zap.NewNop(), cfg)
	hn := n.(*httpNotifier)
	assert.Equal(t, 10*time.Second, hn.client.Timeout)
}

func TestHTTPNotifier_ZeroTaskTypes(t *testing.T) {
	cfg := Config{AnalysisServiceURL: "http://example.com"}
	n := NewHTTPNotifier(zap.NewNop(), cfg)
	assert.False(t, n.ShouldNotify("profiling"), "empty task type list should not notify")
}

// ── NoopNotifier ────────────────────────────────────────────────────────

func TestNoopNotifier_ShouldNotify(t *testing.T) {
	n := NewNoopNotifier()
	assert.False(t, n.ShouldNotify("profiling"))
	assert.False(t, n.ShouldNotify(""))
}

func TestNoopNotifier_Notify_AlwaysSuccess(t *testing.T) {
	n := NewNoopNotifier()
	result := n.Notify(context.Background(), &ArtifactNotification{
		TaskID:   "task-x",
		TaskType: "profiling",
	})
	assert.True(t, result.Success)
	assert.False(t, result.NotifiedAt.IsZero())
}

// ── Store (mock) ───────────────────────────────────────────────────────
type mockStore struct {
	mu    sync.Mutex
	data  map[string]string // id → JSON
	tasks map[string]string // taskID → record ID
	sets  map[Status]map[string]float64
}

func newMockStore() *mockStore {
	return &mockStore{
		data:  make(map[string]string),
		tasks: make(map[string]string),
		sets:  make(map[Status]map[string]float64),
	}
}

func (m *mockStore) Save(_ context.Context, record *Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, _ := json.Marshal(record)
	m.data[record.ID] = string(b)
	m.tasks[record.TaskID] = record.ID
	if m.sets[record.Status] == nil {
		m.sets[record.Status] = make(map[string]float64)
	}
	m.sets[record.Status][record.ID] = float64(record.CreatedAt.Unix())
	return nil
}

func (m *mockStore) Get(_ context.Context, id string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	raw, ok := m.data[id]
	if !ok {
		return nil, nil
	}
	var rec Record
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (m *mockStore) GetByTaskID(_ context.Context, taskID string) (*Record, error) {
	m.mu.Lock()
	id, ok := m.tasks[taskID]
	m.mu.Unlock()
	if !ok {
		return nil, nil
	}
	return m.Get(context.Background(), id)
}

func (m *mockStore) ListByStatus(_ context.Context, status Status, limit int) ([]*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sets[status]
	if !ok {
		return nil, nil
	}
	type item struct {
		id    string
		score float64
	}
	items := make([]item, 0, len(s))
	for id, score := range s {
		items = append(items, item{id, score})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].score < items[j].score })
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	recs := make([]*Record, 0, len(items))
	for _, it := range items {
		raw := m.data[it.id]
		var rec Record
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			continue
		}
		recs = append(recs, &rec)
	}
	return recs, nil
}

func (m *mockStore) Update(_ context.Context, record *Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Load old record to migrate status index.
	if oldRaw, ok := m.data[record.ID]; ok {
		var old Record
		if err := json.Unmarshal([]byte(oldRaw), &old); err == nil && old.Status != record.Status {
			if s, ok := m.sets[old.Status]; ok {
				delete(s, record.ID)
			}
		}
	}

	// Save updated record.
	b, _ := json.Marshal(record)
	m.data[record.ID] = string(b)
	m.tasks[record.TaskID] = record.ID
	if m.sets[record.Status] == nil {
		m.sets[record.Status] = make(map[string]float64)
	}
	m.sets[record.Status][record.ID] = float64(record.CreatedAt.Unix())
	return nil
}

var _ Store = (*mockStore)(nil)

func TestStore_SaveAndGet(t *testing.T) {
	s := newMockStore()
	rec := &Record{
		ID:        "notif-1",
		TaskID:    "task-a",
		Status:    StatusPending,
		CreatedAt: time.Unix(1000, 0),
	}
	require.NoError(t, s.Save(context.Background(), rec))

	got, err := s.Get(context.Background(), "notif-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "task-a", got.TaskID)
	assert.Equal(t, StatusPending, got.Status)
}

func TestStore_GetNotFound(t *testing.T) {
	s := newMockStore()
	got, err := s.Get(context.Background(), "nonexistent")
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestStore_GetByTaskID(t *testing.T) {
	s := newMockStore()
	rec := &Record{ID: "notif-2", TaskID: "task-b", Status: StatusSent, CreatedAt: time.Unix(2000, 0)}
	s.Save(context.Background(), rec)

	got, err := s.GetByTaskID(context.Background(), "task-b")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "notif-2", got.ID)
}

func TestStore_GetByTaskID_NotFound(t *testing.T) {
	s := newMockStore()
	got, err := s.GetByTaskID(context.Background(), "unknown-task")
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestStore_ListByStatus(t *testing.T) {
	s := newMockStore()
	s.Save(context.Background(), &Record{ID: "a", TaskID: "t1", Status: StatusFailed, CreatedAt: time.Unix(3000, 0)})
	s.Save(context.Background(), &Record{ID: "b", TaskID: "t2", Status: StatusFailed, CreatedAt: time.Unix(1000, 0)})
	s.Save(context.Background(), &Record{ID: "c", TaskID: "t3", Status: StatusSent, CreatedAt: time.Unix(2000, 0)})

	failed, err := s.ListByStatus(context.Background(), StatusFailed, 10)
	require.NoError(t, err)
	assert.Len(t, failed, 2)
	assert.Equal(t, "b", failed[0].ID) // earlier time first
	assert.Equal(t, "a", failed[1].ID)

	// Different status
	sent, err := s.ListByStatus(context.Background(), StatusSent, 10)
	require.NoError(t, err)
	assert.Len(t, sent, 1)
	assert.Equal(t, "c", sent[0].ID)
}

func TestStore_Update_StatusMigration(t *testing.T) {
	s := newMockStore()
	rec := &Record{ID: "notif-3", TaskID: "task-c", Status: StatusPending, CreatedAt: time.Unix(5000, 0)}
	s.Save(context.Background(), rec)

	// Verify initial status index
	pending, _ := s.ListByStatus(context.Background(), StatusPending, 10)
	assert.Len(t, pending, 1)

	// Update: status change migrates indices
	rec.Status = StatusSent
	require.NoError(t, s.Update(context.Background(), rec))

	got, _ := s.Get(context.Background(), "notif-3")
	assert.Equal(t, StatusSent, got.Status)

	// Old status index should not have the record
	afterPending, _ := s.ListByStatus(context.Background(), StatusPending, 10)
	assert.Len(t, afterPending, 0)

	// New status index should have it
	afterSent, _ := s.ListByStatus(context.Background(), StatusSent, 10)
	assert.Len(t, afterSent, 1)
	assert.Equal(t, "notif-3", afterSent[0].ID)
}

func TestStore_ListByStatus_Empty(t *testing.T) {
	s := newMockStore()
	recs, err := s.ListByStatus(context.Background(), StatusCallbackReceived, 10)
	assert.NoError(t, err)
	assert.Nil(t, recs)
}

func TestConfig_Defaults(t *testing.T) {
	cfg := DefaultConfig()
	assert.False(t, cfg.Enabled)
	assert.Equal(t, []string{"async-profiler"}, cfg.TaskTypes)
	assert.Equal(t, 10*time.Second, cfg.Timeout)
	assert.Equal(t, "default", cfg.RedisName)
	assert.Equal(t, "otel:notifications", cfg.KeyPrefix)
	assert.Equal(t, 72*time.Hour, cfg.RecordTTL)
}
