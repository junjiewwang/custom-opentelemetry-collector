// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/taskengine"
)

// ═══════════════════════════════════════════════════════════════════════
// helper.go — pure helpers
// ═══════════════════════════════════════════════════════════════════════

func TestComputeEtag(t *testing.T) {
	assert.Empty(t, ComputeEtag(nil))

	// Same content → same etag (deterministic).
	a := ComputeEtag(map[string]int{"x": 1})
	b := ComputeEtag(map[string]int{"x": 1})
	assert.Equal(t, a, b)
	assert.Len(t, a, 32) // md5 hex

	// Different content → different etag.
	c := ComputeEtag(map[string]int{"x": 2})
	assert.NotEqual(t, a, c)

	// Unmarshalable value (channel) → "" without panicking.
	assert.Empty(t, ComputeEtag(make(chan int)))
}

func TestAgentKey(t *testing.T) {
	assert.Equal(t, "agent-1", AgentKey("", "agent-1"))
	assert.Equal(t, "app-1:agent-1", AgentKey("app-1", "agent-1"))
}

func TestGenerateDefaultConfig(t *testing.T) {
	cfg := GenerateDefaultConfig("agent-1")
	require.NotNil(t, cfg)
	assert.NotEmpty(t, cfg.Version)
	require.NotNil(t, cfg.Sampler)
	assert.Equal(t, model.SamplerTypeTraceIDRatio, cfg.Sampler.Type)
	assert.Equal(t, 1.0, cfg.Sampler.Ratio)
}

func TestNowMillis(t *testing.T) {
	before := time.Now().UnixMilli()
	got := NowMillis()
	after := time.Now().UnixMilli()
	assert.GreaterOrEqual(t, got, before)
	assert.LessOrEqual(t, got, after+2) // allow small scheduling slack
}

// ═══════════════════════════════════════════════════════════════════════
// types.go — NewConfigResponse
// ═══════════════════════════════════════════════════════════════════════

func TestNewConfigResponse(t *testing.T) {
	cfg := &model.AgentConfig{Version: "v1"}
	r := NewConfigResponse(true, cfg, "v1", "e1", "msg")
	require.NotNil(t, r)
	assert.Equal(t, LongPollTypeConfig, r.Type)
	assert.True(t, r.HasChanges)
	assert.Equal(t, cfg, r.Config)
	assert.Equal(t, "v1", r.ConfigVersion)
	assert.Equal(t, "e1", r.ConfigEtag)
	assert.Equal(t, "msg", r.Message)
}

// ═══════════════════════════════════════════════════════════════════════
// engine_adapter.go — pure converters
// ═══════════════════════════════════════════════════════════════════════

func TestEngineTypeNameToControlplane(t *testing.T) {
	cases := []struct {
		in   taskengine.TaskType
		want string
	}{
		{taskengine.TaskTypeArthasAttach, "arthas_attach"},
		{taskengine.TaskTypeArthasDetach, "arthas_detach"},
		{taskengine.TaskTypeArthasExecSync, "arthas_exec_sync"},
		{taskengine.TaskTypeArthasSessionOpen, "arthas_session_open"},
		{taskengine.TaskTypeArthasSessionExec, "arthas_session_exec"},
		{taskengine.TaskTypeArthasSessionPull, "arthas_session_pull"},
		{taskengine.TaskTypeArthasSessionClose, "arthas_session_close"},
		{"unknown:action", "unknown:action"}, // unmapped → string passthrough
	}
	for _, c := range cases {
		assert.Equal(t, c.want, engineTypeNameToControlplane(c.in), c.in)
	}
}

