// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"testing"
	"time"
)

func TestTrendBuffer_BasicPushAndAll(t *testing.T) {
	buf := NewTrendBuffer(5)

	if buf.Len() != 0 {
		t.Errorf("expected Len() = 0, got %d", buf.Len())
	}

	buf.Push(UsageSnapshot{Timestamp: time.Unix(1, 0), UsedBytes: 100})
	buf.Push(UsageSnapshot{Timestamp: time.Unix(2, 0), UsedBytes: 200})
	buf.Push(UsageSnapshot{Timestamp: time.Unix(3, 0), UsedBytes: 300})

	if buf.Len() != 3 {
		t.Errorf("expected Len() = 3, got %d", buf.Len())
	}

	all := buf.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 items, got %d", len(all))
	}

	// Should be in chronological order
	if all[0].UsedBytes != 100 || all[1].UsedBytes != 200 || all[2].UsedBytes != 300 {
		t.Errorf("unexpected order: %v", all)
	}
}

func TestTrendBuffer_WrapAround(t *testing.T) {
	buf := NewTrendBuffer(3) // capacity 3

	// Push 5 items into a size-3 buffer → oldest 2 should be overwritten
	buf.Push(UsageSnapshot{Timestamp: time.Unix(1, 0), UsedBytes: 100}) // [100, _, _]
	buf.Push(UsageSnapshot{Timestamp: time.Unix(2, 0), UsedBytes: 200}) // [100, 200, _]
	buf.Push(UsageSnapshot{Timestamp: time.Unix(3, 0), UsedBytes: 300}) // [100, 200, 300] → full=true
	buf.Push(UsageSnapshot{Timestamp: time.Unix(4, 0), UsedBytes: 400}) // [400, 200, 300] head=1
	buf.Push(UsageSnapshot{Timestamp: time.Unix(5, 0), UsedBytes: 500}) // [400, 500, 300] head=2

	if buf.Len() != 3 {
		t.Errorf("expected Len() = 3 (full), got %d", buf.Len())
	}

	all := buf.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 items, got %d", len(all))
	}

	// Should be in chronological order: 300 (oldest surviving), 400, 500 (newest)
	if all[0].UsedBytes != 300 {
		t.Errorf("expected oldest surviving = 300, got %d", all[0].UsedBytes)
	}
	if all[1].UsedBytes != 400 {
		t.Errorf("expected middle = 400, got %d", all[1].UsedBytes)
	}
	if all[2].UsedBytes != 500 {
		t.Errorf("expected newest = 500, got %d", all[2].UsedBytes)
	}
}

func TestTrendBuffer_ExactCapacityFill(t *testing.T) {
	buf := NewTrendBuffer(3)

	buf.Push(UsageSnapshot{UsedBytes: 1})
	buf.Push(UsageSnapshot{UsedBytes: 2})
	buf.Push(UsageSnapshot{UsedBytes: 3})

	if buf.Len() != 3 {
		t.Errorf("expected Len() = 3, got %d", buf.Len())
	}

	all := buf.All()
	if all[0].UsedBytes != 1 || all[1].UsedBytes != 2 || all[2].UsedBytes != 3 {
		t.Errorf("unexpected: %v", all)
	}
}

func TestTrendBuffer_SingleElement(t *testing.T) {
	buf := NewTrendBuffer(1)

	buf.Push(UsageSnapshot{UsedBytes: 100})
	if buf.Len() != 1 {
		t.Errorf("expected Len() = 1, got %d", buf.Len())
	}

	buf.Push(UsageSnapshot{UsedBytes: 200}) // overwrites
	if buf.Len() != 1 {
		t.Errorf("expected Len() = 1 after overwrite, got %d", buf.Len())
	}

	all := buf.All()
	if len(all) != 1 || all[0].UsedBytes != 200 {
		t.Errorf("expected [200], got %v", all)
	}
}

func TestTrendBuffer_EmptyAll(t *testing.T) {
	buf := NewTrendBuffer(10)

	all := buf.All()
	if len(all) != 0 {
		t.Errorf("expected 0 items on empty buffer, got %d", len(all))
	}
}

func TestTrendBuffer_DefaultSize(t *testing.T) {
	buf := NewTrendBuffer(0) // should default to 168

	// Push doesn't panic
	buf.Push(UsageSnapshot{UsedBytes: 1})
	if buf.Len() != 1 {
		t.Errorf("expected Len() = 1, got %d", buf.Len())
	}
}

func TestTrendBuffer_NegativeSize(t *testing.T) {
	buf := NewTrendBuffer(-5) // should default to 168

	buf.Push(UsageSnapshot{UsedBytes: 1})
	if buf.Len() != 1 {
		t.Errorf("expected Len() = 1, got %d", buf.Len())
	}
}

func TestTrendBuffer_FullWrapMultipleTimes(t *testing.T) {
	buf := NewTrendBuffer(3)

	// Push 9 items (3 full wrap-arounds)
	for i := range 9 {
		buf.Push(UsageSnapshot{UsedBytes: int64(i + 1)})
	}

	if buf.Len() != 3 {
		t.Errorf("expected Len() = 3, got %d", buf.Len())
	}

	all := buf.All()
	// Last 3 items: 7, 8, 9
	if all[0].UsedBytes != 7 || all[1].UsedBytes != 8 || all[2].UsedBytes != 9 {
		t.Errorf("expected [7, 8, 9], got [%d, %d, %d]", all[0].UsedBytes, all[1].UsedBytes, all[2].UsedBytes)
	}
}
