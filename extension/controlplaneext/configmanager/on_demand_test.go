// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	otelmodel "go.opentelemetry.io/collector/custom/controlplane/model"
)

// ── mockNacosClient ──────────────────────────────────────────────────────

type mockNacosClient struct {
	mu        sync.Mutex
	configs   map[string]string // "group:dataId" → content
	listening map[string]vo.ConfigParam
}

func newMockNacosClient() *mockNacosClient {
	return &mockNacosClient{
		configs:   make(map[string]string),
		listening: make(map[string]vo.ConfigParam),
	}
}

func key(group, dataID string) string { return group + ":" + dataID }

func (m *mockNacosClient) GetConfig(param vo.ConfigParam) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	content, ok := m.configs[key(param.Group, param.DataId)]
	if !ok {
		return "", errors.New("config not found")
	}
	return content, nil
}

func (m *mockNacosClient) PublishConfig(param vo.ConfigParam) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[key(param.Group, param.DataId)] = param.Content
	return true, nil
}

func (m *mockNacosClient) DeleteConfig(param vo.ConfigParam) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.configs, key(param.Group, param.DataId))
	return true, nil
}

func (m *mockNacosClient) ListenConfig(params vo.ConfigParam) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listening[params.DataId] = params
	return nil
}

func (m *mockNacosClient) CancelListenConfig(params vo.ConfigParam) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.listening, params.DataId)
	return nil
}

func (m *mockNacosClient) SearchConfig(param vo.SearchConfigParam) (*model.ConfigPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var items []model.ConfigItem
	for k, v := range m.configs {
		// key format is "group:dataId" — extract just the dataId part
		parts := splitKey(k)
		dataID := parts[1]
		// Only return items matching the search group
		if param.Group != "" && parts[0] != param.Group {
			continue
		}
		items = append(items, model.ConfigItem{Group: parts[0], DataId: dataID, Content: v})
	}
	return &model.ConfigPage{TotalCount: len(items), PageItems: items, PagesAvailable: 1}, nil
}

func splitKey(k string) [2]string {
	for i := 0; i < len(k); i++ {
		if k[i] == ':' {
			return [2]string{k[:i], k[i+1:]}
		}
	}
	return [2]string{"", k}
}

func (m *mockNacosClient) CloseClient() {}

var _ config_client.IConfigClient = (*mockNacosClient)(nil)

// ── helpers ─────────────────────────────────────────────────────────────

func newTestOnDemandManager(t *testing.T, client *mockNacosClient) *NacosOnDemandConfigManager {
	t.Helper()
	cfg := DefaultOnDemandConfig()
	cfg.CacheExpiration = 100 * time.Millisecond // short for testing
	cfg.CleanupInterval = 50 * time.Millisecond
	mgr, err := NewNacosOnDemandConfigManager(zaptest.NewLogger(t), cfg, nil, client)
	require.NoError(t, err)
	require.NoError(t, mgr.Start(context.Background()))
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

func testAgentConfig(version string) *otelmodel.AgentConfig {
	return &otelmodel.AgentConfig{Version: version}
}

// ── Constructor ────────────────────────────────────────────────────────

func TestNewNacosOnDemandConfigManager_NilClient(t *testing.T) {
	_, err := NewNacosOnDemandConfigManager(zap.NewNop(), DefaultOnDemandConfig(), nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nacos client is required")
}

func TestNewNacosOnDemandConfigManager_Defaults(t *testing.T) {
	mgr, err := NewNacosOnDemandConfigManager(zap.NewNop(), OnDemandConfig{}, nil, newMockNacosClient())
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, mgr.config.LoadTimeout)
	assert.Equal(t, 3, mgr.config.MaxRetries)
	assert.Equal(t, 1*time.Second, mgr.config.RetryInterval)
	assert.Equal(t, 5*time.Minute, mgr.config.CacheExpiration)
	assert.Equal(t, 1*time.Minute, mgr.config.CleanupInterval)
}

// ── RegisterAgent / GetConfigForAgent ─────────────────────────────────

func TestRegisterAgent_LoadsServiceConfig(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "order-service")] = string(cfgJSON)

	mgr := newTestOnDemandManager(t, client)
	config, err := mgr.RegisterAgent(context.Background(), "app-1", "agent-1", "order-service")
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "v1", config.Version)
}

