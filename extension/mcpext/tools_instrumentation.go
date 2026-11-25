// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

// instrumentRuleAction is the common signature of PauseRule, ResumeRule and
// DeleteRule, allowing a single handler for all three tools.
type instrumentRuleAction func(context.Context, string) (*instrumentationmanager.Rule, error)

const instrumentRuleIDDescription = "规则 ID。可通过 instrument_rule_list 工具获取。"

func (w *mcpServerWrapper) instrumentationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeoutCtx, cancel := context.WithTimeout(ctx, w.toolTimeout())
	actor := instrumentationmanager.Actor{
		ID:   mcpPrincipal(timeoutCtx),
		Type: instrumentationmanager.ActorTypeMCP,
	}
	if identity, ok := tenantauth.IdentityFromContext(timeoutCtx); ok {
		actor.TenantID = identity.TenantID
	}
	return instrumentationmanager.WithActor(timeoutCtx, actor), cancel
}

func (w *mcpServerWrapper) instrumentationManagerOrError() (instrumentationmanager.InstrumentationManager, *mcp.CallToolResult) {
	if w.ext.instrMgr == nil || w.instrPolicy == nil {
		return nil, mcp.NewToolResultError("动态插桩能力未启用或规则中心不可用")
	}
	return w.ext.instrMgr, nil
}

func stringArg(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return strings.TrimSpace(value)
}

func boolArg(args map[string]any, name string) bool {
	value, _ := args[name].(bool)
	return value
}

func (w *mcpServerWrapper) auditInstrumentationWrite(ctx context.Context, toolName, ruleID string, err error) {
	fields := []zap.Field{
		zap.String("tool", toolName),
		zap.String("principal", mcpPrincipal(ctx)),
		zap.String("rule_id", ruleID),
	}
	if err != nil {
		w.logger.Warn("instrumentation write rejected or failed", append(fields, zap.Error(err))...)
		return
	}
	w.logger.Info("instrumentation write", fields...)
}

// manageableRuleError returns an empty string when the rule may be modified.
func (w *mcpServerWrapper) manageableRuleError(
	ctx context.Context,
	mgr instrumentationmanager.InstrumentationManager,
	ruleID string,
) string {
	rule, err := mgr.GetRule(ctx, ruleID)
	if err != nil {
		return "查询规则失败: " + err.Error()
	}
	if err := w.instrumentationAuthorizer().authorizeRule(ctx, rule); err != nil {
		return "租户授权失败: " + err.Error()
	}
	if !w.instrPolicy.canManage(rule) {
		return "该规则并非由 MCP 创建，禁止修改（manage_only_own_rules=true）"
	}
	return ""
}

// ========== Registration ==========

func (w *mcpServerWrapper) registerInstrumentationTools() {
	w.registerInstrumentRuleListTool()
	w.registerInstrumentRuleGetTool()
	w.registerInstrumentRuleSnapshotTool()

	if !w.ext.config.Instrumentation.AllowWrite {
		return
	}
	mgr := w.ext.instrMgr
	w.registerInstrumentRuleCreateTool()
	w.registerInstrumentRuleActionTool(
		"instrument_rule_pause", "暂停动态插桩规则，向目标实例下发取消插桩任务。", mgr.PauseRule)
	w.registerInstrumentRuleActionTool(
		"instrument_rule_resume", "恢复已暂停的动态插桩规则，重新下发插桩任务。", mgr.ResumeRule)
	w.registerInstrumentRuleActionTool(
		"instrument_rule_delete", "删除动态插桩规则（软删除），向目标实例下发取消插桩任务。", mgr.DeleteRule)
}

func (w *mcpServerWrapper) registerInstrumentRuleListTool() {
	tool := mcp.NewTool("instrument_rule_list",
		mcp.WithDescription("列出动态插桩规则。支持按应用、服务、插桩类型、期望状态和关键字过滤。"),
		mcp.WithString("app_id", mcp.Description("应用 ID，精确匹配。")),
		mcp.WithString("service_name", mcp.Description("服务名，精确匹配。")),
		mcp.WithString("instrument_type", mcp.Description("插桩类型：trace / metric / log。")),
		mcp.WithString("desired_state", mcp.Description("期望状态：active / paused / deleted。")),
		mcp.WithString("search", mcp.Description("按规则名或类名模糊搜索。")),
		mcp.WithBoolean("include_deleted", mcp.Description("是否包含已删除规则，默认 false。")),
	)
	w.addTool(tool, w.handleInstrumentRuleList)
}

