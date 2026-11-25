// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

// stubControlPlane satisfies ControlPlane through the embedded interface and
// implements only the method the instrumentation policy exercises.
type stubControlPlane struct {
	controlplaneext.ControlPlane
	online []*agentregistry.AgentInfo
}

func (s *stubControlPlane) GetOnlineAgents(context.Context) ([]*agentregistry.AgentInfo, error) {
	return s.online, nil
}

type stubAgentRegistry struct {
	agentregistry.AgentRegistry
	instances []*agentregistry.AgentInfo
}

func (s *stubAgentRegistry) GetInstancesByService(
	context.Context, string, string,
) ([]*agentregistry.AgentInfo, error) {
	return s.instances, nil
}

type controlPlaneWithRegistry struct {
	*stubControlPlane
	instances []*agentregistry.AgentInfo
}

func (c *controlPlaneWithRegistry) GetAgentRegistry() agentregistry.AgentRegistry {
	return &stubAgentRegistry{instances: c.instances}
}

type fakeInstrumentationManager struct {
	instrumentationmanager.InstrumentationManager
	rules     map[string]*instrumentationmanager.Rule
	createReq *instrumentationmanager.CreateRuleRequest
	actions   []string
	refreshed bool
}

func newFakeInstrumentationManager() *fakeInstrumentationManager {
	return &fakeInstrumentationManager{rules: make(map[string]*instrumentationmanager.Rule)}
}

func (f *fakeInstrumentationManager) ListRules(
	context.Context, instrumentationmanager.ListRulesQuery,
) ([]*instrumentationmanager.Rule, error) {
	rules := make([]*instrumentationmanager.Rule, 0, len(f.rules))
	for _, rule := range f.rules {
		rules = append(rules, rule)
	}
	return rules, nil
}

func (f *fakeInstrumentationManager) GetRule(_ context.Context, ruleID string) (*instrumentationmanager.Rule, error) {
	rule, ok := f.rules[ruleID]
	if !ok {
		return nil, fmt.Errorf("rule %s not found", ruleID)
	}
	return rule, nil
}

func (f *fakeInstrumentationManager) ListTargetStatuses(
	context.Context, string,
) ([]*instrumentationmanager.RuleTargetStatus, error) {
	return nil, nil
}

func (f *fakeInstrumentationManager) GetRuleRuntimeSnapshot(
	_ context.Context, ruleID string,
) (*instrumentationmanager.RuleRuntimeSnapshot, error) {
	return &instrumentationmanager.RuleRuntimeSnapshot{RuleID: ruleID}, nil
}

func (f *fakeInstrumentationManager) RefreshRuleRuntimeSnapshot(
	_ context.Context, ruleID string,
) (*instrumentationmanager.RuleRuntimeSnapshot, error) {
	f.refreshed = true
	return &instrumentationmanager.RuleRuntimeSnapshot{RuleID: ruleID}, nil
}

func (f *fakeInstrumentationManager) CreateRule(
	_ context.Context, req *instrumentationmanager.CreateRuleRequest,
) (*instrumentationmanager.Rule, error) {
	f.createReq = req
	rule := &instrumentationmanager.Rule{
		ID:          "rule-1",
		AppID:       req.AppID,
		ServiceName: req.ServiceName,
		ScopeType:   req.ScopeType,
		ClassName:   req.ClassName,
		CreatedBy:   req.CreatedBy,
	}
	f.rules[rule.ID] = rule
	return rule, nil
}

func (f *fakeInstrumentationManager) applyAction(action string) instrumentRuleAction {
	return func(_ context.Context, ruleID string) (*instrumentationmanager.Rule, error) {
		f.actions = append(f.actions, action)
		return f.GetRule(context.Background(), ruleID)
	}
}

func newInstrumentationTestWrapper(
	cfg InstrumentationConfig,
	mgr instrumentationmanager.InstrumentationManager,
	cp controlplaneext.ControlPlane,
) *mcpServerWrapper {
	base := createDefaultConfig()
	base.Auth.APIKeys = []string{"test-key"}
	base.Instrumentation = cfg
	ext := &Extension{
		config:       base,
		logger:       zap.NewNop(),
		controlPlane: cp,
		instrMgr:     mgr,
	}
	return &mcpServerWrapper{
		ext:         ext,
		mcpServer:   server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(true)),
		logger:      zap.NewNop(),
		instrPolicy: newInstrumentationPolicy(cfg),
	}
}

func instrumentationCall(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

func instrumentationText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, result)
	require.NotEmpty(t, result.Content)
	content, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok)
	return content.Text
}

// instrumentedContext uses a global (static super) identity so the existing
// handler tests do not depend on app→tenant resolution. Tenant-scoped cases
// are covered in instrumentation_authz_test.go.
func instrumentedContext() context.Context {
	return tenantauth.WithIdentity(context.Background(), tenantauth.Identity{
		KeyType: tenantauth.KeyTypeSuper,
		KeyID:   "abcdef12",
	})
}

func TestRegisterInstrumentationTools(t *testing.T) {
	readOnly := testInstrumentationConfig()
	readWrite := readOnly
	readWrite.AllowWrite = true
	disabled := createDefaultConfig().Instrumentation

	tests := []struct {
		name string
		cfg  InstrumentationConfig
		want int
	}{
		{name: "disabled registers nothing", cfg: disabled, want: 0},
		{name: "read only registers 3 tools", cfg: readOnly, want: 3},
		{name: "write enabled registers 7 tools", cfg: readWrite, want: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newInstrumentationTestWrapper(tt.cfg, newFakeInstrumentationManager(), &stubControlPlane{})
			if tt.cfg.Enabled {
				w.registerInstrumentationTools()
			}
			assert.Len(t, w.mcpServer.ListTools(), tt.want)
		})
	}
}

func TestHandleInstrumentRuleCreate(t *testing.T) {
	validArgs := func() map[string]any {
		return map[string]any{
			"app_id":       "app-a",
			"service_name": "svc-a",
			"class_name":   "com.example.OrderService",
			"method_name":  "createOrder",
		}
	}

	t.Run("creates service scoped rule with caller identity", func(t *testing.T) {
		mgr := newFakeInstrumentationManager()
		cp := &controlPlaneWithRegistry{
			stubControlPlane: &stubControlPlane{},
			instances: []*agentregistry.AgentInfo{
				{AgentID: "a1"}, {AgentID: "a2"},
			},
		}
		w := newInstrumentationTestWrapper(readWriteConfig(), mgr, cp)

		result, err := w.handleInstrumentRuleCreate(instrumentedContext(), instrumentationCall(validArgs()))
		require.NoError(t, err)
		require.False(t, result.IsError)
		require.NotNil(t, mgr.createReq)
		assert.Equal(t, instrumentationmanager.ScopeTypeService, mgr.createReq.ScopeType)
		assert.Empty(t, mgr.createReq.TargetAgentIDs)
		assert.Equal(t, instrumentationmanager.InstrumentTypeTrace, mgr.createReq.InstrumentType)
		assert.Equal(t, "mcp:super:abcdef12", mgr.createReq.CreatedBy)
		assert.Contains(t, instrumentationText(t, result), "rule-1")
	})

	t.Run("rejects too many instances", func(t *testing.T) {
		mgr := newFakeInstrumentationManager()
		instances := make([]*agentregistry.AgentInfo, 101)
		for i := range instances {
			instances[i] = &agentregistry.AgentInfo{AgentID: fmt.Sprintf("a%d", i)}
		}
		cp := &controlPlaneWithRegistry{
			stubControlPlane: &stubControlPlane{},
			instances:        instances,
		}
		w := newInstrumentationTestWrapper(readWriteConfig(), mgr, cp)

		result, err := w.handleInstrumentRuleCreate(instrumentedContext(), instrumentationCall(validArgs()))
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Contains(t, instrumentationText(t, result), "超过上限")
		assert.Nil(t, mgr.createReq)
	})

	t.Run("policy rejections", func(t *testing.T) {
		tests := []struct {
			name    string
			mutate  func(map[string]any)
			wantErr string
		}{
			{
				name:    "missing service scope",
				mutate:  func(a map[string]any) { delete(a, "app_id") },
				wantErr: "app_id 与 service_name",
			},
			{
				name:    "jdk class",
				mutate:  func(a map[string]any) { a["class_name"] = "java.lang.String" },
				wantErr: "禁用前缀",
			},
			{
				name:    "force not allowed",
				mutate:  func(a map[string]any) { a["force"] = true },
				wantErr: "force 参数未启用",
			},
			{
				name:    "capture not allowed",
				mutate:  func(a map[string]any) { a["capture_args"] = true },
				wantErr: "采集未启用",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				mgr := newFakeInstrumentationManager()
				w := newInstrumentationTestWrapper(readWriteConfig(), mgr, &stubControlPlane{})
				args := validArgs()
				tt.mutate(args)

				result, err := w.handleInstrumentRuleCreate(instrumentedContext(), instrumentationCall(args))
				require.NoError(t, err)
				assert.True(t, result.IsError)
				assert.Contains(t, instrumentationText(t, result), tt.wantErr)
				assert.Nil(t, mgr.createReq)
			})
		}
	})
}