func TestEngineTaskToModelTask(t *testing.T) {
	assert.Nil(t, engineTaskToModelTask(nil))

	t.Run("broadcast task", func(t *testing.T) {
		et := &taskengine.Task{
			ID:        "t1",
			Type:      taskengine.TaskTypeArthasAttach,
			Payload:   jsonRaw(`{"p":1}`),
			Priority:  5,
			CreatedAt: 100,
			Timeout:   30 * time.Second,
			Routing:   taskengine.TaskRouting{Strategy: taskengine.RoutingBroadcast},
		}
		mt := engineTaskToModelTask(et)
		require.NotNil(t, mt)
		assert.Equal(t, "t1", mt.ID)
		assert.Equal(t, "arthas_attach", mt.TypeName)
		assert.Equal(t, jsonRaw(`{"p":1}`), mt.ParametersJSON)
		assert.Equal(t, int32(5), mt.PriorityNum)
		assert.Equal(t, int64(100), mt.CreatedAtMillis)
		assert.Equal(t, int64(30000), mt.TimeoutMillis)
		assert.Empty(t, mt.TargetAgentID, "broadcast has no target agent")
	})

	t.Run("direct task targets agent", func(t *testing.T) {
		et := &taskengine.Task{
			ID:      "t2",
			Type:    taskengine.TaskTypeArthasAttach,
			Routing: taskengine.TaskRouting{Strategy: taskengine.RoutingDirect, TargetNodeID: "agent-9"},
		}
		mt := engineTaskToModelTask(et)
		assert.Equal(t, "agent-9", mt.TargetAgentID)
	})
}

// jsonRaw is a tiny helper to make RawMessage literals readable.
func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }

// ═══════════════════════════════════════════════════════════════════════
// waiter_map.go — concurrency-safe waiter map
// ═══════════════════════════════════════════════════════════════════════

func TestWaiterMap_RegisterLoadDeregister(t *testing.T) {
	wm := &WaiterMap[int]{}
	assert.True(t, wm.IsEmpty())
	assert.Equal(t, 0, wm.Count())

	w1, w2 := new(int), new(int)
	*w1, *w2 = 1, 2

	wm.Register("a", w1)
	wm.Register("b", w2)
	assert.False(t, wm.IsEmpty())
	assert.Equal(t, 2, wm.Count())

	got, ok := wm.Load("a")
	require.True(t, ok)
	assert.Equal(t, 1, *got)
	_, ok = wm.Load("missing")
	assert.False(t, ok)

	// Deregister with mismatched pointer must NOT remove (stale-defer guard).
	wm.Deregister("a", w2)
	_, ok = wm.Load("a")
	assert.True(t, ok, "Deregister with wrong pointer must be a no-op")

	// Deregister with matching pointer removes.
	wm.Deregister("a", w1)
	_, ok = wm.Load("a")
	assert.False(t, ok)
	assert.Equal(t, 1, wm.Count())
}

func TestWaiterMap_Range(t *testing.T) {
	wm := &WaiterMap[string]{}
	wm.Register("a", strPtr("x"))
	wm.Register("b", strPtr("y"))

	seen := map[string]string{}
	wm.Range(func(key string, w *string) bool {
		seen[key] = *w
		return true
	})
	assert.Equal(t, map[string]string{"a": "x", "b": "y"}, seen)

	// Early stop: return false after first.
	count := 0
	wm.Range(func(_ string, _ *string) bool {
		count++
		return false
	})
	assert.Equal(t, 1, count)
}

func TestWaiterMap_Clear(t *testing.T) {
	wm := &WaiterMap[int]{}
	w1, w2 := new(int), new(int)
	wm.Register("a", w1)
	wm.Register("b", w2)

	cancelled := []string{}
	wm.Clear(func(w *int) { cancelled = append(cancelled, "x") })
	assert.True(t, wm.IsEmpty(), "Clear must remove all waiters")
	assert.Len(t, cancelled, 2)

	// Clear with nil cancelFn is safe.
	wm.Register("c", new(int))
	wm.Clear(nil)
	assert.True(t, wm.IsEmpty())
}

func TestWaiterMap_ConcurrentAccess(t *testing.T) {
	wm := &WaiterMap[int]{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p := new(int)
			*p = n
			wm.Register(string(rune('a'+n%26)), p)
			wm.Load(string(rune('a' + n%26)))
			wm.Count()
		}(i)
	}
	wg.Wait()
}

func strPtr(s string) *string { return &s }

// ═══════════════════════════════════════════════════════════════════════
// config_handler.go — computeBusinessEtagFromModel (pure)
// ═══════════════════════════════════════════════════════════════════════

