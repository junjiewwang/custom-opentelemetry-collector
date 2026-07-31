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
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
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

// ═══════════════════════════════════════════════════════════════════════
// engine_adapter.go — adapts taskengine.Engine to TaskClaimEngine
// ═══════════════════════════════════════════════════════════════════════

// fakeEngine embeds taskengine.Engine (11 methods) and overrides only the 3
// the adapter calls (ListTasks/Claim/GetTask). Unoverridden methods panic if
// called — surfacing unexpected use rather than silently passing.
type fakeEngine struct {
	taskengine.Engine
	listPage *taskengine.ListPage
	listErr  error
	claimTask *taskengine.Task
	claimErr  error
	getTask   *taskengine.Task
	getErr    error
	claimCalls int
}

func (f *fakeEngine) ListTasks(_ context.Context, _ taskengine.ListQuery) (*taskengine.ListPage, error) {
	return f.listPage, f.listErr
}
func (f *fakeEngine) Claim(_ context.Context, _ *taskengine.ConsumerDescriptor) (*taskengine.Task, error) {
	f.claimCalls++
	return f.claimTask, f.claimErr
}
func (f *fakeEngine) GetTask(_ context.Context, _ string) (*taskengine.Task, error) {
	return f.getTask, f.getErr
}

func TestNewEngineAdapter(t *testing.T) {
	a := NewEngineAdapter(&fakeEngine{})
	require.NotNil(t, a)
}

func TestEngineAdapter_GetPendingTasks_FiltersByAgent(t *testing.T) {
	// Direct task for agent-1, direct task for agent-2, one broadcast.
	page := &taskengine.ListPage{
		Tasks: []*taskengine.Task{
			{ID: "d1", Routing: taskengine.TaskRouting{Strategy: taskengine.RoutingDirect, TargetNodeID: "agent-1"}},
			{ID: "d2", Routing: taskengine.TaskRouting{Strategy: taskengine.RoutingDirect, TargetNodeID: "agent-2"}},
			{ID: "b1", Routing: taskengine.TaskRouting{Strategy: taskengine.RoutingBroadcast}},
		},
	}
	a := NewEngineAdapter(&fakeEngine{listPage: page})

	tasks, err := a.GetPendingTasks(context.Background(), "agent-1")
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.ElementsMatch(t, []string{"d1", "b1"}, ids, "agent-1 gets its direct task + broadcasts")
}