func TestHandleInstrumentRuleActionOwnership(t *testing.T) {
	mgr := newFakeInstrumentationManager()
	w := newInstrumentationTestWrapper(readWriteConfig(), mgr, &stubControlPlane{})

	mgr.rules["rule-1"] = &instrumentationmanager.Rule{ID: "rule-1", CreatedBy: "human"}
	args := map[string]any{"rule_id": "rule-1"}
	result, err := w.handleInstrumentRuleAction(
		instrumentedContext(), instrumentationCall(args), "instrument_rule_pause", mgr.applyAction("pause"))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, instrumentationText(t, result), "并非由 MCP 创建")
	assert.Empty(t, mgr.actions)

	mgr.rules["rule-1"] = &instrumentationmanager.Rule{ID: "rule-1", CreatedBy: "mcp:key-abcdef12"}
	result, err = w.handleInstrumentRuleAction(
		instrumentedContext(), instrumentationCall(args), "instrument_rule_pause", mgr.applyAction("pause"))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Equal(t, []string{"pause"}, mgr.actions)
}

func TestHandleInstrumentationReadTools(t *testing.T) {
	mgr := newFakeInstrumentationManager()
	mgr.rules["rule-1"] = &instrumentationmanager.Rule{ID: "rule-1", CreatedBy: "mcp:key-abcdef12"}
	w := newInstrumentationTestWrapper(readWriteConfig(), mgr, &stubControlPlane{})
	ctx := instrumentedContext()

	t.Run("list", func(t *testing.T) {
		result, err := w.handleInstrumentRuleList(ctx, instrumentationCall(map[string]any{}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		assert.Contains(t, instrumentationText(t, result), "rule-1")
	})

	t.Run("get", func(t *testing.T) {
		result, err := w.handleInstrumentRuleGet(ctx, instrumentationCall(map[string]any{"rule_id": "rule-1"}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		assert.Contains(t, instrumentationText(t, result), "rule-1")
	})

	t.Run("snapshot without refresh", func(t *testing.T) {
		result, err := w.handleInstrumentRuleSnapshot(ctx, instrumentationCall(map[string]any{"rule_id": "rule-1"}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		assert.False(t, mgr.refreshed)
	})

	t.Run("snapshot with refresh", func(t *testing.T) {
		args := map[string]any{"rule_id": "rule-1", "refresh": true}
		result, err := w.handleInstrumentRuleSnapshot(ctx, instrumentationCall(args))
		require.NoError(t, err)
		require.False(t, result.IsError)
		assert.True(t, mgr.refreshed)
	})
}

func TestInstrumentationToolsUnavailable(t *testing.T) {
	w := newInstrumentationTestWrapper(readWriteConfig(), nil, &stubControlPlane{})
	result, err := w.handleInstrumentRuleList(instrumentedContext(), instrumentationCall(map[string]any{}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, instrumentationText(t, result), "未启用")
}

func readWriteConfig() InstrumentationConfig {
	cfg := testInstrumentationConfig()
	cfg.AllowWrite = true
	return cfg
}

func TestInstrumentationContextInjectsActor(t *testing.T) {
	w := newInstrumentationTestWrapper(readWriteConfig(), newFakeInstrumentationManager(), &stubControlPlane{})
	identity := tenantauth.Identity{
		KeyType:  tenantauth.KeyTypeTenant,
		TenantID: "tenant-a",
		KeyID:    "abcdef12",
	}
	ctx, cancel := w.instrumentationContext(tenantauth.WithIdentity(context.Background(), identity))
	defer cancel()

	actor, ok := instrumentationmanager.ActorFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "mcp:tenant:tenant-a:abcdef12", actor.ID)
	assert.Equal(t, instrumentationmanager.ActorTypeMCP, actor.Type)
	assert.Equal(t, "tenant-a", actor.TenantID)

	_, deadlineSet := ctx.Deadline()
	assert.True(t, deadlineSet)
}