func TestComputeBusinessEtagFromModel(t *testing.T) {
	assert.Empty(t, computeBusinessEtagFromModel(nil))

	cfg := &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}

	// ETag must ignore volatile fields (Version/UpdatedAt/Etag/ServerMetadata):
	// two configs differing only in those must produce the same business etag.
	a := computeBusinessEtagFromModel(cfg)
	cfg2 := &model.AgentConfig{Version: "v2", UpdatedAt: 999, Etag: "ignored", Sampler: &model.SamplerConfig{Ratio: 0.5}}
	b := computeBusinessEtagFromModel(cfg2)
	assert.Equal(t, a, b, "volatile fields must not affect business etag")

	// A genuine business change (sampler ratio) changes the etag.
	cfg3 := &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.9}}
	c := computeBusinessEtagFromModel(cfg3)
	assert.NotEqual(t, a, c)
}

// ═══════════════════════════════════════════════════════════════════════
// manager.go — with fake LongPollHandler
// ═══════════════════════════════════════════════════════════════════════

// fakeHandler is a controllable LongPollHandler for Manager tests. It embeds
// nothing (the interface is small) and returns scripted results.
type fakeHandler struct {
	hType     LongPollType
	result    *HandlerResult
	err       error
	pollCalls int
	delay     time.Duration
}

func (f *fakeHandler) GetType() LongPollType { return f.hType }
func (f *fakeHandler) ShouldContinue() bool  { return true }
func (f *fakeHandler) Start(_ context.Context) error { return nil }
func (f *fakeHandler) Stop() error            { return nil }
func (f *fakeHandler) CheckImmediate(_ context.Context, _ *PollRequest) (bool, *HandlerResult, error) {
	return false, nil, nil
}
func (f *fakeHandler) Poll(ctx context.Context, _ *PollRequest) (*HandlerResult, error) {
	f.pollCalls++
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
		}
	}
	return f.result, f.err
}

func TestDefaultManagerConfig(t *testing.T) {
	cfg := DefaultManagerConfig()
	assert.Equal(t, DefaultMaxTimeout, cfg.MaxTimeout)
	assert.Equal(t, DefaultTimeout, cfg.DefaultTimeout)
}

func TestNewManager_AppliesDefaults(t *testing.T) {
	m := NewManager(zap.NewNop(), ManagerConfig{})
	assert.Equal(t, DefaultMaxTimeout, m.config.MaxTimeout)
	assert.Equal(t, DefaultTimeout, m.config.DefaultTimeout)
}

func TestManager_RegisterUnregisterGetHandler(t *testing.T) {
	m := NewManager(zap.NewNop(), DefaultManagerConfig())
	h := &fakeHandler{hType: LongPollTypeConfig}

	require.NoError(t, m.RegisterHandler(h))
	_, ok := m.GetHandler(LongPollTypeConfig)
	assert.True(t, ok)

	// Duplicate registration replaces the existing handler (logged as Warn, no error).
	require.NoError(t, m.RegisterHandler(&fakeHandler{hType: LongPollTypeConfig}))

	assert.True(t, m.UnregisterHandler(LongPollTypeConfig))
	_, ok = m.GetHandler(LongPollTypeConfig)
	assert.False(t, ok)
	assert.False(t, m.UnregisterHandler(LongPollTypeConfig), "second unregister is false")
}

func TestManager_GetRegisteredTypes(t *testing.T) {
	m := NewManager(zap.NewNop(), DefaultManagerConfig())
	_ = m.RegisterHandler(&fakeHandler{hType: LongPollTypeConfig})
	_ = m.RegisterHandler(&fakeHandler{hType: LongPollTypeTask})
	types := m.GetRegisteredTypes()
	assert.ElementsMatch(t, []LongPollType{LongPollTypeConfig, LongPollTypeTask}, types)
}

func TestManager_Poll_NoHandlers(t *testing.T) {
	m := NewManager(zap.NewNop(), DefaultManagerConfig())
	resp, err := m.Poll(context.Background(), &PollRequest{TimeoutMillis: 100})
	require.NoError(t, err)
	assert.False(t, resp.HasAnyChanges)
	assert.Contains(t, resp.Message, "no handlers")
}