func TestRegisterAgent_Validation(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	_, err := mgr.RegisterAgent(context.Background(), "", "agent-1", "svc")
	assert.Error(t, err)
	_, err = mgr.RegisterAgent(context.Background(), "app-1", "", "svc")
	assert.Error(t, err)
}

func TestRegisterAgent_NoConfig(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	config, err := mgr.RegisterAgent(context.Background(), "app-1", "agent-1", "no-config-svc")
	require.NoError(t, err) // no config is not an error
	assert.Nil(t, config)
}

func TestGetConfigForAgent_CacheHit(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)

	mgr := newTestOnDemandManager(t, client)

	// First call loads from Nacos (cache miss)
	config1, err := mgr.GetConfigForAgent(context.Background(), "app-1", "agent-1", "svc")
	require.NoError(t, err)
	require.NotNil(t, config1)
	assert.Equal(t, "v1", config1.Version)

	// Second call should hit cache
	config2, err := mgr.GetConfigForAgent(context.Background(), "app-1", "agent-1", "svc")
	require.NoError(t, err)
	assert.Equal(t, config1.Version, config2.Version)

	// Verify cache stats
	stats := mgr.GetCacheStats()
	assert.Equal(t, int64(1), stats.CacheHits)
	assert.Equal(t, int64(1), stats.CacheMisses)
}

func TestGetConfigForAgent_EmptyServiceName(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	config, err := mgr.GetConfigForAgent(context.Background(), "app-1", "agent-1", "")
	require.NoError(t, err)
	assert.Nil(t, config) // empty serviceName → no config, no error
}

func TestGetConfigForAgent_EmptyAppID(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	_, err := mgr.GetConfigForAgent(context.Background(), "", "agent-1", "svc")
	assert.Error(t, err)
}

// ── UnregisterAgent ───────────────────────────────────────────────────

func TestUnregisterAgent(t *testing.T) {
	client := newMockNacosClient()
	mgr := newTestOnDemandManager(t, client)

	mgr.RegisterAgent(context.Background(), "app-1", "agent-1", "svc")
	assert.True(t, mgr.isAgentRegistered("app-1", "agent-1"))

	require.NoError(t, mgr.UnregisterAgent(context.Background(), "app-1", "agent-1"))
	assert.False(t, mgr.isAgentRegistered("app-1", "agent-1"))
}

func TestUnregisterAgent_Validation(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	assert.Error(t, mgr.UnregisterAgent(context.Background(), "", "agent-1"))
	assert.Error(t, mgr.UnregisterAgent(context.Background(), "app-1", ""))
}

func TestUnregisterAgent_LastAgentRemovesAppEntry(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	mgr.RegisterAgent(context.Background(), "app-1", "a1", "svc")
	mgr.RegisterAgent(context.Background(), "app-1", "a2", "svc")

	mgr.UnregisterAgent(context.Background(), "app-1", "a1")
	assert.True(t, mgr.isAgentRegistered("app-1", "a2"))
	_, ok := mgr.registeredAgents.Load("app-1")
	assert.True(t, ok) // app-1 still has a2

	mgr.UnregisterAgent(context.Background(), "app-1", "a2")
	_, ok = mgr.registeredAgents.Load("app-1")
	assert.False(t, ok) // app-1 removed
}

// ── SetServiceConfig / GetServiceConfig ────────────────────────────────

func TestSetServiceConfig(t *testing.T) {
	client := newMockNacosClient()
	mgr := newTestOnDemandManager(t, client)

	cfg := testAgentConfig("v2")
	require.NoError(t, mgr.SetServiceConfig(context.Background(), "app-1", "order-service", cfg))

	// Verify it was published to Nacos (SetServiceConfig auto-generates version as v{timestamp})
	content := client.configs[key("app-1", "order-service")]
	assert.NotEmpty(t, content)
	var stored otelmodel.AgentConfig
	require.NoError(t, json.Unmarshal([]byte(content), &stored))
	assert.True(t, len(stored.Version) > 0)
	assert.NotEqual(t, "v2", stored.Version) // version is auto-generated, not the input
}

