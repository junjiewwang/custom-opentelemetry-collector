// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── CircuitState.String ────────────────────────────────────────────────

func TestCircuitState_String(t *testing.T) {
	assert.Equal(t, "closed", CircuitClosed.String())
	assert.Equal(t, "open", CircuitOpen.String())
	assert.Equal(t, "half-open", CircuitHalfOpen.String())
	// Any other value maps to "unknown".
	assert.Equal(t, "unknown", CircuitState(99).String())
}

// ── Defaults / constructors ────────────────────────────────────────────

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()
	assert.Equal(t, 5, cfg.MaxFailures)
	assert.Equal(t, 30*time.Second, cfg.ResetTimeout)
}

func TestDefaultBackoffConfig(t *testing.T) {
	cfg := DefaultBackoffConfig()
	assert.Equal(t, 2*time.Second, cfg.BaseDelay)
	assert.Equal(t, 64*time.Second, cfg.MaxDelay)
}

func TestNewCircuitBreaker_StartsClosed(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())
	assert.Equal(t, CircuitClosed, cb.State())
	assert.Equal(t, 0, cb.ConsecutiveFailures())
	assert.True(t, cb.Allow(), "fresh breaker must allow")
}

func TestNewBackoff_StartsIdle(t *testing.T) {
	b := NewBackoff(DefaultBackoffConfig())
	assert.Equal(t, 0, b.ConsecutiveFailures())
	assert.False(t, b.ShouldWait(), "fresh backoff must not wait")
}

// ── CircuitBreaker state transitions ───────────────────────────────────
//
// The breaker exposes nowFunc for time injection, so all ResetTimeout-based
// transitions are tested deterministically without sleeping.

// newBreakerWithClock builds a breaker whose clock is the returned mutable
// fake, so tests can advance time and observe Open→HalfOpen.
func newBreakerWithClock(t *testing.T, maxFailures int, resetTimeout time.Duration) (*CircuitBreaker, *fakeClock) {
	t.Helper()
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: maxFailures, ResetTimeout: resetTimeout})
	cb.nowFunc = clock.nowTime
	return cb, clock
}

func TestCircuitBreaker_OpenAfterMaxFailures(t *testing.T) {
	cb, _ := newBreakerWithClock(t, 3, time.Second)

	// Failures below the threshold keep the circuit closed.
	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, CircuitClosed, cb.State())
	assert.True(t, cb.Allow())

	// The MaxFailures-th failure trips the breaker open.
	cb.RecordFailure()
	assert.Equal(t, CircuitOpen, cb.State())
	assert.Equal(t, 3, cb.ConsecutiveFailures())
	assert.False(t, cb.Allow(), "open circuit must reject")
}

func TestCircuitBreaker_RecordSuccessResetsToClosed(t *testing.T) {
	cb, _ := newBreakerWithClock(t, 2, time.Second)
	cb.RecordFailure()
	cb.RecordFailure()
	require.Equal(t, CircuitOpen, cb.State())

	cb.RecordSuccess()
	assert.Equal(t, CircuitClosed, cb.State())
	assert.Equal(t, 0, cb.ConsecutiveFailures())
	assert.True(t, cb.Allow())
}

func TestCircuitBreaker_OpenToHalfOpenAfterResetTimeout(t *testing.T) {
	cb, clock := newBreakerWithClock(t, 1, 5*time.Second)

	cb.RecordFailure() // trips open
	require.Equal(t, CircuitOpen, cb.State())
	require.False(t, cb.Allow())

	// Just before the reset timeout: still open, still rejected.
	clock.advance(5*time.Second - time.Nanosecond)
	assert.False(t, cb.Allow())
	require.Equal(t, CircuitOpen, cb.State())

	// At/after the reset timeout: Allow transitions to HalfOpen and lets a probe through.
	clock.advance(time.Nanosecond)
	assert.True(t, cb.Allow(), "probe must be allowed once reset timeout elapses")
	assert.Equal(t, CircuitHalfOpen, cb.State())
}

func TestCircuitBreaker_HalfOpenSuccessCloses(t *testing.T) {
	cb, clock := newBreakerWithClock(t, 1, time.Second)
	cb.RecordFailure()
	clock.advance(2 * time.Second)
	require.True(t, cb.Allow()) // → HalfOpen
	require.Equal(t, CircuitHalfOpen, cb.State())

	// A successful probe closes the circuit.
	cb.RecordSuccess()
	assert.Equal(t, CircuitClosed, cb.State())
	assert.True(t, cb.Allow())
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	cb, clock := newBreakerWithClock(t, 1, time.Second)
	cb.RecordFailure()
	clock.advance(2 * time.Second)
	require.True(t, cb.Allow()) // → HalfOpen

	// A failed probe sends the breaker back to Open.
	cb.RecordFailure()
	assert.Equal(t, CircuitOpen, cb.State())
	assert.False(t, cb.Allow())
}

func TestCircuitBreaker_FailuresResetOnSuccess(t *testing.T) {
	cb, _ := newBreakerWithClock(t, 3, time.Second)
	cb.RecordFailure()
	cb.RecordFailure()
	// A success clears the failure streak so the next two failures don't trip.
	cb.RecordSuccess()
	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, CircuitClosed, cb.State())
	assert.True(t, cb.Allow())
}

