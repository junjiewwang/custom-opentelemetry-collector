// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
)

// mcpCreatedByPrefix marks rules created through the MCP tools. It is also the
// ownership boundary used by the manage_only_own_rules guard.
const mcpCreatedByPrefix = "mcp:"

// agentRegistryProvider is an optional capability of the control plane. The
// concrete controlplaneext.Extension implements it; consumers fall back to
// GetOnlineAgents when it is unavailable.
type agentRegistryProvider interface {
	GetAgentRegistry() agentregistry.AgentRegistry
}

// instrumentationPolicy holds the guard rails for the dynamic instrumentation
// tools. It is deliberately independent from the rule manager: the policy is an
// MCP-facing concern, while parameter validation remains in the manager.
type instrumentationPolicy struct {
	cfg InstrumentationConfig
}

func newInstrumentationPolicy(cfg InstrumentationConfig) *instrumentationPolicy {
	if len(cfg.DeniedClassPrefixes) == 0 {
		cfg.DeniedClassPrefixes = append([]string(nil), defaultDeniedClassPrefixes...)
	}
	if cfg.MaxTargets <= 0 {
		cfg.MaxTargets = defaultInstrumentationMaxTargets
	}
	return &instrumentationPolicy{cfg: cfg}
}

func (p *instrumentationPolicy) maxTargets() int {
	return p.cfg.MaxTargets
}

func (p *instrumentationPolicy) validateClass(className string) error {
	name := strings.TrimSpace(className)
	if name == "" {
		return errors.New("class_name 不能为空")
	}
	if len(p.cfg.AllowedClassPrefixes) > 0 {
		for _, prefix := range p.cfg.AllowedClassPrefixes {
			if strings.HasPrefix(name, prefix) {
				return nil
			}
		}
		return fmt.Errorf("class_name %q 不在配置的允许前缀白名单内", name)
	}
	for _, prefix := range p.cfg.DeniedClassPrefixes {
		if strings.HasPrefix(name, prefix) {
			return fmt.Errorf("class_name %q 命中禁用前缀 %q", name, prefix)
		}
	}
	return nil
}

func (p *instrumentationPolicy) validateWriteFlags(force, captureArgs, captureReturn bool) error {
	if force && !p.cfg.AllowForce {
		return errors.New("force 参数未启用（allow_force=false）")
	}
	if (captureArgs || captureReturn) && !p.cfg.AllowCapture {
		return errors.New("参数/返回值采集未启用（allow_capture=false）")
	}
	return nil
}

// countServiceInstances mirrors the manager's service-scope target resolution.
// It prefers the shared agent registry so offline instances are counted too.
func (p *instrumentationPolicy) countServiceInstances(
	ctx context.Context,
	cp controlplaneext.ControlPlane,
	appID, serviceName string,
) (int, error) {
	if cp == nil {
		return 0, errors.New("控制面不可用")
	}
	if provider, ok := cp.(agentRegistryProvider); ok {
		if reg := provider.GetAgentRegistry(); reg != nil {
			instances, err := reg.GetInstancesByService(ctx, appID, serviceName)
			if err == nil {
				return len(instances), nil
			}
		}
	}
	agents, err := cp.GetOnlineAgents(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, agent := range agents {
		if agent != nil && agent.AppID == appID && agent.ServiceName == serviceName {
			count++
		}
	}
	return count, nil
}

func (p *instrumentationPolicy) validateTargetCount(count int) error {
	if count > p.cfg.MaxTargets {
		return fmt.Errorf("service 下实例数 %d 超过上限 max_targets=%d", count, p.cfg.MaxTargets)
	}
	return nil
}

func (p *instrumentationPolicy) canManage(rule *instrumentationmanager.Rule) bool {
	if rule == nil {
		return false
	}
	if !p.cfg.ManageOnlyOwnRules {
		return true
	}
	return strings.HasPrefix(rule.CreatedBy, mcpCreatedByPrefix)
}