func TestGetServiceConfig_AfterSet(t *testing.T) {
	client := newMockNacosClient()
	mgr := newTestOnDemandManager(t, client)

	cfg := testAgentConfig("v3")
	mgr.SetServiceConfig(context.Background(), "app-1", "svc", cfg)

	got, err := mgr.GetServiceConfig(context.Background(), "app-1", "svc")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, len(got.Version) > 0) // auto-generated version
}

func TestGetServiceConfig_NotFound(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	got, err := mgr.GetServiceConfig(context.Background(), "app-1", "nonexistent")
	assert.Error(t, err)
	assert.Nil(t, got)
}

// ── DeleteServiceConfig ────────────────────────────────────────────────

func TestDeleteServiceConfig(t *testing.T) {
	client := newMockNacosClient()
	mgr := newTestOnDemandManager(t, client)

	cfg := testAgentConfig("v1")
	mgr.SetServiceConfig(context.Background(), "app-1", "svc", cfg)

	require.NoError(t, mgr.DeleteServiceConfig(context.Background(), "app-1", "svc"))

	_, ok := client.configs[key("app-1", "svc")]
	assert.False(t, ok)
}

// ── ListServiceConfigs ───────────────────────────────────────────────

func TestListServiceConfigs(t *testing.T) {
	client := newMockNacosClient()
	mgr := newTestOnDemandManager(t, client)

	mgr.SetServiceConfig(context.Background(), "app-1", "svc-a", testAgentConfig("v1"))
	mgr.SetServiceConfig(context.Background(), "app-1", "svc-b", testAgentConfig("v2"))

	services, err := mgr.ListServiceConfigs(context.Background(), "app-1")
	require.NoError(t, err)
	assert.Contains(t, services, "svc-a")
	assert.Contains(t, services, "svc-b")
}

func TestListServiceConfigs_FiltersReservedIDs(t *testing.T) {
	client := newMockNacosClient()
	client.configs[key("app-1", "_default_")] = "{}"
	client.configs[key("app-1", "real-svc")] = `{"version":"v1"}`

	mgr := newTestOnDemandManager(t, client)
	services, err := mgr.ListServiceConfigs(context.Background(), "app-1")
	require.NoError(t, err)
	assert.Contains(t, services, "real-svc")
	assert.NotContains(t, services, "_default_")
	assert.NotContains(t, services, "")
}

// ── IsSystemReservedDataID ───────────────────────────────────────────

func TestIsSystemReservedDataID(t *testing.T) {
	assert.True(t, IsSystemReservedDataID(""))
	assert.True(t, IsSystemReservedDataID("_unused_default_"))
	assert.True(t, IsSystemReservedDataID("_default_"))
	assert.False(t, IsSystemReservedDataID("order-service"))
	assert.False(t, IsSystemReservedDataID("_custom"))
}

// ── Cache cleanup ─────────────────────────────────────────────────────

func TestCleanupExpiredEntries(t *testing.T) {
	client := newMockNacosClient()
	mgr := newTestOnDemandManager(t, client)

	// Cache a config by loading it (but don't register the agent)
	mgr.cacheConfig("app-1", "svc", testAgentConfig("v1"))

	// Verify it's in cache
	_, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	assert.True(t, ok)

	// Wait for cleanup (agent not registered → expires after CacheExpiration)
	time.Sleep(300 * time.Millisecond)

	// Should be cleaned up
	_, ok = mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	assert.False(t, ok)
}

func TestCleanupExpiredEntries_KeepsRegisteredAgents(t *testing.T) {
	// After the refactoring, service-level cache entries are protected by
	// serviceWatchers: if any agent is watching the service, the entry
	// is refreshed and never expires.
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)

	mgr := newTestOnDemandManager(t, client)

	// Register agent (which loads + caches config + adds to serviceWatchers)
	mgr.RegisterAgent(context.Background(), "app-1", "agent-1", "svc")

	// Wait past cache expiration
	time.Sleep(300 * time.Millisecond)

	// Config should still be cached because agent is a registered watcher
	_, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	assert.True(t, ok, "registered agent's config should not be cleaned up")
}

// ── SubscribeAgentConfig ───────────────────────────────────────────────