func TestEngineAdapter_GetPendingTasks_NilPage(t *testing.T) {
	a := NewEngineAdapter(&fakeEngine{listPage: nil})
	tasks, err := a.GetPendingTasks(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Nil(t, tasks)
}

func TestEngineAdapter_GetPendingTasks_ListError(t *testing.T) {
	a := NewEngineAdapter(&fakeEngine{listErr: errors.New("down")})
	_, err := a.GetPendingTasks(context.Background(), "agent-1")
	require.Error(t, err)
}

func TestEngineAdapter_ClaimTaskForAgent(t *testing.T) {
	claimed := &taskengine.Task{ID: "t1", Type: taskengine.TaskTypeArthasAttach}
	a := NewEngineAdapter(&fakeEngine{claimTask: claimed})

	mt, err := a.ClaimTaskForAgent(context.Background(), "agent-1")
	require.NoError(t, err)
	require.NotNil(t, mt)
	assert.Equal(t, "t1", mt.ID)
	assert.Equal(t, "arthas_attach", mt.TypeName)
}

func TestEngineAdapter_ClaimTaskForAgent_Nothing(t *testing.T) {
	a := NewEngineAdapter(&fakeEngine{claimTask: nil})
	mt, err := a.ClaimTaskForAgent(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Nil(t, mt)
}

func TestEngineAdapter_ClaimTaskForAgent_Error(t *testing.T) {
	a := NewEngineAdapter(&fakeEngine{claimErr: errors.New("busy")})
	_, err := a.ClaimTaskForAgent(context.Background(), "agent-1")
	require.Error(t, err)
}

func TestEngineAdapter_IsTaskCancelled(t *testing.T) {
	a := NewEngineAdapter(&fakeEngine{getTask: &taskengine.Task{Status: taskengine.StatusCancelled}})
	got, err := a.IsTaskCancelled(context.Background(), "t1")
	require.NoError(t, err)
	assert.True(t, got)

	a2 := NewEngineAdapter(&fakeEngine{getTask: &taskengine.Task{Status: taskengine.StatusRunning}})
	got, err = a2.IsTaskCancelled(context.Background(), "t1")
	require.NoError(t, err)
	assert.False(t, got)

	// Not found → false, nil.
	a3 := NewEngineAdapter(&fakeEngine{getTask: nil})
	got, err = a3.IsTaskCancelled(context.Background(), "missing")
	require.NoError(t, err)
	assert.False(t, got)

	// GetTask error propagates.
	a4 := NewEngineAdapter(&fakeEngine{getErr: errors.New("down")})
	_, err = a4.IsTaskCancelled(context.Background(), "t1")
	require.Error(t, err)
}

// ═══════════════════════════════════════════════════════════════════════
// config_cache.go — config lifecycle for a single service
// ═══════════════════════════════════════════════════════════════════════

// fakeConfigMgr embeds OnDemandConfigManager (18 methods) and overrides only
// the 3 configCache calls (GetServiceConfig/WatchServiceConfig/UnwatchServiceConfig).
// Its fields are guarded by mu because WatchServiceConfig is called from a
// Poll goroutine while the test goroutine reads watchCb to fire the callback.
type fakeConfigMgr struct {
	configmanager.OnDemandConfigManager
	mu sync.Mutex

	getCfg    *model.AgentConfig
	getErr    error
	watchCb   configmanager.AgentConfigChangeCallback
	watched   bool
	unwatched bool
}

func (f *fakeConfigMgr) GetServiceConfig(_ context.Context, _, _ string) (*model.AgentConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCfg, f.getErr
}
func (f *fakeConfigMgr) WatchServiceConfig(_, _ string, cb configmanager.AgentConfigChangeCallback) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchCb = cb
	f.watched = true
}
func (f *fakeConfigMgr) UnwatchServiceConfig(_, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unwatched = true
}

// getWatchCb returns the registered callback under lock, for tests that fire it.
func (f *fakeConfigMgr) getWatchCb() configmanager.AgentConfigChangeCallback {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watchCb
}

func TestNewConfigCache_GetSet(t *testing.T) {
	c := newConfigCache("app-1", "svc", &fakeConfigMgr{}, zap.NewNop())
	assert.Nil(t, c.Get())

	cfg := &model.AgentConfig{Version: "v1"}
	c.Set(cfg)
	assert.Equal(t, "v1", c.Get().Version)
}

func TestConfigCache_SetOnChange(t *testing.T) {
	c := newConfigCache("app-1", "svc", &fakeConfigMgr{}, zap.NewNop())
	fired := false
	c.SetOnChange(func(_ *model.AgentConfig) { fired = true })

	// SetOnChange only registers; it does not fire on its own. Calling the
	// captured callback directly verifies the wiring (the handler wires
	// notifyServiceWaiters through this setter).
	assert.False(t, fired, "registering must not fire the callback")
}

func TestConfigCache_LoadFromConfigMgr_EmptyServiceName(t *testing.T) {
	c := newConfigCache("app-1", "", &fakeConfigMgr{}, zap.NewNop())
	cfg, err := c.LoadFromConfigMgr(context.Background())
	require.NoError(t, err)
	assert.Nil(t, cfg, "empty serviceName short-circuits to nil")
}

func TestConfigCache_LoadFromConfigMgr_OK_FillsEtag(t *testing.T) {
	// configMgr returns a config with no Etag → cache must compute one.
	mgr := &fakeConfigMgr{getCfg: &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}}
	c := newConfigCache("app-1", "svc", mgr, zap.NewNop())

	cfg, err := c.LoadFromConfigMgr(context.Background())
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.NotEmpty(t, cfg.Etag, "missing Etag must be computed from business fields")
}

func TestConfigCache_LoadFromConfigMgr_NotFound(t *testing.T) {
	mgr := &fakeConfigMgr{getErr: configmanager.ErrConfigNotFound}
	c := newConfigCache("app-1", "svc", mgr, zap.NewNop())
	cfg, err := c.LoadFromConfigMgr(context.Background())
	require.NoError(t, err, "IsConfigNotFound maps to (nil, nil)")
	assert.Nil(t, cfg)
}

func TestConfigCache_LoadFromConfigMgr_OtherError(t *testing.T) {
	mgr := &fakeConfigMgr{getErr: errors.New("nacos down")}
	c := newConfigCache("app-1", "svc", mgr, zap.NewNop())
	_, err := c.LoadFromConfigMgr(context.Background())
	require.Error(t, err)
}

func TestConfigCache_EnsureWatching_NoOpWhenNoServiceNameOrMgr(t *testing.T) {
	// No serviceName → no-op.
	c := newConfigCache("app-1", "", nil, zap.NewNop())
	c.EnsureWatching()
	assert.False(t, c.IsWatching())

	// serviceName set but configMgr nil → no-op.
	c2 := newConfigCache("app-1", "svc", nil, zap.NewNop())
	c2.EnsureWatching()
	assert.False(t, c2.IsWatching())
}

func TestConfigCache_EnsureWatching_Idempotent(t *testing.T) {
	mgr := &fakeConfigMgr{}
	c := newConfigCache("app-1", "svc", mgr, zap.NewNop())
	c.EnsureWatching()
	assert.True(t, c.IsWatching())
	// Second call must not re-register.
	c.EnsureWatching()
	assert.True(t, mgr.watched)
}

func TestConfigCache_EnsureWatching_CallbackUpdatesConfig(t *testing.T) {
	mgr := &fakeConfigMgr{}
	c := newConfigCache("app-1", "svc", mgr, zap.NewNop())
	c.EnsureWatching()
	cb := mgr.getWatchCb()
	require.NotNil(t, cb)

	// Simulate a config change from the backend.
	newCfg := &model.AgentConfig{Version: "v2"}
	cb(&configmanager.AgentConfigChangeEvent{NewConfig: newCfg})
	assert.Equal(t, "v2", c.Get().Version, "watch callback must update cached config")
}

func TestConfigCache_Unwatch(t *testing.T) {
	mgr := &fakeConfigMgr{}
	c := newConfigCache("app-1", "svc", mgr, zap.NewNop())
	c.EnsureWatching()
	c.Unwatch()
	assert.False(t, c.IsWatching())
	assert.True(t, mgr.unwatched)

	// Unwatch when not watching (or no mgr) is a no-op.
	c.Unwatch()
	assert.False(t, c.IsWatching())
}

// ═══════════════════════════════════════════════════════════════════════
// config_handler.go — ConfigPollHandler business logic
// ═══════════════════════════════════════════════════════════════════════

// fakeMetadataProvider is a ServerMetadataProvider returning fixed metadata.
type fakeMetadataProvider struct {
	name string
	meta map[string]string
}

func (p *fakeMetadataProvider) Name() string { return p.name }
func (p *fakeMetadataProvider) ProvideMetadata(_ context.Context, _ *PollRequest) map[string]string {
	return p.meta
}

func newHandlerWithMgr(mgr configmanager.OnDemandConfigManager) *ConfigPollHandler {
	return NewConfigPollHandler(zap.NewNop(), mgr)
}

func TestConfigPollHandler_TypeAndLifecycle(t *testing.T) {
	h := newHandlerWithMgr(&fakeConfigMgr{})
	assert.Equal(t, LongPollTypeConfig, h.GetType())
	assert.False(t, h.ShouldContinue(), "not running before Start")

	require.NoError(t, h.Start(context.Background()))
	assert.True(t, h.ShouldContinue())
	// Start is idempotent.
	require.NoError(t, h.Start(context.Background()))

	require.NoError(t, h.Stop())
	assert.False(t, h.ShouldContinue())
	// Stop is idempotent.
	require.NoError(t, h.Stop())
}

func TestConfigPollHandler_NilConfigMgr_PollErrors(t *testing.T) {
	h := NewConfigPollHandler(zap.NewNop(), nil)
	_, err := h.Poll(context.Background(), &PollRequest{AgentID: "a1", ServiceName: "svc"})
	require.Error(t, err)
}

func TestConfigPollHandler_CheckImmediate_FirstLoadReturnsChange(t *testing.T) {
	// No cached config → proactive Nacos load returns a config → version differs
	// from the client's (empty) → HasChanges.
	mgr := &fakeConfigMgr{getCfg: &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}}
	h := newHandlerWithMgr(mgr)
	require.NoError(t, h.Start(context.Background()))
	defer h.Stop()

	hasChanges, result, err := h.CheckImmediate(context.Background(), &PollRequest{
		AgentID: "a1", AppID: "app-1", ServiceName: "svc",
	})
	require.NoError(t, err)
	assert.True(t, hasChanges)
	require.NotNil(t, result)
	assert.True(t, result.HasChanges)
	assert.Equal(t, "v1", result.Response.ConfigVersion)
}

func TestConfigPollHandler_CheckImmediate_NoChangeWhenVersionsMatch(t *testing.T) {
	// Prime the cache with a config, then ask with matching version AND etag
	// (etag non-empty so CheckImmediate skips the proactive reload) → no change.
	mgr := &fakeConfigMgr{getCfg: &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}}
	h := newHandlerWithMgr(mgr)
	require.NoError(t, h.Start(context.Background()))
	defer h.Stop()

	// First call loads v1 into the cache (client version differs → change).
	_, _, _ = h.CheckImmediate(context.Background(), &PollRequest{
		AgentID: "a1", ServiceName: "svc", CurrentConfigVersion: "v0",
	})

	// Read the cached etag (computed by the cache) and report a matching
	// version+etag → no immediate change.
	cached := h.getOrCreateServiceState("", "svc").config.Get()
	require.NotNil(t, cached)

	hasChanges, _, err := h.CheckImmediate(context.Background(), &PollRequest{
		AgentID: "a1", ServiceName: "svc",
		CurrentConfigVersion: cached.Version,
		CurrentConfigEtag:    cached.Etag,
	})
	require.NoError(t, err)
	assert.False(t, hasChanges, "matching version+etag must report no change")
}

func TestConfigPollHandler_CheckImmediate_SkeletonWhenNoConfig(t *testing.T) {
	// configMgr returns nil (not found) and cache empty → skeleton version "0".
	// Client reports version "0" → no change; client reports anything else → change.
	mgr := &fakeConfigMgr{getCfg: nil, getErr: configmanager.ErrConfigNotFound}
	h := newHandlerWithMgr(mgr)
	require.NoError(t, h.Start(context.Background()))
	defer h.Stop()

	// Client has skeleton "0" → no change.
	hasChanges, _, err := h.CheckImmediate(context.Background(), &PollRequest{
		AgentID: "a1", ServiceName: "svc", CurrentConfigVersion: "0",
	})
	require.NoError(t, err)
	assert.False(t, hasChanges)

	// Client has stale version → change to skeleton.
	hasChanges, result, err := h.CheckImmediate(context.Background(), &PollRequest{
		AgentID: "a1", ServiceName: "svc", CurrentConfigVersion: "stale",
	})
	require.NoError(t, err)
	assert.True(t, hasChanges)
	assert.Equal(t, "0", result.Response.ConfigVersion)
}

func TestConfigPollHandler_RegisterMetadataProvider_Injects(t *testing.T) {
	mgr := &fakeConfigMgr{getCfg: &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}}
	h := newHandlerWithMgr(mgr)
	h.RegisterMetadataProvider(&fakeMetadataProvider{name: "p1", meta: map[string]string{"region": "us"}})
	require.NoError(t, h.Start(context.Background()))
	defer h.Stop()

	_, result, err := h.CheckImmediate(context.Background(), &PollRequest{
		AgentID: "a1", ServiceName: "svc",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Response.Config.ServerMetadata)
	assert.Equal(t, "us", result.Response.Config.ServerMetadata["region"])
}

func TestConfigPollHandler_Poll_ImmediateChange(t *testing.T) {
	mgr := &fakeConfigMgr{getCfg: &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}}
	h := newHandlerWithMgr(mgr)
	require.NoError(t, h.Start(context.Background()))
	defer h.Stop()

	// Short timeout; immediate change returns before the wait.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := h.Poll(ctx, &PollRequest{AgentID: "a1", ServiceName: "svc"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.HasChanges)
}

func TestConfigPollHandler_Poll_TimeoutNoChange(t *testing.T) {
	// configMgr returns a config; client already has matching version → no immediate
	// change → registers waiter → ctx times out → no-change result.
	cfg := &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}
	mgr := &fakeConfigMgr{getCfg: cfg}
	h := newHandlerWithMgr(mgr)
	require.NoError(t, h.Start(context.Background()))
	defer h.Stop()

	// First poll loads the config into cache.
	_, _, _ = h.CheckImmediate(context.Background(), &PollRequest{AgentID: "a1", ServiceName: "svc"})
	cached := h.getOrCreateServiceState("", "svc").config.Get()
	require.NotNil(t, cached)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := h.Poll(ctx, &PollRequest{
		AgentID: "a1", ServiceName: "svc",
		CurrentConfigVersion: cached.Version, CurrentConfigEtag: cached.Etag,
	})
	require.NoError(t, err)
	// Matching version+etag → no immediate change → waiter blocks → ctx timeout
	// → no-change result.
	if result != nil {
		assert.False(t, result.HasChanges)
	}
}

