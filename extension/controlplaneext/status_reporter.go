// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// StatusReporter manages agent status reporting.
type StatusReporter struct {
	logger *zap.Logger
	config StatusReporterConfig

	mu            sync.RWMutex
	healthState   string // "healthy", "degraded", "unhealthy"
	configVersion string

	// Metrics
	successCount atomic.Int64
	failureCount atomic.Int64

	// Lifecycle
	stopChan chan struct{}
	wg       sync.WaitGroup
	started  bool
}

// newStatusReporter creates a new status reporter.
func newStatusReporter(logger *zap.Logger, config StatusReporterConfig) *StatusReporter {
	return &StatusReporter{
		logger:      logger,
		config:      config,
		healthState: "healthy",
		stopChan:    make(chan struct{}),
	}
}

// Start starts the status reporter.
func (r *StatusReporter) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return nil
	}

	r.logger.Info("Starting status reporter")

	// Start health check goroutine
	r.wg.Add(1)
	go r.healthCheckLoop()

	r.started = true
	return nil
}

// Shutdown stops the status reporter.
func (r *StatusReporter) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = false
	r.mu.Unlock()

	close(r.stopChan)

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		r.logger.Info("Status reporter shutdown complete")
	case <-ctx.Done():
		r.logger.Warn("Status reporter shutdown timed out")
	}

	return nil
}

// SetConfigVersion sets the current config version.
func (r *StatusReporter) SetConfigVersion(version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configVersion = version
}

// GetConfigVersion returns the current config version.
func (r *StatusReporter) GetConfigVersion() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.configVersion
}

// GetHealthState returns the current health state.
func (r *StatusReporter) GetHealthState() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.healthState
}

// RecordSuccess records a successful operation.
func (r *StatusReporter) RecordSuccess() {
	r.successCount.Add(1)
}

// RecordFailure records a failed operation.
func (r *StatusReporter) RecordFailure() {
	r.failureCount.Add(1)
}

// GetMetrics returns the current metrics.
func (r *StatusReporter) GetMetrics() (successCount, failureCount int64, successRate float64) {
	successCount = r.successCount.Load()
	failureCount = r.failureCount.Load()
	total := successCount + failureCount
	if total > 0 {
		successRate = float64(successCount) / float64(total)
	}
	return
}

// healthCheckLoop periodically updates health status.
func (r *StatusReporter) healthCheckLoop() {
	defer r.wg.Done()

	interval := r.config.HealthCheckInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopChan:
			return
		case <-ticker.C:
			r.updateHealthState()
		}
	}
}

// updateHealthState updates the health state based on metrics.
func (r *StatusReporter) updateHealthState() {
	successCount := r.successCount.Load()
	failureCount := r.failureCount.Load()
	total := successCount + failureCount

	if total == 0 {
		return
	}

	successRate := float64(successCount) / float64(total)

	r.mu.Lock()
	defer r.mu.Unlock()

	switch {
	case successRate >= 0.99:
		r.healthState = "healthy"
	case successRate >= 0.90:
		r.healthState = "degraded"
	default:
		r.healthState = "unhealthy"
	}
}
