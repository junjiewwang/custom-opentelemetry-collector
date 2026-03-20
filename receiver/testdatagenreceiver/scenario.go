// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// DataType 场景产出的数据类型
type DataType int

const (
	// DataTypeTraces 产出 Trace 数据
	DataTypeTraces DataType = iota
	// DataTypeMetrics 产出 Metric 数据
	DataTypeMetrics
)

// Scenario 定义测试数据生成场景的接口
// 每个场景负责生成特定类型的遥测数据
type Scenario interface {
	// Name 场景唯一标识
	Name() string

	// Type 场景产出的数据类型
	Type() DataType

	// Init 根据用户配置初始化场景，解析 ScenarioConfig.Config 中的键值对
	Init(cfg map[string]interface{}) error

	// GenerateTraces 生成 Trace 数据（仅 DataTypeTraces 场景实现）
	GenerateTraces() (ptrace.Traces, error)

	// GenerateMetrics 生成 Metric 数据（仅 DataTypeMetrics 场景实现）
	GenerateMetrics() (pmetric.Metrics, error)
}

// BaseScenario 提供 Scenario 接口的默认实现基类
// 子类只需嵌入并覆盖需要的方法
type BaseScenario struct {
	ScenarioName string
	ScenarioType DataType
}

// Name 返回场景名称
func (b *BaseScenario) Name() string {
	return b.ScenarioName
}

// Type 返回场景数据类型
func (b *BaseScenario) Type() DataType {
	return b.ScenarioType
}

// Init 默认空实现
func (b *BaseScenario) Init(_ map[string]interface{}) error {
	return nil
}

// GenerateTraces 默认返回空 Traces
func (b *BaseScenario) GenerateTraces() (ptrace.Traces, error) {
	return ptrace.NewTraces(), nil
}

// GenerateMetrics 默认返回空 Metrics
func (b *BaseScenario) GenerateMetrics() (pmetric.Metrics, error) {
	return pmetric.NewMetrics(), nil
}
