// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

type authzControlPlane struct {
	*stubControlPlane
	tm     *stubTokenManager
	agents map[string]*agentregistry.AgentInfo
}

func (c *authzControlPlane) GetTokenManager() appmanager.TokenManager {
	return c.tm
}

func (c *authzControlPlane) GetAgent(_ context.Context, agentID string) (*agentregistry.AgentInfo, error) {
	agent, ok := c.agents[agentID]
	if !ok {
		return nil, errors.New("agent not found")
	}
	return agent, nil
}

func authzTestWrapper() (*mcpServerWrapper, *authzControlPlane) {
	cp := &authzControlPlane{
		stubControlPlane: &stubControlPlane{},
		tm: &stubTokenManager{apps: map[string]*appmanager.AppInfo{
			"app-a": {ID: "app-a", TenantID: "tn-a"},
			"app-b": {ID: "app-b", TenantID: "tn-b"},
		}},
		agents: map[string]*agentregistry.AgentInfo{
			"ag-a": {AgentID: "ag-a", AppID: "app-a", ServiceName: "svc-a"},
			"ag-b": {AgentID: "ag-b", AppID: "app-b", ServiceName: "svc-b"},
		},
	}
	w := newInstrumentationTestWrapper(readWriteConfig(), newFakeInstrumentationManager(), cp)
	w.sessionManager = NewSessionManager(zap.NewNop(), SessionManagerConfig{})
	return w, cp
}

func TestAuthorizeToolCallAgentScope(t *testing.T) {
	w, _ := authzTestWrapper()
	tenantCtx := tenantScopedContext("tn-a")

	require.NoError(t, w.authorizeToolCall(tenantCtx, instrumentationCall(map[string]any{"agent_id": "ag-a"})))

	err := w.authorizeToolCall(tenantCtx, instrumentationCall(map[string]any{"agent_id": "ag-b"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "跨租户")

	require.NoError(t, w.authorizeToolCall(
		globalMCPContext(tenantauth.KeyTypeSuper),
		instrumentationCall(map[string]any{"agent_id": "ag-b"})))
	require.NoError(t, w.authorizeToolCall(
		globalMCPContext(tenantauth.KeyTypeOperator),
		instrumentationCall(map[string]any{"agent_id": "ag-b"})))

	require.Error(t, w.authorizeToolCall(
		context.Background(), instrumentationCall(map[string]any{"agent_id": "ag-a"})),
		"missing identity must fail closed")
	require.Error(t, w.authorizeToolCall(
		tenantCtx, instrumentationCall(map[string]any{"agent_id": "unknown"})))

	// Tools without an agent/session argument are delegated to their own
	// rule-level authorization or result filtering.
	require.NoError(t, w.authorizeToolCall(tenantCtx, instrumentationCall(map[string]any{})))
}

func TestAuthorizeToolCallSessionScope(t *testing.T) {
	w, _ := authzTestWrapper()
	tenantCtx := tenantScopedContext("tn-a")

	otherSession, err := w.sessionManager.CreateSession(&CreateSessionRequest{AgentID: "ag-b"})
	require.NoError(t, err)
	err = w.authorizeToolCall(tenantCtx, instrumentationCall(map[string]any{
		"session_id": otherSession.CollectorSessionID,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "跨租户")

	ownSession, err := w.sessionManager.CreateSession(&CreateSessionRequest{AgentID: "ag-a"})
	require.NoError(t, err)
	require.NoError(t, w.authorizeToolCall(tenantCtx, instrumentationCall(map[string]any{
		"session_id": ownSession.CollectorSessionID,
	})))

	require.Error(t, w.authorizeToolCall(tenantCtx, instrumentationCall(map[string]any{
		"session_id": "missing-session",
	})))
}

func namedToolCall(name string, args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}}
}

func tenantContextWithScopes(scopes []string) context.Context {
	return tenantauth.WithIdentity(context.Background(), tenantauth.Identity{
		KeyType:  tenantauth.KeyTypeTenant,
		TenantID: "tn-a",
		KeyID:    "key-t",
		Scopes:   scopes,
	})
}

func TestInstrumentationWriteScope(t *testing.T) {
	w, _ := authzTestWrapper()

	err := w.authorizeToolCall(tenantScopedContext("tn-a"),
		namedToolCall("instrument_rule_create", map[string]any{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), instrumentationWriteScope)

	require.NoError(t, w.authorizeToolCall(
		tenantContextWithScopes([]string{instrumentationWriteScope}),
		namedToolCall("instrument_rule_create", map[string]any{})))

	for _, tool := range []string{"instrument_rule_list", "instrument_rule_get", "list_agents"} {
		require.NoError(t, w.authorizeToolCall(tenantScopedContext("tn-a"), namedToolCall(tool, map[string]any{})),
			"read tools must not require the write scope: %s", tool)
	}

	require.NoError(t, w.authorizeToolCall(
		globalMCPContext(tenantauth.KeyTypeSuper),
		namedToolCall("instrument_rule_delete", map[string]any{})),
		"global keys keep full privileges")
	require.NoError(t, w.authorizeToolCall(
		globalMCPContext(tenantauth.KeyTypeOperator),
		namedToolCall("instrument_rule_pause", map[string]any{})))
}

func TestFilterAgentsByTenant(t *testing.T) {
	w, cp := authzTestWrapper()
	agents := []*agentregistry.AgentInfo{
		cp.agents["ag-a"],
		cp.agents["ag-b"],
		{AgentID: "ag-c", AppID: "app-a"},
	}

	filtered, err := w.filterAgentsByTenant(tenantScopedContext("tn-a"), agents)
	require.NoError(t, err)
	assert.Len(t, filtered, 2)

	all, err := w.filterAgentsByTenant(globalMCPContext(tenantauth.KeyTypeSuper), agents)
	require.NoError(t, err)
	assert.Len(t, all, 3)
}

func TestListAgentsFiltersByTenant(t *testing.T) {
	w, cp := authzTestWrapper()
	cp.online = []*agentregistry.AgentInfo{cp.agents["ag-a"], cp.agents["ag-b"]}

	result, err := w.handleListAgents(tenantScopedContext("tn-a"), instrumentationCall(map[string]any{}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := instrumentationText(t, result)
	assert.Contains(t, text, "ag-a")
	assert.NotContains(t, text, "ag-b")
}
