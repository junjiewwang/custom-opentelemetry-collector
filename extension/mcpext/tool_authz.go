// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

// instrumentationWriteScope is the operator-granted permission required for
// tenant (tk_) keys to use dynamic instrumentation write tools. Global keys
// (static super / ok_) are unrestricted.
const instrumentationWriteScope = "instrumentation:write"

var instrumentationWriteTools = map[string]struct{}{
	"instrument_rule_create": {},
	"instrument_rule_pause":  {},
	"instrument_rule_resume": {},
	"instrument_rule_delete": {},
}

func requiresInstrumentationWriteScope(toolName string) bool {
	_, ok := instrumentationWriteTools[toolName]
	return ok
}

func identityHasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if strings.TrimSpace(scope) == want {
			return true
		}
	}
	return false
}

// authorizeToolCall is the single tenant-isolation gate for every MCP tool.
// Global identities (static super / ok_) are unrestricted. Tenant (tk_) keys
// may only act on agents that resolve to their own tenant and need an explicit
// scope for instrumentation write tools.
func (w *mcpServerWrapper) authorizeToolCall(ctx context.Context, request mcp.CallToolRequest) error {
	identity, ok := tenantauth.IdentityFromContext(ctx)
	if !ok {
		return errors.New("MCP 请求缺少已认证的租户身份")
	}
	if identity.IsGlobal() {
		return nil
	}

	if requiresInstrumentationWriteScope(request.Params.Name) &&
		!identityHasScope(identity.Scopes, instrumentationWriteScope) {
		return fmt.Errorf("该租户 key 缺少 %s scope，禁止执行动态插桩写操作", instrumentationWriteScope)
	}

	args := request.GetArguments()
	if agentID := strings.TrimSpace(stringArg(args, "agent_id")); agentID != "" {
		return w.authorizeAgent(ctx, agentID)
	}
	if sessionID := strings.TrimSpace(stringArg(args, "session_id")); sessionID != "" {
		session, found := w.sessionManager.GetSession(sessionID)
		if !found || session == nil {
			return errors.New("会话不存在或已过期")
		}
		return w.authorizeAgent(ctx, session.AgentID)
	}

	// Tools without an agent/session argument either filter their own result
	// (list_agents) or perform rule-level authorization (instrument_rule_*).
	return nil
}

func (w *mcpServerWrapper) authorizeAgent(ctx context.Context, agentID string) error {
	identity, _ := tenantauth.IdentityFromContext(ctx)
	if identity.IsGlobal() {
		return nil
	}
	agent, err := w.ext.controlPlane.GetAgent(ctx, agentID)
	if err != nil || agent == nil {
		return fmt.Errorf("无法确认 Agent 归属: %v", err)
	}
	tenant, err := w.instrumentationAuthorizer().tenantOfApp(ctx, agent.AppID)
	if err != nil {
		return fmt.Errorf("无法确认 Agent 所属租户: %w", err)
	}
	if tenant != identity.TenantID {
		return errors.New("禁止跨租户操作 Agent")
	}
	return nil
}

func (w *mcpServerWrapper) filterAgentsByTenant(
	ctx context.Context,
	agents []*agentregistry.AgentInfo,
) ([]*agentregistry.AgentInfo, error) {
	identity, ok := tenantauth.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("MCP 请求缺少已认证的租户身份")
	}
	if identity.IsGlobal() {
		return agents, nil
	}

	authorizer := w.instrumentationAuthorizer()
	tenants := make(map[string]string)
	out := make([]*agentregistry.AgentInfo, 0, len(agents))
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		tenant, cached := tenants[agent.AppID]
		if !cached {
			var err error
			tenant, err = authorizer.tenantOfApp(ctx, agent.AppID)
			if err != nil {
				return nil, fmt.Errorf("无法确认 Agent 所属租户: %w", err)
			}
			tenants[agent.AppID] = tenant
		}
		if tenant == identity.TenantID {
			out = append(out, agent)
		}
	}
	return out, nil
}