func TestSubscribeAgentConfig(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())

	called := false
	mgr.SubscribeAgentConfig("app-1", "agent-1", func(event *AgentConfigChangeEvent) {
		called = true
	})

	// Trigger a notification
	mgr.notifyAgentSubscribers("app-1", "agent-1", &AgentConfigChangeEvent{})

	// Give goroutine time to run
	time.Sleep(50 * time.Millisecond)
	assert.True(t, called)
}

func TestUnsubscribeAgentConfig(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())

	callCount := 0
	mgr.SubscribeAgentConfig("app-1", "agent-1", func(event *AgentConfigChangeEvent) {
		callCount++
	})

	mgr.notifyAgentSubscribers("app-1", "agent-1", &AgentConfigChangeEvent{})
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, callCount)

	mgr.UnsubscribeAgentConfig("app-1", "agent-1")

	mgr.notifyAgentSubscribers("app-1", "agent-1", &AgentConfigChangeEvent{})
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, callCount) // not called after unsubscribe
}

// ── GetCacheStats ────────────────────────────────────────────────────

func TestGetCacheStats(t *testing.T) {
	client := newMockNacosClient()
	mgr := newTestOnDemandManager(t, client)

	// Initial stats
	stats := mgr.GetCacheStats()
	assert.Equal(t, int64(0), stats.CacheHits)
	assert.Equal(t, int64(0), stats.CacheMisses)

	// Load config (miss)
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)
	mgr.GetConfigForAgent(context.Background(), "app-1", "a1", "svc")

	// Load again (hit)
	mgr.GetConfigForAgent(context.Background(), "app-1", "a1", "svc")

	stats = mgr.GetCacheStats()
	assert.Equal(t, int64(1), stats.CacheHits)
	assert.Equal(t, int64(1), stats.CacheMisses)
	assert.Equal(t, 1, stats.TotalCachedConfigs)
}

// ── Start idempotent ────────────────────────────────────────────────

func TestStart_Idempotent(t *testing.T) {
	mgr, _ := NewNacosOnDemandConfigManager(zap.NewNop(), DefaultOnDemandConfig(), nil, newMockNacosClient())
	require.NoError(t, mgr.Start(context.Background()))
	require.NoError(t, mgr.Start(context.Background())) // second Start is no-op
	require.NoError(t, mgr.Close())
}

// ── mock helpers (watch state) ─────────────────────────────────────────

// isListening reports whether the mock Nacos client has an active listener
// registered for the given DataID. Used to assert watch setup/cancellation.
func (m *mockNacosClient) isListening(dataID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.listening[dataID]
	return ok
}

// listeningCount returns the number of active listeners.
func (m *mockNacosClient) listeningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.listening)
}

// ── watch-lifecycle test helpers ───────────────────────────────────────

// newTestOnDemandManagerNoLoop is like newTestOnDemandManager but does not
// start the background cleanup loop. Tests that drive cleanupExpiredEntries
// directly use this so they are deterministic and avoid races between the
// test goroutine (which mutates LastAccess) and the loop.
func newTestOnDemandManagerNoLoop(t *testing.T, client *mockNacosClient) *NacosOnDemandConfigManager {
	t.Helper()
	cfg := DefaultOnDemandConfig()
	cfg.CacheExpiration = 100 * time.Millisecond
	cfg.CleanupInterval = 50 * time.Millisecond
	mgr, err := NewNacosOnDemandConfigManager(zaptest.NewLogger(t), cfg, nil, client)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

// ageCacheEntry sets a cache entry's LastAccess to now+offset, simulating an
// entry that has (not) been accessed recently. Lets cleanup tests run
// deterministically without sleeping for CacheExpiration.
func ageCacheEntry(mgr *NacosOnDemandConfigManager, appID, dataID string, offset time.Duration) {
	if e, ok := mgr.configCache.Load(mgr.cacheKey(appID, dataID)); ok {
		entry := e.(*AgentConfigEntry)
		entry.mu.Lock()
		entry.LastAccess = time.Now().Add(offset)
		entry.mu.Unlock()
	}
}

// watcherCount returns the number of agents watching the given service config.
func watcherCount(mgr *NacosOnDemandConfigManager, appID, serviceName string) int {
	w, ok := mgr.serviceWatchers.Load(mgr.cacheKey(appID, serviceName))
	if !ok {
		return 0
	}
	n := 0
	w.(*sync.Map).Range(func(_, _ interface{}) bool { n++; return true })
	return n
}

// agentServiceCount returns the number of services an agent is watching.
func agentServiceCount(mgr *NacosOnDemandConfigManager, appID, agentID string) int {
	s, ok := mgr.agentServices.Load(mgr.cacheKey(appID, agentID))
	if !ok {
		return 0
	}
	n := 0
	s.(*sync.Map).Range(func(_, _ interface{}) bool { n++; return true })
	return n
}

// ── Watch lifecycle (on-demand watch setup/release) ────────────────────

// TestRegisterAgent_SetupWatch verifies that registering an agent for a
// service records it as a watcher and establishes the underlying Nacos watch.
func TestRegisterAgent_SetupWatch(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)

	mgr := newTestOnDemandManager(t, client)
	_, err := mgr.RegisterAgent(context.Background(), "app-1", "agent-1", "svc")
	require.NoError(t, err)

	assert.Equal(t, 1, watcherCount(mgr, "app-1", "svc"), "agent should be tracked as a watcher")
	assert.True(t, client.isListening("svc"), "Nacos watch should be active")

	e, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	require.True(t, ok)
	assert.True(t, e.(*AgentConfigEntry).IsWatching)
}

