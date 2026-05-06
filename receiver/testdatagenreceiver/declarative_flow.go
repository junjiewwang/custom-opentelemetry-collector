// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"fmt"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

// DeclarativeFlow 声明式业务流实现
// 通过配置驱动，将图拓扑自动转换为 FlowStep 树并执行
type DeclarativeFlow struct {
	name        string
	description string
	errorRate   float64
	flowCfg     *DeclarativeFlowConfig
	resolver    *TopologyResolver
	rootSteps   []*FlowStep // 解析后缓存的 FlowStep 树
}

// NewDeclarativeFlow 创建声明式业务流实例
func NewDeclarativeFlow(flowCfg *DeclarativeFlowConfig, globalCfg *Config) (*DeclarativeFlow, error) {
	resolver := NewTopologyResolver(globalCfg)

	// 解析拓扑为 FlowStep 树
	rootSteps, err := resolver.Resolve(flowCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve topology for flow %q: %w", flowCfg.Name, err)
	}

	description := flowCfg.Description
	if description == "" {
		description = fmt.Sprintf("声明式业务流: %s", flowCfg.Name)
	}

	return &DeclarativeFlow{
		name:        flowCfg.Name,
		description: description,
		errorRate:   flowCfg.ErrorRate,
		flowCfg:     flowCfg,
		resolver:    resolver,
		rootSteps:   rootSteps,
	}, nil
}

// Name 业务流唯一标识
func (f *DeclarativeFlow) Name() string {
	return f.name
}

// Description 业务流描述
func (f *DeclarativeFlow) Description() string {
	return f.description
}

// Init 声明式 Flow 不需要额外初始化（配置已在构造时解析）
func (f *DeclarativeFlow) Init(_ map[string]interface{}) error {
	return nil
}

// GenerateTraces 生成一条完整的业务调用链 Trace
// 每次调用都会重新解析拓扑以获得新的随机数据
func (f *DeclarativeFlow) GenerateTraces() (ptrace.Traces, error) {
	// 每次生成都重新解析拓扑，以获得独立的 FlowStep 实例（避免状态污染）
	rootSteps, err := f.resolver.Resolve(f.flowCfg)
	if err != nil {
		return ptrace.NewTraces(), fmt.Errorf("failed to resolve topology: %w", err)
	}

	td := ExecuteFlow(rootSteps, f.errorRate)
	return td, nil
}
