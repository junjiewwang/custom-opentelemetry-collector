// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

const (
	typeStr   = "testdatagen"
	stability = component.StabilityLevelDevelopment
)

// Type 组件类型标识
var Type = component.MustNewType(typeStr)

// createDefaultConfig 创建默认配置
func createDefaultConfig() *Config {
	return &Config{
		Interval: 10 * time.Second,
	}
}

// receiverInstances 缓存 receiver 实例（同一配置复用同一实例）
var (
	receiverMu        sync.Mutex
	receiverInstances = map[*Config]*testDataGenReceiver{}
)

// getOrCreateReceiver 获取或创建 Receiver 实例
func getOrCreateReceiver(set receiver.Settings, cfg *Config) (*testDataGenReceiver, error) {
	receiverMu.Lock()
	defer receiverMu.Unlock()

	if r, ok := receiverInstances[cfg]; ok {
		return r, nil
	}

	r := newReceiver(cfg, set.Logger)
	receiverInstances[cfg] = r
	return r, nil
}

// NewFactory 创建 TestDataGen Receiver 的 Factory
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		Type,
		func() component.Config { return createDefaultConfig() },
		receiver.WithTraces(createTracesReceiver, stability),
		receiver.WithMetrics(createMetricsReceiver, stability),
	)
}

func createTracesReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	consumer consumer.Traces,
) (receiver.Traces, error) {
	oCfg := cfg.(*Config)
	r, err := getOrCreateReceiver(set, oCfg)
	if err != nil {
		return nil, err
	}
	r.registerTracesConsumer(consumer)
	return r, nil
}

func createMetricsReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	consumer consumer.Metrics,
) (receiver.Metrics, error) {
	oCfg := cfg.(*Config)
	r, err := getOrCreateReceiver(set, oCfg)
	if err != nil {
		return nil, err
	}
	r.registerMetricsConsumer(consumer)
	return r, nil
}