func (w *mcpServerWrapper) registerInstrumentRuleGetTool() {
	tool := mcp.NewTool("instrument_rule_get",
		mcp.WithDescription("查询单条动态插桩规则及其各目标实例的执行状态。创建规则后可用本工具轮询生效进度。"),
		mcp.WithString("rule_id", mcp.Required(), mcp.Description(instrumentRuleIDDescription)),
	)
	w.addTool(tool, w.handleInstrumentRuleGet)
}

func (w *mcpServerWrapper) registerInstrumentRuleSnapshotTool() {
	tool := mcp.NewTool("instrument_rule_snapshot",
		mcp.WithDescription("查询规则的运行时快照（各实例上插桩是否真正生效、是否漂移）。refresh=true 时主动下发查询任务刷新。"),
		mcp.WithString("rule_id", mcp.Required(), mcp.Description(instrumentRuleIDDescription)),
		mcp.WithBoolean("refresh", mcp.Description("是否主动刷新运行时快照，默认 false。")),
	)
	w.addTool(tool, w.handleInstrumentRuleSnapshot)
}

func (w *mcpServerWrapper) registerInstrumentRuleCreateTool() {
	tool := mcp.NewTool("instrument_rule_create",
		mcp.WithDescription(fmt.Sprintf(
			"创建动态插桩规则。作用范围固定为 service 下的全部实例，单条规则目标实例数上限 %d。"+
				"规则创建是异步的：返回 rule_id 后请用 instrument_rule_get 轮询状态，"+
				"再用 instrument_rule_snapshot 确认运行时是否生效。", w.instrPolicy.maxTargets())),
		mcp.WithString("app_id", mcp.Required(), mcp.Description("应用 ID。")),
		mcp.WithString("service_name", mcp.Required(), mcp.Description("服务名；规则作用于该服务下的所有实例。")),
		mcp.WithString("class_name", mcp.Required(), mcp.Description("完整类名，例如 com.example.OrderService。")),
		mcp.WithString("method_name", mcp.Required(), mcp.Description("方法名，例如 createOrder。")),
		mcp.WithString("instrument_type", mcp.Description("插桩类型：trace / metric / log，默认 trace。")),
		mcp.WithString("name", mcp.Description("规则名称，不填则自动生成。")),
		mcp.WithString("description", mcp.Description("规则说明。")),
		mcp.WithString("parameter_types", mcp.Description("方法参数类型，用于重载方法定位。")),
		mcp.WithString("method_descriptor", mcp.Description("方法描述符，用于重载方法定位。")),
		mcp.WithString("span_name", mcp.Description("自定义 span 名称。")),
		mcp.WithBoolean("force", mcp.Description("是否强制插桩，默认 false；需 allow_force=true。")),
		mcp.WithBoolean("capture_args", mcp.Description("是否采集方法参数，默认 false；需 allow_capture=true。")),
		mcp.WithBoolean("capture_return", mcp.Description("是否采集返回值，默认 false；需 allow_capture=true。")),
	)
	w.addTool(tool, w.handleInstrumentRuleCreate)
}

func (w *mcpServerWrapper) registerInstrumentRuleActionTool(name, description string, action instrumentRuleAction) {
	tool := mcp.NewTool(name,
		mcp.WithDescription(description),
		mcp.WithString("rule_id", mcp.Required(), mcp.Description(instrumentRuleIDDescription)),
	)
	w.addTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return w.handleInstrumentRuleAction(ctx, request, name, action)
	})
}

// ========== Handlers ==========

func (w *mcpServerWrapper) handleInstrumentRuleList(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	ctx, cancel := w.instrumentationContext(ctx)
	defer cancel()

	mgr, errResult := w.instrumentationManagerOrError()
	if errResult != nil {
		return errResult, nil
	}
	args := request.GetArguments()
	rules, err := mgr.ListRules(ctx, instrumentationmanager.ListRulesQuery{
		AppID:          stringArg(args, "app_id"),
		ServiceName:    stringArg(args, "service_name"),
		InstrumentType: instrumentationmanager.InstrumentType(stringArg(args, "instrument_type")),
		DesiredState:   instrumentationmanager.RuleDesiredState(stringArg(args, "desired_state")),
		Search:         stringArg(args, "search"),
		IncludeDeleted: boolArg(args, "include_deleted"),
	})
	if err != nil {
		return mcp.NewToolResultError("查询动态插桩规则失败: " + err.Error()), nil
	}
	rules, err = w.instrumentationAuthorizer().filterRules(ctx, rules)
	if err != nil {
		return mcp.NewToolResultError("租户授权失败: " + err.Error()), nil
	}
	if len(rules) == 0 {
		return mcp.NewToolResultText("当前没有匹配的动态插桩规则。"), nil
	}
	result, err := formatInstrumentRuleList(rules)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(result), nil
}

func (w *mcpServerWrapper) handleInstrumentRuleGet(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	ctx, cancel := w.instrumentationContext(ctx)
	defer cancel()

	mgr, errResult := w.instrumentationManagerOrError()
	if errResult != nil {
		return errResult, nil
	}
	ruleID := stringArg(request.GetArguments(), "rule_id")
	if ruleID == "" {
		return mcp.NewToolResultError("参数 rule_id 不能为空"), nil
	}
	rule, err := mgr.GetRule(ctx, ruleID)
	if err != nil {
		return mcp.NewToolResultError("查询规则失败: " + err.Error()), nil
	}
	if err := w.instrumentationAuthorizer().authorizeRule(ctx, rule); err != nil {
		return mcp.NewToolResultError("租户授权失败: " + err.Error()), nil
	}
	targets, err := mgr.ListTargetStatuses(ctx, ruleID)
	if err != nil {
		w.logger.Warn("[instrument_rule_get] Failed to list target statuses",
			zap.String("rule_id", ruleID), zap.Error(err))
	}
	result, err := formatInstrumentRuleDetail(rule, targets)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(result), nil
}

func (w *mcpServerWrapper) handleInstrumentRuleSnapshot(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	ctx, cancel := w.instrumentationContext(ctx)
	defer cancel()

	mgr, errResult := w.instrumentationManagerOrError()
	if errResult != nil {
		return errResult, nil
	}
	args := request.GetArguments()
	ruleID := stringArg(args, "rule_id")
	if ruleID == "" {
		return mcp.NewToolResultError("参数 rule_id 不能为空"), nil
	}
	ownedRule, authErr := mgr.GetRule(ctx, ruleID)
	if authErr != nil {
		return mcp.NewToolResultError("查询规则失败: " + authErr.Error()), nil
	}
	if authErr := w.instrumentationAuthorizer().authorizeRule(ctx, ownedRule); authErr != nil {
		return mcp.NewToolResultError("租户授权失败: " + authErr.Error()), nil
	}

	var (
		snapshot *instrumentationmanager.RuleRuntimeSnapshot
		err      error
	)
	if boolArg(args, "refresh") {
		snapshot, err = mgr.RefreshRuleRuntimeSnapshot(ctx, ruleID)
	} else {
		snapshot, err = mgr.GetRuleRuntimeSnapshot(ctx, ruleID)
	}
	if err != nil {
		return mcp.NewToolResultError("查询运行时快照失败: " + err.Error()), nil
	}
	if snapshot == nil {
		return mcp.NewToolResultText("暂无运行时快照，可设置 refresh=true 主动刷新。"), nil
	}
	result, err := formatInstrumentSnapshot(snapshot)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(result), nil
}