// TestUnregisterAgent_LastWatcherReleasesWatch verifies that unregistering
// the last watcher of a service cancels the Nacos watch and clears the cache.
func TestUnregisterAgent_LastWatcherReleasesWatch(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)

	mgr := newTestOnDemandManager(t, client)
	_, err := mgr.RegisterAgent(context.Background(), "app-1", "agent-1", "svc")
	require.NoError(t, err)
	require.True(t, client.isListening("svc"))

	require.NoError(t, mgr.UnregisterAgent(context.Background(), "app-1", "agent-1"))

	assert.Equal(t, 0, watcherCount(mgr, "app-1", "svc"))
	assert.False(t, client.isListening("svc"), "watch should be cancelled")
	_, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	assert.False(t, ok, "cache entry should be cleared")
}

// TestUnregisterAgent_NotLastWatcher_KeepsWatch verifies that unregistering
// a non-last agent keeps the watch alive for the remaining watchers.
func TestUnregisterAgent_NotLastWatcher_KeepsWatch(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)

	mgr := newTestOnDemandManager(t, client)
	_, err := mgr.RegisterAgent(context.Background(), "app-1", "a1", "svc")
	require.NoError(t, err)
	_, err = mgr.RegisterAgent(context.Background(), "app-1", "a2", "svc")
	require.NoError(t, err)
	require.Equal(t, 2, watcherCount(mgr, "app-1", "svc"))

	require.NoError(t, mgr.UnregisterAgent(context.Background(), "app-1", "a1"))

	assert.Equal(t, 1, watcherCount(mgr, "app-1", "svc"))
	assert.True(t, client.isListening("svc"), "watch should stay active for a2")
	_, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	assert.True(t, ok)
}

// TestCleanupExpiredEntries_WithWatchers_NotCleaned verifies that a cache
// entry with an active watcher survives cleanup and keeps its watch active.
func TestCleanupExpiredEntries_WithWatchers_NotCleaned(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)

	mgr := newTestOnDemandManagerNoLoop(t, client)
	_, err := mgr.RegisterAgent(context.Background(), "app-1", "agent-1", "svc")
	require.NoError(t, err)

	ageCacheEntry(mgr, "app-1", "svc", -1*time.Hour)
	mgr.cleanupExpiredEntries()

	_, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	assert.True(t, ok, "watched entry must survive cleanup")
	assert.True(t, client.isListening("svc"), "watch must stay active")
	assert.Equal(t, 1, watcherCount(mgr, "app-1", "svc"))
}

// TestCleanupExpiredEntries_NoWatchers_Cleaned verifies that a watching
// cache entry with no remaining watchers is expired and its watch cancelled.
func TestCleanupExpiredEntries_NoWatchers_Cleaned(t *testing.T) {
	client := newMockNacosClient()
	mgr := newTestOnDemandManagerNoLoop(t, client)

	// setupWatch with no registered agent → placeholder entry, IsWatching=true,
	// but no watcher in serviceWatchers.
	mgr.setupWatch("app-1", "svc")
	require.True(t, client.isListening("svc"))

	ageCacheEntry(mgr, "app-1", "svc", -1*time.Hour)
	mgr.cleanupExpiredEntries()

	_, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	assert.False(t, ok, "unwatched entry must be cleaned")
	assert.False(t, client.isListening("svc"), "watch must be cancelled")
}

// TestGetConfigForAgent_ReestablishesWatch verifies that after a watch is
// cleaned up (no watchers, expired), a subsequent GetConfigForAgent
// re-establishes both the cache entry and the Nacos watch.
func TestGetConfigForAgent_ReestablishesWatch(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)

	mgr := newTestOnDemandManagerNoLoop(t, client)

	// First load: cache miss → load from Nacos → setup watch.
	_, err := mgr.GetConfigForAgent(context.Background(), "app-1", "a1", "svc")
	require.NoError(t, err)
	require.True(t, client.isListening("svc"))

	// Simulate watch cleanup (no watcher + aged out).
	ageCacheEntry(mgr, "app-1", "svc", -1*time.Hour)
	mgr.cleanupExpiredEntries()
	require.False(t, client.isListening("svc"), "watch should be cancelled by cleanup")

	// Second load: cache miss again → load → setup watch re-established.
	_, err = mgr.GetConfigForAgent(context.Background(), "app-1", "a1", "svc")
	require.NoError(t, err)
	assert.True(t, client.isListening("svc"), "watch must be re-established")

	e, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	require.True(t, ok)
	assert.True(t, e.(*AgentConfigEntry).IsWatching)
}

// TestRegisterAgent_MultipleAgentsSameService_OneWatch verifies that
// multiple agents registering for the same service share a single Nacos watch.
func TestRegisterAgent_MultipleAgentsSameService_OneWatch(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)

	mgr := newTestOnDemandManager(t, client)
	_, err := mgr.RegisterAgent(context.Background(), "app-1", "a1", "svc")
	require.NoError(t, err)
	_, err = mgr.RegisterAgent(context.Background(), "app-1", "a2", "svc")
	require.NoError(t, err)

	assert.Equal(t, 1, client.listeningCount(), "only one Nacos listener for the service")
	assert.True(t, client.isListening("svc"))
	assert.Equal(t, 2, watcherCount(mgr, "app-1", "svc"))
	assert.Equal(t, 1, agentServiceCount(mgr, "app-1", "a1"))
	assert.Equal(t, 1, agentServiceCount(mgr, "app-1", "a2"))
}

// TestUnregisterAgent_RemovesFromAllServices verifies that unregistering an
// agent removes it from every service it watches, releasing watches for
// services where it was the last watcher.
func TestUnregisterAgent_RemovesFromAllServices(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc-a")] = string(cfgJSON)
	client.configs[key("app-1", "svc-b")] = string(cfgJSON)

	mgr := newTestOnDemandManager(t, client)
	_, err := mgr.RegisterAgent(context.Background(), "app-1", "a1", "svc-a")
	require.NoError(t, err)
	_, err = mgr.RegisterAgent(context.Background(), "app-1", "a1", "svc-b")
	require.NoError(t, err)
	require.Equal(t, 2, agentServiceCount(mgr, "app-1", "a1"))

	require.NoError(t, mgr.UnregisterAgent(context.Background(), "app-1", "a1"))

	// a1 was the only watcher for both → both watches released + caches cleared.
	assert.False(t, client.isListening("svc-a"))
	assert.False(t, client.isListening("svc-b"))
	_, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc-a"))
	assert.False(t, ok)
	_, ok = mgr.configCache.Load(mgr.cacheKey("app-1", "svc-b"))
	assert.False(t, ok)
	assert.Equal(t, 0, agentServiceCount(mgr, "app-1", "a1"))
}

// ── handleConfigChange (Nacos watch callback) ──────────────────────────

// TestHandleConfigChange_Created verifies the "created" path: a config
// arriving for a service with no prior cache entry is stored, and ConfigManager
// subscribers are notified.
func TestHandleConfigChange_Created(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())

	notified := false
	mgr.Subscribe(func(oldCfg, newCfg *otelmodel.AgentConfig) {
		notified = true
		assert.Nil(t, oldCfg)
		require.NotNil(t, newCfg)
		assert.Equal(t, "v1", newCfg.Version)
	})

	mgr.handleConfigChange("app-1", "svc", `{"version":"v1"}`)

	e, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	require.True(t, ok)
	entry := e.(*AgentConfigEntry)
	require.NotNil(t, entry.Config)
	assert.Equal(t, "v1", entry.Config.Version)
	assert.True(t, notified)
}

