// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.uber.org/zap"
)

// testDataGenReceiver 是 TestDataGen Receiver 的核心实现
// 负责调度所有已启用的场景和业务流，定时生成测试数据并推送给 nextConsumer
type testDataGenReceiver struct {
	cfg            *Config
	logger         *zap.Logger
	tracesConsumer consumer.Traces
	metricConsumer consumer.Metrics

	scenarios []Scenario     // 已初始化的场景实例列表
	flows     []BusinessFlow // 已初始化的业务流实例列表

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newReceiver 创建 Receiver 实例
func newReceiver(cfg *Config, logger *zap.Logger) *testDataGenReceiver {
	return &testDataGenReceiver{
		cfg:    cfg,
		logger: logger,
	}
}

// Start 启动 Receiver，初始化场景和业务流并开始定时调度
func (r *testDataGenReceiver) Start(_ context.Context, _ component.Host) error {
	r.logger.Info("TestDataGen Receiver starting",
		zap.Duration("interval", r.cfg.Interval),
		zap.Int("scenario_count", len(r.cfg.Scenarios)),
		zap.Int("flow_count", len(r.cfg.Flows)),
	)

	// 初始化所有已启用的场景
	for _, sc := range r.cfg.Scenarios {
		if !sc.Enabled {
			r.logger.Info("Scenario disabled, skipping", zap.String("name", sc.Name))
			continue
		}

		scenario, err := NewScenario(sc.Name)
		if err != nil {
			return fmt.Errorf("failed to create scenario %q: %w", sc.Name, err)
		}

		if err := scenario.Init(sc.Config); err != nil {
			return fmt.Errorf("failed to init scenario %q: %w", sc.Name, err)
		}

		r.scenarios = append(r.scenarios, scenario)
		r.logger.Info("Scenario initialized", zap.String("name", sc.Name), zap.String("type", dataTypeString(scenario.Type())))
	}

	// 初始化所有已启用的业务流
	for _, fl := range r.cfg.Flows {
		if !fl.Enabled {
			r.logger.Info("Flow disabled, skipping", zap.String("name", fl.Name))
			continue
		}

		flow, err := NewFlow(fl.Name)
		if err != nil {
			return fmt.Errorf("failed to create flow %q: %w", fl.Name, err)
		}

		if err := flow.Init(fl.Config); err != nil {
			return fmt.Errorf("failed to init flow %q: %w", fl.Name, err)
		}

		r.flows = append(r.flows, flow)
		r.logger.Info("Flow initialized", zap.String("name", fl.Name), zap.String("description", flow.Description()))
	}

	if len(r.scenarios) == 0 && len(r.flows) == 0 {
		r.logger.Warn("No enabled scenarios or flows, receiver will idle")
		return nil
	}

	// 启动调度 goroutine
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	r.wg.Add(1)
	go r.runScheduler(ctx)

	r.logger.Info("TestDataGen Receiver started successfully",
		zap.Int("active_scenarios", len(r.scenarios)),
		zap.Int("active_flows", len(r.flows)),
	)
	return nil
}

// Shutdown 优雅关闭
func (r *testDataGenReceiver) Shutdown(_ context.Context) error {
	r.logger.Info("TestDataGen Receiver shutting down")
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	r.logger.Info("TestDataGen Receiver stopped")
	return nil
}

// runScheduler 定时调度循环
func (r *testDataGenReceiver) runScheduler(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	// 启动后立即执行一次
	r.executeAllScenarios(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.executeAllScenarios(ctx)
		}
	}
}

// executeAllScenarios 执行所有场景和业务流并推送数据
func (r *testDataGenReceiver) executeAllScenarios(ctx context.Context) {
	// 1. 执行独立场景
	for _, sc := range r.scenarios {
		select {
		case <-ctx.Done():
			return
		default:
		}

		switch sc.Type() {
		case DataTypeTraces:
			if r.tracesConsumer == nil {
				continue
			}
			td, err := sc.GenerateTraces()
			if err != nil {
				r.logger.Error("Failed to generate traces",
					zap.String("scenario", sc.Name()),
					zap.Error(err),
				)
				continue
			}
			if td.SpanCount() == 0 {
				continue
			}
			if err := r.tracesConsumer.ConsumeTraces(ctx, td); err != nil {
				r.logger.Error("Failed to consume traces",
					zap.String("scenario", sc.Name()),
					zap.Error(err),
				)
			} else {
				r.logger.Debug("Traces generated",
					zap.String("scenario", sc.Name()),
					zap.Int("span_count", td.SpanCount()),
				)
			}

		case DataTypeMetrics:
			if r.metricConsumer == nil {
				continue
			}
			md, err := sc.GenerateMetrics()
			if err != nil {
				r.logger.Error("Failed to generate metrics",
					zap.String("scenario", sc.Name()),
					zap.Error(err),
				)
				continue
			}
			if md.MetricCount() == 0 {
				continue
			}
			if err := r.metricConsumer.ConsumeMetrics(ctx, md); err != nil {
				r.logger.Error("Failed to consume metrics",
					zap.String("scenario", sc.Name()),
					zap.Error(err),
				)
			} else {
				r.logger.Debug("Metrics generated",
					zap.String("scenario", sc.Name()),
					zap.Int("metric_count", md.MetricCount()),
				)
			}
		}
	}

	// 2. 执行业务流
	for _, fl := range r.flows {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if r.tracesConsumer == nil {
			continue
		}

		td, err := fl.GenerateTraces()
		if err != nil {
			r.logger.Error("Failed to generate flow traces",
				zap.String("flow", fl.Name()),
				zap.Error(err),
			)
			continue
		}
		if td.SpanCount() == 0 {
			continue
		}
		if err := r.tracesConsumer.ConsumeTraces(ctx, td); err != nil {
			r.logger.Error("Failed to consume flow traces",
				zap.String("flow", fl.Name()),
				zap.Error(err),
			)
		} else {
			r.logger.Debug("Flow traces generated",
				zap.String("flow", fl.Name()),
				zap.Int("span_count", td.SpanCount()),
				zap.Int("service_count", td.ResourceSpans().Len()),
			)
		}
	}
}

// registerTracesConsumer 注册 Traces Consumer
func (r *testDataGenReceiver) registerTracesConsumer(tc consumer.Traces) {
	r.tracesConsumer = tc
}

// registerMetricsConsumer 注册 Metrics Consumer
func (r *testDataGenReceiver) registerMetricsConsumer(mc consumer.Metrics) {
	r.metricConsumer = mc
}

// dataTypeString 将 DataType 转为可读字符串
func dataTypeString(dt DataType) string {
	switch dt {
	case DataTypeTraces:
		return "traces"
	case DataTypeMetrics:
		return "metrics"
	default:
		return "unknown"
	}
}
