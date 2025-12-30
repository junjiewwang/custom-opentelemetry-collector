// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package configmanager

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// mockNacosConfigClient is a mock implementation of config_client.IConfigClient.
type mockNacosConfigClient struct {
	mu       sync.RWMutex
	configs  map[string]string // key: "group:dataId"
	watchers map[string]func(namespace, group, dataId, data string)
}

func newMockNacosConfigClient() *mockNacosConfigClient {
	return &mockNacosConfigClient{
		configs:  make(map[string]string),
		watchers: make(map[string]func(namespace, group, dataId, data string)),
	}
}

func (m *mockNacosConfigClient) key(group, dataId string) string {
	return group + ":" + dataId
}

func (m *mockNacosConfigClient) GetConfig(param vo.ConfigParam) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.configs[m.key(param.Group, param.DataId)], nil
}

func (m *mockNacosConfigClient) PublishConfig(param vo.ConfigParam) (bool, error) {
	m.mu.Lock()
	key := m.key(param.Group, param.DataId)
	oldContent := m.configs[key]
	m.configs[key] = param.Content
	watcher := m.watchers[key]
	m.mu.Unlock()

	// Notify watcher if exists and content changed
	if watcher != nil && oldContent != param.Content {
		go watcher("", param.Group, param.DataId, param.Content)
	}

	return true, nil
}

func (m *mockNacosConfigClient) DeleteConfig(param vo.ConfigParam) (bool, error) {
	m.mu.Lock()
	key := m.key(param.Group, param.DataId)
	delete(m.configs, key)
	watcher := m.watchers[key]
	m.mu.Unlock()

	if watcher != nil {
		go watcher("", param.Group, param.DataId, "")
	}

	return true, nil
}

func (m *mockNacosConfigClient) ListenConfig(param vo.ConfigParam) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.watchers[m.key(param.Group, param.DataId)] = param.OnChange
	return nil
}

func (m *mockNacosConfigClient) CancelListenConfig(param vo.ConfigParam) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.watchers, m.key(param.Group, param.DataId))
	return nil
}

func (m *mockNacosConfigClient) SearchConfig(param vo.SearchConfigParam) (*model.ConfigPage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	items := make([]model.ConfigItem, 0)
	for k := range m.configs {
		group, dataId := parseCacheKey(k)
		if param.Group == "" || param.Group == group {
			items = append(items, model.ConfigItem{
				Group:  group,
				DataId: dataId,
			})
		}
	}

	return &model.ConfigPage{
		PageItems: items,
	}, nil
}

// SetConfig sets a config in the mock client.
func (m *mockNacosConfigClient) SetConfig(group, dataId string, config *controlplanev1.AgentConfig) {
	data, _ := json.Marshal(config)
	m.mu.Lock()
	m.configs[m.key(group, dataId)] = string(data)
	m.mu.Unlock()
}

// CloseClient implements config_client.IConfigClient.
func (m *mockNacosConfigClient) CloseClient() {
	// No-op for mock
}

func TestMultiAgentNacosConfigManager_GetConfigForAgent(t *testing.T) {
	logger := zaptest.NewLogger(t)
	client := newMockNacosConfigClient()

	// Setup test configs
	config1 := &controlplanev1.AgentConfig{
		ConfigVersion: "v1.0",
		Sampler: &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerTypeTraceIDRatio,
			Ratio: 0.5,
		},
	}
	config2 := &controlplanev1.AgentConfig{
		ConfigVersion: "v2.0",
		Sampler: &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerTypeTraceIDRatio,
			Ratio: 0.8,
		},
	}
	defaultConfig := &controlplanev1.AgentConfig{
		ConfigVersion: "default",
		Sampler: &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerTypeTraceIDRatio,
			Ratio: 0.1,
		},
	}

	client.SetConfig("TOKEN_A", "agent-001", config1)
	client.SetConfig("TOKEN_A", "agent-002", config2)
	client.SetConfig("TOKEN_A", DefaultConfigDataId, defaultConfig)

	mgr, err := NewMultiAgentNacosConfigManager(logger, MultiAgentConfig{
		Groups:       []string{"TOKEN_A"},
		ScanInterval: time.Hour, // Disable periodic scan for test
		LoadTimeout:  5 * time.Second,
		MaxRetries:   1,
		EnableWatch:  false,
	}, client)
	require.NoError(t, err)

	ctx := context.Background()
	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Wait for initial scan
	time.Sleep(100 * time.Millisecond)

	// Test exact match
	cfg, err := mgr.GetConfigForAgent(ctx, "TOKEN_A", "agent-001")
	require.NoError(t, err)
	assert.Equal(t, "v1.0", cfg.ConfigVersion)
	assert.Equal(t, 0.5, cfg.Sampler.Ratio)

	cfg, err = mgr.GetConfigForAgent(ctx, "TOKEN_A", "agent-002")
	require.NoError(t, err)
	assert.Equal(t, "v2.0", cfg.ConfigVersion)

	// Test fallback to default
	cfg, err = mgr.GetConfigForAgent(ctx, "TOKEN_A", "agent-unknown")
	require.NoError(t, err)
	assert.Equal(t, "default", cfg.ConfigVersion)
}

func TestMultiAgentNacosConfigManager_UpdateConfigForAgent(t *testing.T) {
	logger := zaptest.NewLogger(t)
	client := newMockNacosConfigClient()

	mgr, err := NewMultiAgentNacosConfigManager(logger, MultiAgentConfig{
		Groups:       []string{"TOKEN_A"},
		ScanInterval: time.Hour,
		LoadTimeout:  5 * time.Second,
		MaxRetries:   1,
		EnableWatch:  false,
	}, client)
	require.NoError(t, err)

	ctx := context.Background()
	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Update config
	newConfig := &controlplanev1.AgentConfig{
		ConfigVersion: "v3.0",
		Sampler: &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerTypeTraceIDRatio,
			Ratio: 0.9,
		},
	}

	err = mgr.UpdateConfigForAgent(ctx, "TOKEN_A", "agent-003", newConfig)
	require.NoError(t, err)

	// Verify config was stored
	cfg, err := mgr.GetConfigForAgent(ctx, "TOKEN_A", "agent-003")
	require.NoError(t, err)
	assert.Equal(t, "v3.0", cfg.ConfigVersion)
}

func TestMultiAgentNacosConfigManager_ValidateToken(t *testing.T) {
	logger := zaptest.NewLogger(t)
	client := newMockNacosConfigClient()

	// Setup a config for TOKEN_A
	client.SetConfig("TOKEN_A", "agent-001", &controlplanev1.AgentConfig{
		ConfigVersion: "v1.0",
	})

	mgr, err := NewMultiAgentNacosConfigManager(logger, MultiAgentConfig{
		Groups:       []string{"TOKEN_A", "TOKEN_B"},
		ScanInterval: time.Hour,
		LoadTimeout:  5 * time.Second,
		MaxRetries:   1,
		EnableWatch:  false,
	}, client)
	require.NoError(t, err)

	ctx := context.Background()
	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Wait for initial scan
	time.Sleep(100 * time.Millisecond)

	// Configured token should be valid
	valid, err := mgr.ValidateToken(ctx, "TOKEN_A")
	require.NoError(t, err)
	assert.True(t, valid)

	valid, err = mgr.ValidateToken(ctx, "TOKEN_B")
	require.NoError(t, err)
	assert.True(t, valid) // In configured groups

	// Unknown token
	valid, err = mgr.ValidateToken(ctx, "TOKEN_UNKNOWN")
	require.NoError(t, err)
	assert.False(t, valid)
}

func TestMultiAgentNacosConfigManager_GetAgentsByToken(t *testing.T) {
	logger := zaptest.NewLogger(t)
	client := newMockNacosConfigClient()

	// Setup configs
	client.SetConfig("TOKEN_A", "agent-001", &controlplanev1.AgentConfig{ConfigVersion: "v1"})
	client.SetConfig("TOKEN_A", "agent-002", &controlplanev1.AgentConfig{ConfigVersion: "v1"})
	client.SetConfig("TOKEN_B", "agent-003", &controlplanev1.AgentConfig{ConfigVersion: "v1"})

	mgr, err := NewMultiAgentNacosConfigManager(logger, MultiAgentConfig{
		Groups:       []string{"TOKEN_A", "TOKEN_B"},
		ScanInterval: time.Hour,
		LoadTimeout:  5 * time.Second,
		MaxRetries:   1,
		EnableWatch:  false,
	}, client)
	require.NoError(t, err)

	ctx := context.Background()
	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Wait for initial scan
	time.Sleep(100 * time.Millisecond)

	agents, err := mgr.GetAgentsByToken(ctx, "TOKEN_A")
	require.NoError(t, err)
	assert.Len(t, agents, 2)
	assert.Contains(t, agents, "agent-001")
	assert.Contains(t, agents, "agent-002")

	agents, err = mgr.GetAgentsByToken(ctx, "TOKEN_B")
	require.NoError(t, err)
	assert.Len(t, agents, 1)
	assert.Contains(t, agents, "agent-003")
}

