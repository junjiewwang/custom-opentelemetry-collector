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

const (
	metricReadScope = "metric:read"
	traceReadScope  = "trace:read"
	logReadScope    = "log:read"
)

var instrumentationWriteTools = map[string]struct{}{
	"instrument_rule_create": {},
	"instrument_rule_pause":  {},
	"instrument_rule_resume": {},
	"instrument_rule_delete": {},
}

// observabilityQueryTools maps each read-only query tool to the scope required
// for tenant (tk_) identities. Registration is conditional, but the gate is
// also enforced at call time so a hand-crafted request cannot bypass it.
var observabilityQueryTools = map[string]string{
	"metric_query":        metricReadScope,
	"metric_query_range":  metricReadScope,
	"metric_list_names":   metricReadScope,
	"metric_label_values": metricReadScope,
	"trace_search":        traceReadScope,
	"trace_get":           traceReadScope,
	"trace_services":      traceReadScope,
	"log_search":          logReadScope,
	"log_context":         logReadScope,
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
// Global identities (static super / ok_) are unrestricted except for the
// capability/tenant-binding checks that apply to every caller.
func (w *mcpServerWrapper) authorizeToolCall(ctx context.Context, request mcp.CallToolRequest) error {
	identity, ok := tenantauth.IdentityFromContext(ctx)
	if !ok {
		return errors.New("MCP 请求缺少已认证的租户身份")
	}

	if _, isQuery := observabilityQueryTools[request.Params.Name]; isQuery {
		if err := w.authorizeObservabilityQuery(ctx, identity, request); err != nil {
			return err
		}
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

// authorizeObservabilityQuery enforces fail-closed capability checks for the
// read-only query tools. Tenant scope is never read from tool arguments: the
// handlers overwrite it with the authenticated identity.
func (w *mcpServerWrapper) authorizeObservabilityQuery(
	ctx context.Context,
	identity tenantauth.Identity,
	request mcp.CallToolRequest,
) error {
	scope := observabilityQueryTools[request.Params.Name]
	cfg := w.ext.config.Observability
	if !cfg.Enabled {
		return errors.New("可观测性查询能力未启用")
	}

	switch scope {
	case metricReadScope:
		if !cfg.Signals.Metric || w.ext.metricReader == nil {
			return errors.New("metric 查询能力未启用或 Reader 不可用")
		}
	case traceReadScope:
		if !cfg.Signals.Trace || w.ext.traceReader == nil {
			return errors.New("trace 查询能力未启用或 Reader 不可用")
		}
	case logReadScope:
		if !cfg.Signals.Log || w.ext.logReader == nil {
			return errors.New("log 查询能力未启用或 Reader 不可用")
		}
	}

	args := request.GetArguments()
	if strings.TrimSpace(stringArg(args, "tenant_id")) != "" {
		return errors.New("tenant_id 由服务端根据认证身份决定，禁止在工具参数中指定")
	}
	if appID := strings.TrimSpace(stringArg(args, "app_id")); appID != "" {
		if err := w.instrumentationAuthorizer().authorize(ctx, appID); err != nil {
			return fmt.Errorf("app_id 归属校验失败: %w", err)
		}
	}

	if !identity.IsGlobal() && !identityHasScope(identity.Scopes, scope) {
		return fmt.Errorf("该租户 key 缺少 %s scope，禁止执行可观测性查询", scope)
	}
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