// TestHandleConfigChange_Updated verifies the "updated" path: both the
// agent-specific subscriber and ConfigManager subscribers fire with old/new.
func TestHandleConfigChange_Updated(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	mgr.cacheConfig("app-1", "svc", testAgentConfig("v1"))

	agentNotified := false
	mgr.SubscribeAgentConfig("app-1", "svc", func(event *AgentConfigChangeEvent) {
		agentNotified = true
		assert.Equal(t, "updated", event.Type)
		require.NotNil(t, event.OldConfig)
		assert.Equal(t, "v1", event.OldConfig.Version)
	})
	globalNotified := false
	mgr.Subscribe(func(oldCfg, newCfg *otelmodel.AgentConfig) {
		globalNotified = true
		require.NotNil(t, oldCfg)
		require.NotNil(t, newCfg)
		assert.Equal(t, "v1", oldCfg.Version)
		assert.Equal(t, "v2", newCfg.Version)
	})

	mgr.handleConfigChange("app-1", "svc", `{"version":"v2"}`)

	e, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	require.True(t, ok)
	entry := e.(*AgentConfigEntry)
	require.NotNil(t, entry.Config)
	assert.Equal(t, "v2", entry.Config.Version)
	assert.True(t, agentNotified)
	assert.True(t, globalNotified)
}

// TestHandleConfigChange_Deleted verifies the "deleted" path (empty data):
// the cached config is nilled but the entry is kept; ConfigManager
// subscribers are NOT notified (newConfig is nil).
func TestHandleConfigChange_Deleted(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	mgr.cacheConfig("app-1", "svc", testAgentConfig("v1"))

	globalNotified := false
	mgr.Subscribe(func(oldCfg, newCfg *otelmodel.AgentConfig) {
		globalNotified = true
	})

	mgr.handleConfigChange("app-1", "svc", "")

	e, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	require.True(t, ok)
	entry := e.(*AgentConfigEntry)
	assert.Nil(t, entry.Config)
	assert.False(t, globalNotified, "ConfigManager subscribers must not fire for deletions")
}

// TestHandleConfigChange_ParseError verifies that unparseable config data is
// dropped silently: the cache is left untouched and no subscriber fires.
func TestHandleConfigChange_ParseError(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())
	mgr.cacheConfig("app-1", "svc", testAgentConfig("v1"))

	globalNotified := false
	mgr.Subscribe(func(oldCfg, newCfg *otelmodel.AgentConfig) {
		globalNotified = true
	})

	mgr.handleConfigChange("app-1", "svc", "{not-json")

	e, ok := mgr.configCache.Load(mgr.cacheKey("app-1", "svc"))
	require.True(t, ok)
	entry := e.(*AgentConfigEntry)
	require.NotNil(t, entry.Config)
	assert.Equal(t, "v1", entry.Config.Version)
	assert.False(t, globalNotified)
}

// ── WatchServiceConfig / UnwatchServiceConfig ──────────────────────────

// TestWatchServiceConfig_and_Unwatch verifies the service-level watch API:
// WatchServiceConfig activates the Nacos watch + registers the callback;
// UnwatchServiceConfig cancels the watch + removes the callback.
func TestWatchServiceConfig_and_Unwatch(t *testing.T) {
	client := newMockNacosClient()
	mgr := newTestOnDemandManager(t, client)

	called := false
	mgr.WatchServiceConfig("app-1", "svc", func(event *AgentConfigChangeEvent) {
		called = true
	})
	assert.True(t, client.isListening("svc"))

	// notifyAgentSubscribers is synchronous → callback fires before return.
	mgr.notifyAgentSubscribers("app-1", "svc", &AgentConfigChangeEvent{})
	assert.True(t, called)

	mgr.UnwatchServiceConfig("app-1", "svc")
	assert.False(t, client.isListening("svc"))

	called = false
	mgr.notifyAgentSubscribers("app-1", "svc", &AgentConfigChangeEvent{})
	assert.False(t, called)
}