func TestMultiAgentNacosConfigManager_ConfigChangeCallback(t *testing.T) {
	logger := zaptest.NewLogger(t)
	client := newMockNacosConfigClient()

	mgr, err := NewMultiAgentNacosConfigManager(logger, MultiAgentConfig{
		Groups:       []string{"TOKEN_A"},
		ScanInterval: time.Hour,
		LoadTimeout:  5 * time.Second,
		MaxRetries:   1,
		EnableWatch:  true,
	}, client)
	require.NoError(t, err)

	ctx := context.Background()
	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Subscribe to changes before any updates
	var receivedEvents []*ConfigChangeEvent
	var eventMu sync.Mutex
	mgr.SubscribeMultiAgent(func(event *ConfigChangeEvent) {
		eventMu.Lock()
		receivedEvents = append(receivedEvents, event)
		eventMu.Unlock()
	})

	// First, set a config in the mock client directly
	config1 := &controlplanev1.AgentConfig{
		ConfigVersion: "v1.0",
		Sampler: &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerTypeTraceIDRatio,
			Ratio: 0.5,
		},
	}
	client.SetConfig("TOKEN_A", "agent-001", config1)

	// Trigger scan to load the config (this should trigger "created" event)
	err = mgr.ScanConfigs(ctx)
	require.NoError(t, err)

	// Wait for event
	time.Sleep(100 * time.Millisecond)

	// Check event was received
	eventMu.Lock()
	assert.NotEmpty(t, receivedEvents, "Expected at least one event")
	if len(receivedEvents) > 0 {
		lastEvent := receivedEvents[len(receivedEvents)-1]
		assert.Equal(t, "TOKEN_A", lastEvent.Group)
		assert.Equal(t, "agent-001", lastEvent.DataId)
		assert.Equal(t, "created", lastEvent.Type)
	}
	eventMu.Unlock()
}

func TestMultiAgentNacosConfigManager_CacheStats(t *testing.T) {
	logger := zaptest.NewLogger(t)
	client := newMockNacosConfigClient()

	// Setup configs
	client.SetConfig("TOKEN_A", "agent-001", &controlplanev1.AgentConfig{ConfigVersion: "v1"})
	client.SetConfig("TOKEN_A", "agent-002", &controlplanev1.AgentConfig{ConfigVersion: "v1"})

	mgr, err := NewMultiAgentNacosConfigManager(logger, MultiAgentConfig{
		Groups:       []string{"TOKEN_A"},
		ScanInterval: time.Hour,
		LoadTimeout:  5 * time.Second,
		MaxRetries:   1,
		EnableWatch:  false,
	}, client)
	require.NoError(t, err)

	ctx := context.Background()
	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Wait for initial scan
	time.Sleep(100 * time.Millisecond)

	stats := mgr.GetCacheStats()
	assert.Equal(t, 2, stats.TotalEntries)
	assert.Equal(t, 1, stats.TotalTokens)
	assert.Equal(t, 2, stats.TotalAgents)
	assert.Equal(t, 0, stats.FailedEntries)
}

func TestCacheKey(t *testing.T) {
	tests := []struct {
		group  string
		dataId string
		want   string
	}{
		{"TOKEN_A", "agent-001", "TOKEN_A:agent-001"},
		{"GROUP", "config", "GROUP:config"},
		{"", "data", ":data"},
	}

	for _, tt := range tests {
		got := cacheKey(tt.group, tt.dataId)
		assert.Equal(t, tt.want, got)
	}
}

func TestParseCacheKey(t *testing.T) {
	tests := []struct {
		key       string
		wantGroup string
		wantData  string
	}{
		{"TOKEN_A:agent-001", "TOKEN_A", "agent-001"},
		{"GROUP:config", "GROUP", "config"},
		{":data", "", "data"},
		{"nocolon", "", "nocolon"},
	}

	for _, tt := range tests {
		group, dataId := parseCacheKey(tt.key)
		assert.Equal(t, tt.wantGroup, group)
		assert.Equal(t, tt.wantData, dataId)
	}
}

func TestDefaultMultiAgentConfig(t *testing.T) {
	cfg := DefaultMultiAgentConfig()
	assert.Equal(t, DefaultScanInterval, cfg.ScanInterval)
	assert.Equal(t, DefaultLoadTimeout, cfg.LoadTimeout)
	assert.Equal(t, DefaultMaxRetries, cfg.MaxRetries)
	assert.Equal(t, DefaultRetryInterval, cfg.RetryInterval)
	assert.True(t, cfg.EnableWatch)
}
