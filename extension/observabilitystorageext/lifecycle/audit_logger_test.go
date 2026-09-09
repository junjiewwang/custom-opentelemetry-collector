// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"testing"
	"time"
)

// TestNormalizeAuditValue_StructBecomesMap is the regression test for the ES
// mapping conflict: a *PurgeResult (struct) must normalize to map[string]any,
// not remain a struct that the OTel log bridge would stringify into a scalar.
func TestNormalizeAuditValue_StructBecomesMap(t *testing.T) {
	in := &PurgeResult{
		Signal:       "metric",
		DeletedDocs:  0,
		DeletedUnits: 3,
		FreedBytes:   0,
		Message:      "deleted 3 expired indices",
		Duration:     974 * time.Millisecond,
	}
	got := normalizeAuditValue(in)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", got)
	}
	if m["deletedUnits"] != float64(3) { // JSON numbers unmarshal to float64
		t.Errorf("deletedUnits = %v (%T), want 3", m["deletedUnits"], m["deletedUnits"])
	}
	if m["message"] != "deleted 3 expired indices" {
		t.Errorf("message = %v, want %q", m["message"], "deleted 3 expired indices")
	}
	if _, hasSignal := m["signal"]; !hasSignal {
		t.Errorf("expected signal key in %v", m)
	}
}

func TestNormalizeAuditValue_EstimateBecomesMap(t *testing.T) {
	in := &PurgeEstimate{
		Signal:        "trace",
		EstimatedDocs: 100,
		AffectedUnits: []string{"otel-traces-a-2026.09.01"},
	}
	got := normalizeAuditValue(in)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", got)
	}
	if m["signal"] != "trace" {
		t.Errorf("signal = %v, want trace", m["signal"])
	}
	units, ok := m["affectedUnits"].([]any)
	if !ok || len(units) != 1 {
		t.Errorf("affectedUnits = %v (%T), want 1-element slice", m["affectedUnits"], m["affectedUnits"])
	}
}

func TestNormalizeAuditValue_Passthrough(t *testing.T) {
	// Maps, slices and scalars must be untouched so existing call sites keep
	// their exact serialized shape.
	m := map[string]any{"appID": "x", "cutoff": time.Now()}
	if got := normalizeAuditValue(m); got == nil {
		t.Fatal("map must pass through unchanged, got nil")
	} else if gm, ok := got.(map[string]any); !ok || gm["appID"] != "x" {
		t.Errorf("map must pass through unchanged, got %v", got)
	}

	s := "some string"
	if got := normalizeAuditValue(s); got != s {
		t.Errorf("scalar must pass through unchanged, got %v", got)
	}

	n := 42
	if got := normalizeAuditValue(n); got != n {
		t.Errorf("int must pass through unchanged, got %v", got)
	}

	if got := normalizeAuditValue(nil); got != nil {
		t.Errorf("nil must pass through unchanged, got %v", got)
	}
}

func TestNormalizeAuditValue_NilPointer(t *testing.T) {
	// A typed nil *PurgeResult must not panic and must return the value as-is
	// (zap renders a nil interface value as null).
	var p *PurgeResult
	got := normalizeAuditValue(p)
	if got != p {
		t.Errorf("typed nil pointer must pass through unchanged, got %v", got)
	}
}
