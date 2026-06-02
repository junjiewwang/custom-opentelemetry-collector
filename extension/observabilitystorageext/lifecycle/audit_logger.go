// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// ZapAuditEmitter logs lifecycle events using structured zap logging.
// It implements AuditEmitter with SRP — only responsible for emission, not storage.
type ZapAuditEmitter struct {
	logger *zap.Logger
}

// NewZapAuditEmitter creates a new audit emitter backed by zap.
func NewZapAuditEmitter(logger *zap.Logger) *ZapAuditEmitter {
	return &ZapAuditEmitter{
		logger: logger.Named("lifecycle-audit"),
	}
}

// Emit logs the lifecycle event with structured fields.
func (e *ZapAuditEmitter) Emit(_ context.Context, event LifecycleEvent) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	fields := []zap.Field{
		zap.String("action", string(event.Action)),
		zap.String("signal", string(event.Signal)),
		zap.String("operator", event.Operator),
		zap.Bool("dry_run", event.DryRun),
		zap.Time("event_time", event.Timestamp),
	}

	if event.AppID != "" {
		fields = append(fields, zap.String("app_id", event.AppID))
	}
	if event.Input != nil {
		fields = append(fields, zap.Any("input", event.Input))
	}
	if event.Result != nil {
		fields = append(fields, zap.Any("result", event.Result))
	}
	if event.Error != "" {
		fields = append(fields, zap.String("error", event.Error))
	}

	switch {
	case event.Error != "":
		e.logger.Error("Lifecycle event", fields...)
	case event.Action == ActionAlert:
		e.logger.Warn("Lifecycle alert", fields...)
	default:
		e.logger.Info("Lifecycle event", fields...)
	}
}