func TestCircuitBreaker_StateStringWhileOpen(t *testing.T) {
	// Guards the zap.String("state", cb.State().String()) call site in reaper_engine.
	cb, _ := newBreakerWithClock(t, 1, time.Second)
	cb.RecordFailure()
	assert.Equal(t, "open", cb.State().String())
}

// ── Backoff ────────────────────────────────────────────────────────────

// newBackoffWithClock builds a backoff whose clock is the returned mutable fake.
func newBackoffWithClock(t *testing.T, base, max time.Duration) (*Backoff, *fakeClock) {
	t.Helper()
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	b := NewBackoff(BackoffConfig{BaseDelay: base, MaxDelay: max})
	b.nowFunc = clock.nowTime
	return b, clock
}

func TestBackoff_NoWaitBeforeAnyFailure(t *testing.T) {
	b, _ := newBackoffWithClock(t, time.Second, 64*time.Second)
	assert.False(t, b.ShouldWait())
}

func TestBackoff_ExponentialDelays(t *testing.T) {
	b, clock := newBackoffWithClock(t, time.Second, 64*time.Second)

	// Failure 1 → wait baseDelay (1s); before that window: wait; after: don't.
	b.RecordFailure()
	assert.Equal(t, 1, b.ConsecutiveFailures())
	clock.advance(999 * time.Millisecond)
	assert.True(t, b.ShouldWait(), "must wait within the backoff window")
	clock.advance(time.Millisecond)
	assert.False(t, b.ShouldWait(), "must not wait once the window elapses")

	// Failure 2 → wait baseDelay*2 (2s) from the new now.
	clock.advance(time.Second) // move to just past the first window
	b.RecordFailure()
	clock.advance(2*time.Second - time.Millisecond)
	assert.True(t, b.ShouldWait())
	clock.advance(time.Millisecond)
	assert.False(t, b.ShouldWait())

	// Failure 3 → 4s; Failure 4 → 8s.
	b.RecordFailure()
	clock.advance(4*time.Second - time.Millisecond)
	assert.True(t, b.ShouldWait())
	clock.advance(time.Millisecond)
	assert.False(t, b.ShouldWait())
}

func TestBackoff_CappedAtMaxDelay(t *testing.T) {
	// base=4s, max=8s → failure 1: 4s, failure 2: 8s (capped), failure 3: 8s (capped).
	b, clock := newBackoffWithClock(t, 4*time.Second, 8*time.Second)

	b.RecordFailure()
	clock.advance(4*time.Second - time.Millisecond)
	assert.True(t, b.ShouldWait())
	clock.advance(time.Millisecond)
	assert.False(t, b.ShouldWait())

	b.RecordFailure()
	clock.advance(8*time.Second - time.Millisecond)
	assert.True(t, b.ShouldWait(), "failure 2 must wait the full capped 8s")
	clock.advance(time.Millisecond)
	assert.False(t, b.ShouldWait())

	b.RecordFailure()
	clock.advance(8*time.Second - time.Millisecond)
	assert.True(t, b.ShouldWait(), "failure 3 stays capped at 8s, not 16s")
}

func TestBackoff_ShiftCappedAtSixtyFourX(t *testing.T) {
	// With a large max, verify the shift cap (1<<6 = 64x) kicks in beyond failure 7,
	// i.e. failure 7 and failure 8 produce the same delay (no further doubling).
	b, clock := newBackoffWithClock(t, time.Second, 10*time.Minute)

	// Drive to failure 7: delay = 1s * 2^6 = 64s.
	for i := 0; i < 7; i++ {
		b.RecordFailure()
		clock.advance(64 * time.Second) // past each window so ShouldWait flips false
	}
	require.Equal(t, 7, b.ConsecutiveFailures())

	// Failure 8 would be 1s * 2^7 = 128s without the cap, but the cap keeps shift at 6 → 64s.
	b.RecordFailure()
	clock.advance(64*time.Second - time.Millisecond)
	assert.True(t, b.ShouldWait(), "failure 8 must wait 64s (capped shift), not 128s")
	clock.advance(time.Millisecond)
	assert.False(t, b.ShouldWait())
}

func TestBackoff_RecordSuccessResets(t *testing.T) {
	b, clock := newBackoffWithClock(t, time.Second, 64*time.Second)
	b.RecordFailure()
	b.RecordFailure()
	clock.advance(time.Millisecond) // still within window
	require.True(t, b.ShouldWait())

	b.RecordSuccess()
	assert.Equal(t, 0, b.ConsecutiveFailures())
	assert.False(t, b.ShouldWait(), "success clears the backoff window immediately")
}

func TestBackoff_ConsecutiveFailuresCount(t *testing.T) {
	b, _ := newBackoffWithClock(t, time.Second, 64*time.Second)
	assert.Equal(t, 0, b.ConsecutiveFailures())
	b.RecordFailure()
	b.RecordFailure()
	b.RecordFailure()
	assert.Equal(t, 3, b.ConsecutiveFailures())
	b.RecordSuccess()
	assert.Equal(t, 0, b.ConsecutiveFailures())
}

// ── fakeClock ──────────────────────────────────────────────────────────
//
// A controllable clock so breaker/backoff time-based transitions are tested
// without time.Sleep. Not safe for concurrent use by multiple goroutines, but
// the breaker/backoff themselves are — tests here are single-goroutine.

type fakeClock struct {
	now time.Time
}

func newFakeClock(at time.Time) *fakeClock    { return &fakeClock{now: at} }
func (c *fakeClock) nowTime() time.Time       { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }
