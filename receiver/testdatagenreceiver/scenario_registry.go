// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"fmt"
	"sync"
)

// ScenarioFactory 场景工厂函数类型
type ScenarioFactory func() Scenario

// FlowFactory 业务流工厂函数类型
type FlowFactory func() BusinessFlow

// globalRegistry 全局场景注册中心（单例）
var globalRegistry = &scenarioRegistry{
	factories:     make(map[string]ScenarioFactory),
	flowFactories: make(map[string]FlowFactory),
}

// scenarioRegistry 场景注册中心，管理所有可用场景和业务流的工厂函数
type scenarioRegistry struct {
	mu            sync.RWMutex
	factories     map[string]ScenarioFactory
	flowFactories map[string]FlowFactory
}

// Register 注册一个场景工厂函数
// 如果名称已存在，会 panic（通常在 init() 中调用，重复注册说明代码有问题）
func Register(name string, factory ScenarioFactory) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()

	if _, exists := globalRegistry.factories[name]; exists {
		panic(fmt.Sprintf("testdatagen: scenario %q already registered", name))
	}
	globalRegistry.factories[name] = factory
}

// NewScenario 根据名称创建场景实例
func NewScenario(name string) (Scenario, error) {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	factory, exists := globalRegistry.factories[name]
	if !exists {
		return nil, fmt.Errorf("testdatagen: unknown scenario %q, available: %v", name, globalRegistry.availableNames())
	}
	return factory(), nil
}

// availableNames 返回所有已注册的场景名称（内部方法，需持有读锁）
func (r *scenarioRegistry) availableNames() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// RegisterFlow 注册一个业务流工厂函数
func RegisterFlow(name string, factory FlowFactory) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()

	if _, exists := globalRegistry.flowFactories[name]; exists {
		panic(fmt.Sprintf("testdatagen: flow %q already registered", name))
	}
	globalRegistry.flowFactories[name] = factory
}

// NewFlow 根据名称创建业务流实例
func NewFlow(name string) (BusinessFlow, error) {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	factory, exists := globalRegistry.flowFactories[name]
	if !exists {
		return nil, fmt.Errorf("testdatagen: unknown flow %q, available: %v", name, globalRegistry.availableFlowNames())
	}
	return factory(), nil
}

// availableFlowNames 返回所有已注册的业务流名称
func (r *scenarioRegistry) availableFlowNames() []string {
	names := make([]string, 0, len(r.flowFactories))
	for name := range r.flowFactories {
		names = append(names, name)
	}
	return names
}