func TestManager_PollSingle_HandlerNotFound(t *testing.T) {
	m := NewManager(zap.NewNop(), DefaultManagerConfig())
	_, err := m.PollSingle(context.Background(), &PollRequest{TimeoutMillis: 100}, LongPollTypeConfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler not found")
}

func TestManager_Poll_EarlyReturnOnChange(t *testing.T) {
	m := NewManager(zap.NewNop(), DefaultManagerConfig())
	// One handler reports a change immediately → early return, HasAnyChanges true.
	_ = m.RegisterHandler(&fakeHandler{
		hType:  LongPollTypeConfig,
		result: &HandlerResult{HasChanges: true, Response: &PollResponse{Type: LongPollTypeConfig, HasChanges: true}},
	})

	resp, err := m.Poll(context.Background(), &PollRequest{TimeoutMillis: 500})
	require.NoError(t, err)
	assert.True(t, resp.HasAnyChanges)
	require.Contains(t, resp.Results, LongPollTypeConfig)
}

func TestManager_Poll_TimeoutNoChanges(t *testing.T) {
	m := NewManager(zap.NewNop(), ManagerConfig{MaxTimeout: time.Second, DefaultTimeout: 100 * time.Millisecond})
	// Handler blocks longer than the timeout → ctx done → no changes.
	_ = m.RegisterHandler(&fakeHandler{
		hType: LongPollTypeConfig,
		delay: 500 * time.Millisecond,
		result: &HandlerResult{HasChanges: false, Response: NoChangeResponse(LongPollTypeConfig)},
	})

	resp, err := m.Poll(context.Background(), &PollRequest{TimeoutMillis: 100})
	require.NoError(t, err)
	assert.False(t, resp.HasAnyChanges)
}

func TestManager_Poll_HandlerError(t *testing.T) {
	m := NewManager(zap.NewNop(), ManagerConfig{MaxTimeout: time.Second, DefaultTimeout: 100 * time.Millisecond})
	_ = m.RegisterHandler(&fakeHandler{
		hType: LongPollTypeConfig,
		err:   errors.New("boom"),
	})
	resp, err := m.Poll(context.Background(), &PollRequest{TimeoutMillis: 100})
	require.NoError(t, err, "handler errors are logged, not propagated")
	assert.False(t, resp.HasAnyChanges)
}

func TestManager_PollSingle_OK(t *testing.T) {
	m := NewManager(zap.NewNop(), DefaultManagerConfig())
	_ = m.RegisterHandler(&fakeHandler{
		hType:  LongPollTypeTask,
		result: &HandlerResult{HasChanges: true, Response: &PollResponse{Type: LongPollTypeTask, HasChanges: true}},
	})
	resp, err := m.PollSingle(context.Background(), &PollRequest{TimeoutMillis: 500}, LongPollTypeTask)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.HasChanges)
}

func TestManager_PollSingle_NilResult_NoChange(t *testing.T) {
	m := NewManager(zap.NewNop(), DefaultManagerConfig())
	_ = m.RegisterHandler(&fakeHandler{hType: LongPollTypeConfig, result: nil})
	resp, err := m.PollSingle(context.Background(), &PollRequest{TimeoutMillis: 100}, LongPollTypeConfig)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.False(t, resp.HasChanges)
}

func TestManager_StartStop(t *testing.T) {
	m := NewManager(zap.NewNop(), DefaultManagerConfig())
	require.NoError(t, m.Start(context.Background()))
	// Start is idempotent (no error, no double-start goroutine).
	require.NoError(t, m.Start(context.Background()))
	m.Stop()
}

func TestManager_NormalizeTimeout(t *testing.T) {
	m := NewManager(zap.NewNop(), ManagerConfig{MaxTimeout: 60 * time.Second, DefaultTimeout: 30 * time.Second})
	assert.Equal(t, 30*time.Second, m.normalizeTimeout(0), "0 → default")
	assert.Equal(t, MinTimeout, m.normalizeTimeout(100), "below min → min (100ms)")
	assert.Equal(t, 60*time.Second, m.normalizeTimeout(120000), "above max → max (120s)")
	assert.Equal(t, 5*time.Second, m.normalizeTimeout(5000), "within range → as-is (5s)")
}

func TestManager_GetHandlersToPoll(t *testing.T) {
	m := NewManager(zap.NewNop(), DefaultManagerConfig())
	_ = m.RegisterHandler(&fakeHandler{hType: LongPollTypeConfig})
	_ = m.RegisterHandler(&fakeHandler{hType: LongPollTypeTask})

	// No types → all handlers.
	all := m.getHandlersToPoll(nil)
	assert.Len(t, all, 2)

	// Specific type → only that one.
	one := m.getHandlersToPoll([]LongPollType{LongPollTypeConfig})
	assert.Len(t, one, 1)
}
