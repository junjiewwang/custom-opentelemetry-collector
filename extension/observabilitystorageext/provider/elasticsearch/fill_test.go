// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"math"
	"testing"
	"time"
)

func makeDPs(vals ...float64) []MetricDataPoint {
	baseTime := time.UnixMilli(1000)
	dps := make([]MetricDataPoint, len(vals))
	for i, v := range vals {
		dps[i] = MetricDataPoint{
			Time:  baseTime.Add(time.Duration(i) * time.Second),
			Value: v,
		}
	}
	return dps
}

func TestFillNull(t *testing.T) {
	dps := makeDPs(1.0, NilValue, 3.0)
	result := fillNull(dps)
	if len(result) != 3 {
		t.Fatalf("expected len 3, got %d", len(result))
	}
	if !math.IsNaN(result[1].Value) {
		t.Fatalf("null fill should preserve NaN sentinel, got %v", result[1].Value)
	}
}

func TestFillNone(t *testing.T) {
	dps := makeDPs(1.0, NilValue, 3.0)
	result := fillNone(dps)
	if len(result) != 2 {
		t.Fatalf("expected len 2 (skip NaN), got %d", len(result))
	}
	if result[0].Value != 1.0 {
		t.Fatalf("pos 0: expected 1.0, got %v", result[0].Value)
	}
	if result[1].Value != 3.0 {
		t.Fatalf("pos 1: expected 3.0, got %v", result[1].Value)
	}
}

func TestFillZero(t *testing.T) {
	dps := makeDPs(1.0, NilValue, 3.0)
	result := fillZero(dps)
	if math.IsNaN(result[1].Value) {
		t.Fatalf("fill=0 should replace NaN with 0, got NaN")
	}
	if result[1].Value != 0 {
		t.Fatalf("pos 1: expected 0, got %v", result[1].Value)
	}
}

func TestFillPrevious(t *testing.T) {
	dps := makeDPs(1.0, NilValue, NilValue, 4.0, NilValue, NilValue)
	result := fillPrevious(dps)

	// [1.0, 1.0, 1.0, 4.0, 4.0, 4.0]
	if result[1].Value != 1.0 {
		t.Fatalf("pos 1: expected 1.0, got %v", result[1].Value)
	}
	if result[2].Value != 1.0 {
		t.Fatalf("pos 2: expected 1.0, got %v", result[2].Value)
	}
	if result[4].Value != 4.0 {
		t.Fatalf("pos 4: expected 4.0, got %v", result[4].Value)
	}
	if result[5].Value != 4.0 {
		t.Fatalf("pos 5: expected 4.0, got %v", result[5].Value)
	}
}

func TestFillPrevious_FirstNaN(t *testing.T) {
	dps := makeDPs(NilValue, NilValue, 3.0, NilValue, 5.0)
	result := fillPrevious(dps)

	// First two stay NaN (no previous valid), then: 3.0, 3.0, 5.0
	if !math.IsNaN(result[0].Value) {
		t.Fatalf("pos 0: expected NaN, got %v", result[0].Value)
	}
	if !math.IsNaN(result[1].Value) {
		t.Fatalf("pos 1: expected NaN, got %v", result[1].Value)
	}
	if result[3].Value != 3.0 {
		t.Fatalf("pos 3: expected 3.0, got %v", result[3].Value)
	}
}

func TestFillLinear(t *testing.T) {
	dps := makeDPs(0.0, NilValue, 6.0, NilValue, 9.0)
	result := fillLinear(dps)

	// pos 0→2: 0→6, step=3 → pos 1=3
	if result[1].Value != 3.0 {
		t.Fatalf("pos 1: expected 3.0, got %v", result[1].Value)
	}
	// pos 2→4: 6→9, step=1.5 → pos 3=7.5
	if result[3].Value != 7.5 {
		t.Fatalf("pos 3: expected 7.5, got %v", result[3].Value)
	}
}

func TestFillLinear_GapBefore(t *testing.T) {
	dps := makeDPs(NilValue, NilValue, 3.0, 6.0)
	result := fillLinear(dps)
	if !math.IsNaN(result[0].Value) {
		t.Fatalf("pos 0: expected NaN (gap before), got %v", result[0].Value)
	}
	if !math.IsNaN(result[1].Value) {
		t.Fatalf("pos 1: expected NaN (gap before), got %v", result[1].Value)
	}
}

func TestFillLinear_GapAfter(t *testing.T) {
	dps := makeDPs(3.0, 6.0, NilValue, NilValue)
	result := fillLinear(dps)
	if !math.IsNaN(result[2].Value) {
		t.Fatalf("pos 2: expected NaN (gap after), got %v", result[2].Value)
	}
	if !math.IsNaN(result[3].Value) {
		t.Fatalf("pos 3: expected NaN (gap after), got %v", result[3].Value)
	}
}

func TestFillLinear_Empty(t *testing.T) {
	result := fillLinear(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestValidFillStrategies(t *testing.T) {
	names := ValidFillStrategies()
	if len(names) != 5 {
		t.Fatalf("expected 5 fill strategies, got %d", len(names))
	}
}

func TestGetFillStrategy_Default(t *testing.T) {
	fn := GetFillStrategy("")
	dps := makeDPs(1.0, 2.0)
	result := fn(dps)
	if result[0].Value != 1.0 || result[1].Value != 2.0 {
		t.Fatalf("default fill should be identity")
	}
}

func TestGetFillStrategy_Unknown(t *testing.T) {
	fn := GetFillStrategy("unknown")
	dps := makeDPs(1.0, 2.0)
	result := fn(dps)
	if result[0].Value != 1.0 || result[1].Value != 2.0 {
		t.Fatalf("unknown fill should fall back to null")
	}
}