// ── GetRegisteredAgents / GetConfig / interface methods ───────────────

func TestGetRegisteredAgents(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)
	client.configs[key("app-2", "svc")] = string(cfgJSON)
	mgr := newTestOnDemandManager(t, client)

	_, err := mgr.RegisterAgent(context.Background(), "app-1", "a1", "svc")
	require.NoError(t, err)
	_, err = mgr.RegisterAgent(context.Background(), "app-1", "a2", "svc")
	require.NoError(t, err)
	_, err = mgr.RegisterAgent(context.Background(), "app-2", "b1", "svc")
	require.NoError(t, err)

	agents := mgr.GetRegisteredAgents()
	assert.ElementsMatch(t, []string{"a1", "a2"}, agents["app-1"])
	assert.ElementsMatch(t, []string{"b1"}, agents["app-2"])
}

// TestGetConfig_ReturnsCachedConfig covers the ConfigManager.GetConfig
// fallback: returns the first cached config, or an error if none.
func TestGetConfig_ReturnsCachedConfig(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())

	_, err := mgr.GetConfig(context.Background())
	assert.Error(t, err)

	mgr.cacheConfig("app-1", "svc", testAgentConfig("v1"))
	cfg, err := mgr.GetConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "v1", cfg.Version)
}

// TestConfigManagerInterface_Watch_Subscribe_UpdateConfig covers the
// ConfigManager interface adapters Watch/Subscribe/UpdateConfig.
func TestConfigManagerInterface_Watch_Subscribe_UpdateConfig(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)
	mgr := newTestOnDemandManager(t, client)

	require.NoError(t, mgr.Watch(context.Background(), func(o, n *otelmodel.AgentConfig) {}))
	mgr.Subscribe(func(o, n *otelmodel.AgentConfig) {})

	// No registered agents → "no appID available for update".
	err := mgr.UpdateConfig(context.Background(), testAgentConfig("v1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no appID available")

	_, err = mgr.RegisterAgent(context.Background(), "app-1", "a1", "svc")
	require.NoError(t, err)
	err = mgr.UpdateConfig(context.Background(), testAgentConfig("v1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer supported")
}

// TestDeprecatedMethods_ReturnErrors covers the deprecated instance/default
// config methods, which now always fail or return nil.
func TestDeprecatedMethods_ReturnErrors(t *testing.T) {
	mgr := newTestOnDemandManager(t, newMockNacosClient())

	assert.Error(t, mgr.SetDefaultConfig(context.Background(), "app-1", testAgentConfig("v1")))

	cfg, err := mgr.GetDefaultConfig(context.Background(), "app-1")
	assert.NoError(t, err)
	assert.Nil(t, cfg)

	assert.Error(t, mgr.SetConfigForAgent(context.Background(), "app-1", "a1", testAgentConfig("v1")))
	assert.Error(t, mgr.DeleteConfigForAgent(context.Background(), "app-1", "a1"))
}

// TestNotifyAllAgentsForAppID verifies that a config-change event is fanned
// out to every registered agent under an appID, each with its own AgentID.
func TestNotifyAllAgentsForAppID(t *testing.T) {
	client := newMockNacosClient()
	cfgJSON, _ := json.Marshal(testAgentConfig("v1"))
	client.configs[key("app-1", "svc")] = string(cfgJSON)
	mgr := newTestOnDemandManager(t, client)

	_, err := mgr.RegisterAgent(context.Background(), "app-1", "a1", "svc")
	require.NoError(t, err)
	_, err = mgr.RegisterAgent(context.Background(), "app-1", "a2", "svc")
	require.NoError(t, err)

	var mu sync.Mutex
	got := map[string]string{}
	mgr.SubscribeAgentConfig("app-1", "a1", func(e *AgentConfigChangeEvent) {
		mu.Lock()
		got["a1"] = e.AgentID
		mu.Unlock()
	})
	mgr.SubscribeAgentConfig("app-1", "a2", func(e *AgentConfigChangeEvent) {
		mu.Lock()
		got["a2"] = e.AgentID
		mu.Unlock()
	})

	mgr.notifyAllAgentsForAppID("app-1", &AgentConfigChangeEvent{Type: "updated"})

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "a1", got["a1"])
	assert.Equal(t, "a2", got["a2"])
}