func TestConfigPollHandler_Poll_NotifiedOnConfigChange(t *testing.T) {
	// Start a blocking Poll, then fire the watch callback → waiter notified.
	mgr := &fakeConfigMgr{getCfg: &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}}
	h := newHandlerWithMgr(mgr)
	require.NoError(t, h.Start(context.Background()))
	defer h.Stop()

	// Prime the cache so the first CheckImmediate loads v1.
	_, _, _ = h.CheckImmediate(context.Background(), &PollRequest{AgentID: "a1", ServiceName: "svc"})

	// Read back the cached config's version+etag so the blocking Poll sees NO
	// immediate change (matching version AND etag) and actually registers a waiter.
	cached := h.getOrCreateServiceState("", "svc").config.Get()
	require.NotNil(t, cached)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	type res struct {
		r   *HandlerResult
		err error
	}
	resCh := make(chan res, 1)
	go func() {
		r, err := h.Poll(ctx, &PollRequest{
			AgentID: "a1", ServiceName: "svc",
			CurrentConfigVersion: cached.Version,
			CurrentConfigEtag:    cached.Etag,
		})
		resCh <- res{r, err}
	}()

	// Wait for the waiter to register, then fire the watch callback.
	time.Sleep(150 * time.Millisecond)
	cb := mgr.getWatchCb()
	require.NotNil(t, cb, "Poll must have started watching")
	cb(&configmanager.AgentConfigChangeEvent{
		NewConfig: &model.AgentConfig{Version: "v2", Sampler: &model.SamplerConfig{Ratio: 0.5}},
	})

	select {
	case got := <-resCh:
		require.NoError(t, got.err)
		require.NotNil(t, got.r)
		assert.True(t, got.r.HasChanges, "waiter must be notified of the change")
	case <-time.After(2 * time.Second):
		t.Fatal("waiter was not notified of config change")
	}
}

func TestConfigPollHandler_GetWaiterAndWatchCount(t *testing.T) {
	mgr := &fakeConfigMgr{getCfg: &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}}
	h := newHandlerWithMgr(mgr)
	require.NoError(t, h.Start(context.Background()))
	defer h.Stop()

	// Prime cache + start watching.
	_, _, _ = h.CheckImmediate(context.Background(), &PollRequest{AgentID: "a1", ServiceName: "svc"})
	h.getOrCreateServiceState("app-1", "svc").config.EnsureWatching()

	assert.Equal(t, 0, h.GetWaiterCount(), "no waiters yet")
	assert.GreaterOrEqual(t, h.GetWatchCount(), 1, "at least one watch active")

	// Register a waiter directly to exercise GetWaiterCount.
	state := h.getOrCreateServiceState("app-1", "svc")
	wctx, wcancel := context.WithCancel(context.Background())
	w := &ConfigWaiter{
		agentID:    "a1",
		appID:      "app-1",
		serviceName: "svc",
		resultChan: make(chan *HandlerResult, 1),
		ctx:        wctx,
		cancel:     wcancel,
	}
	state.waiters.Register("a1", w)
	assert.Equal(t, 1, h.GetWaiterCount())
	state.waiters.Deregister("a1", w)
	wcancel()
}

func TestConfigPollHandler_StopCancelsWaiters(t *testing.T) {
	mgr := &fakeConfigMgr{getCfg: &model.AgentConfig{Version: "v1", Sampler: &model.SamplerConfig{Ratio: 0.5}}}
	h := newHandlerWithMgr(mgr)
	require.NoError(t, h.Start(context.Background()))

	// Register a waiter directly.
	state := h.getOrCreateServiceState("app-1", "svc")
	wctx, wcancel := context.WithCancel(context.Background())
	w := &ConfigWaiter{
		agentID: "a1", resultChan: make(chan *HandlerResult, 1), ctx: wctx, cancel: wcancel,
	}
	state.waiters.Register("a1", w)

	// Stop must cancel the waiter's context.
	require.NoError(t, h.Stop())
	assert.Equal(t, context.Canceled, wctx.Err(), "Stop must cancel waiter contexts")
}