func (w *mcpServerWrapper) handleInstrumentRuleCreate(
	ctx context.Context, request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	ctx, cancel := w.instrumentationContext(ctx)
	defer cancel()

	mgr, errResult := w.instrumentationManagerOrError()
	if errResult != nil {
		return errResult, nil
	}
	args := request.GetArguments()
	appID := stringArg(args, "app_id")
	serviceName := stringArg(args, "service_name")
	className := stringArg(args, "class_name")
	methodName := stringArg(args, "method_name")
	instrumentType := instrumentationmanager.InstrumentType(stringArg(args, "instrument_type"))
	if instrumentType == "" {
		instrumentType = instrumentationmanager.InstrumentTypeTrace
	}
	force := boolArg(args, "force")
	captureArgs := boolArg(args, "capture_args")
	captureReturn := boolArg(args, "capture_return")

	if appID == "" || serviceName == "" {
		w.auditInstrumentationWrite(ctx, "instrument_rule_create", "", errors.New("missing service scope"))
		return mcp.NewToolResultError("参数 app_id 与 service_name 不能为空"), nil
	}
	if className == "" || methodName == "" {
		w.auditInstrumentationWrite(ctx, "instrument_rule_create", "", errors.New("missing class or method"))
		return mcp.NewToolResultError("参数 class_name 与 method_name 不能为空"), nil
	}
	if err := w.instrumentationAuthorizer().authorize(ctx, appID); err != nil {
		w.auditInstrumentationWrite(ctx, "instrument_rule_create", "", err)
		return mcp.NewToolResultError("租户授权失败: " + err.Error()), nil
	}

	// MCP-facing policy: scope, class prefixes, sensitive captures and fan-out.
	if err := w.instrPolicy.validateClass(className); err != nil {
		w.auditInstrumentationWrite(ctx, "instrument_rule_create", "", err)
		return mcp.NewToolResultError("策略拒绝: " + err.Error()), nil
	}
	if err := w.instrPolicy.validateWriteFlags(force, captureArgs, captureReturn); err != nil {
		w.auditInstrumentationWrite(ctx, "instrument_rule_create", "", err)
		return mcp.NewToolResultError("策略拒绝: " + err.Error()), nil
	}
	count, err := w.instrPolicy.countServiceInstances(ctx, w.ext.controlPlane, appID, serviceName)
	if err != nil {
		w.auditInstrumentationWrite(ctx, "instrument_rule_create", "", err)
		return mcp.NewToolResultError("无法确认目标实例数: " + err.Error()), nil
	}
	if err := w.instrPolicy.validateTargetCount(count); err != nil {
		w.auditInstrumentationWrite(ctx, "instrument_rule_create", "", err)
		return mcp.NewToolResultError("策略拒绝: " + err.Error()), nil
	}

	req := &instrumentationmanager.CreateRuleRequest{
		Name:             stringArg(args, "name"),
		Description:      stringArg(args, "description"),
		AppID:            appID,
		ServiceName:      serviceName,
		ScopeType:        instrumentationmanager.ScopeTypeService,
		ClassName:        className,
		MethodName:       methodName,
		ParameterTypes:   stringArg(args, "parameter_types"),
		MethodDescriptor: stringArg(args, "method_descriptor"),
		InstrumentType:   instrumentType,
		SpanName:         stringArg(args, "span_name"),
		Force:            force,
		CreatedBy:        mcpPrincipal(ctx),
	}
	if captureArgs {
		req.CaptureArgs = "true"
	}
	if captureReturn {
		req.CaptureReturn = "true"
	}

	rule, err := mgr.CreateRule(ctx, req)
	if err != nil {
		w.auditInstrumentationWrite(ctx, "instrument_rule_create", "", err)
		return mcp.NewToolResultError("创建动态插桩规则失败: " + err.Error()), nil
	}
	w.auditInstrumentationWrite(ctx, "instrument_rule_create", rule.ID, nil)
	result, err := formatInstrumentRuleDetail(rule, nil)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText("规则已创建（生效是异步的，请用 instrument_rule_get 轮询状态）：\n" + result), nil
}

func (w *mcpServerWrapper) handleInstrumentRuleAction(
	ctx context.Context,
	request mcp.CallToolRequest,
	toolName string,
	action instrumentRuleAction,
) (*mcp.CallToolResult, error) {
	ctx, cancel := w.instrumentationContext(ctx)
	defer cancel()

	mgr, errResult := w.instrumentationManagerOrError()
	if errResult != nil {
		return errResult, nil
	}
	ruleID := stringArg(request.GetArguments(), "rule_id")
	if ruleID == "" {
		return mcp.NewToolResultError("参数 rule_id 不能为空"), nil
	}
	if msg := w.manageableRuleError(ctx, mgr, ruleID); msg != "" {
		w.auditInstrumentationWrite(ctx, toolName, ruleID, errors.New(msg))
		return mcp.NewToolResultError(msg), nil
	}

	rule, err := action(ctx, ruleID)
	if err != nil {
		w.auditInstrumentationWrite(ctx, toolName, ruleID, err)
		return mcp.NewToolResultError(toolName + " 失败: " + err.Error()), nil
	}
	w.auditInstrumentationWrite(ctx, toolName, ruleID, nil)
	result, err := formatInstrumentRuleDetail(rule, nil)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(result), nil
}
