// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"reflect"
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
		fields = append(fields, zap.Any("input", normalizeAuditValue(event.Input)))
	}
	if event.Result != nil {
		fields = append(fields, zap.Any("result", normalizeAuditValue(event.Result)))
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

// normalizeAuditValue converts struct values to map[string]any so a given audit
// field keeps a single shape across emit sites. The lifecycle scheduler sets
// LifecycleEvent.Result to either a map[string]any (per-app purge) or a
// *PurgeResult / *PurgeEstimate struct (single-signal purge). If a struct
// reaches zap.Any verbatim, the OTel log bridge stringifies it with
// fmt.Sprintf("%v"), so `result` becomes a scalar string in one log and a
// nested object in another — Elasticsearch then refuses to merge
// `attributes.result` (text vs object) and drops the audit doc. Converting
// structs to maps (honoring their JSON tags) keeps `result` an object
// everywhere. Maps, slices and scalars pass through unchanged.
func normalizeAuditValue(v any) any {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return v
	}
	// Dereference pointers so a typed nil or *PurgeResult both resolve to their
	// underlying kind. A nil pointer is left as-is (zap renders it null).
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return v
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return v
	}

	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		// Struct marshals to a non-object (e.g. time.Time → string); keep it.
		return v
	}
	return m
}
