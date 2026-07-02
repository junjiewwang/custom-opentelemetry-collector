// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"sync"
	"time"
)

// Compile-time check: TrendBuffer implements UsageHistoryReader.
var _ UsageHistoryReader = (*TrendBuffer)(nil)

// TrendBuffer is a thread-safe, fixed-size ring buffer for UsageSnapshot.
// It retains the N most recent snapshots and overwrites the oldest when full.
type TrendBuffer struct {
	mu   sync.RWMutex
	buf  []UsageSnapshot
	size int
	head int // next write position
	full bool
}

// NewTrendBuffer creates a new ring buffer with the given capacity.
func NewTrendBuffer(size int) *TrendBuffer {
	if size <= 0 {
		size = 168 // default: 7 days @ 1h
	}
	return &TrendBuffer{
		buf:  make([]UsageSnapshot, size),
		size: size,
	}
}

// Push adds a snapshot to the buffer, overwriting the oldest if full.
func (b *TrendBuffer) Push(s UsageSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf[b.head] = s
	b.head = (b.head + 1) % b.size
	if b.head == 0 && !b.full {
		b.full = true
	}
}

// All returns all snapshots in chronological order (oldest first).
func (b *TrendBuffer) All() []UsageSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.full {
		// Buffer not yet wrapped — return [0..head)
		result := make([]UsageSnapshot, b.head)
		copy(result, b.buf[:b.head])
		return result
	}

	// Buffer has wrapped — return [head..end] + [0..head)
	result := make([]UsageSnapshot, b.size)
	copy(result, b.buf[b.head:])
	copy(result[b.size-b.head:], b.buf[:b.head])
	return result
}

// ReadSnapshots returns all snapshots within [since, until] in chronological order.
// Implements UsageHistoryReader.
func (b *TrendBuffer) ReadSnapshots(since, until time.Time) []UsageSnapshot {
	all := b.All()
	if len(all) == 0 {
		return nil
	}
	// Binary-search for start index (snapshots are sorted by timestamp)
	startIdx := 0
	for startIdx < len(all) && all[startIdx].Timestamp.Before(since) {
		startIdx++
	}
	// Collect up to until
	endIdx := startIdx
	for endIdx < len(all) && !all[endIdx].Timestamp.After(until) {
		endIdx++
	}
	if startIdx >= endIdx {
		return nil
	}
	return all[startIdx:endIdx]
}

// Len returns the number of snapshots currently stored.
func (b *TrendBuffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.full {
		return b.size
	}
	return b.head
}
