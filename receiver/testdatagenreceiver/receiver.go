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
// 负责调度所有已启用的业务流，定时生成测试数据并推送给 nextConsumer
type testDataGenReceiver struct {
	cfg            *Config
	logger         *zap.Logger
	tracesConsumer consumer.Traces
	metricConsumer consumer.Metrics

	flows []BusinessFlow // 已初始化的业务流实例列表

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

// Start 启动 Receiver，初始化业务流并开始定时调度
func (r *testDataGenReceiver) Start(_ context.Context, _ component.Host) error {
	r.logger.Info("TestDataGen Receiver starting",
		zap.Duration("interval", r.cfg.Interval),
		zap.Int("system_count", len(r.cfg.Systems)),
		zap.Int("flow_count", len(r.cfg.Flows)),
	)

	// 初始化声明式业务流
	for i := range r.cfg.Flows {
		fl := &r.cfg.Flows[i]
		if !fl.Enabled {
			r.logger.Info("Flow disabled, skipping", zap.String("name", fl.Name))
			continue
		}

		flow, err := NewDeclarativeFlow(fl, r.cfg)
		if err != nil {
			return fmt.Errorf("failed to create flow %q: %w", fl.Name, err)
		}

		r.flows = append(r.flows, flow)
		r.logger.Info("Flow initialized",
			zap.String("name", fl.Name),
			zap.String("description", flow.Description()),
		)
	}

	if len(r.flows) == 0 {
		r.logger.Warn("No enabled flows, receiver will idle")
		return nil
	}

	// 启动调度 goroutine
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	r.wg.Add(1)
	go r.runScheduler(ctx)

	r.logger.Info("TestDataGen Receiver started successfully",
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
	r.executeAllFlows(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.executeAllFlows(ctx)
		}
	}
}

// executeAllFlows 执行所有业务流并推送数据
func (r *testDataGenReceiver) executeAllFlows(ctx context.Context) {
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