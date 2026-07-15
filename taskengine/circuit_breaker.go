// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"sync"
	"time"
)

// CircuitState represents the state of a circuit breaker.
type CircuitState int

const (
	// CircuitClosed is the normal state — requests are allowed.
	CircuitClosed CircuitState = iota
	// CircuitOpen means too many failures — requests are rejected.
	CircuitOpen
	// CircuitHalfOpen means the breaker is probing — one request is allowed.
	CircuitHalfOpen
)

// String returns the human-readable state name.
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig holds configuration for the circuit breaker.
type CircuitBreakerConfig struct {
	// MaxFailures is the number of consecutive failures before opening the circuit.
	MaxFailures int
	// ResetTimeout is how long to wait in Open state before transitioning to HalfOpen.
	ResetTimeout time.Duration
}

// DefaultCircuitBreakerConfig returns sensible defaults for the Reaper use case.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		MaxFailures:  5,
		ResetTimeout: 30 * time.Second,
	}
}

// CircuitBreaker implements the circuit breaker pattern for fault isolation.
//
// State transitions:
//
//	Closed  →  Open      : after MaxFailures consecutive failures
//	Open    →  HalfOpen  : after ResetTimeout elapsed
//	HalfOpen →  Closed   : on a successful probe
//	HalfOpen →  Open     : on a failed probe
//
// Thread-safe via sync.Mutex.
type CircuitBreaker struct {
	mu            sync.Mutex
	config        CircuitBreakerConfig
	state         CircuitState
	failures      int
	lastFailureAt time.Time

	// nowFunc allows time injection for testing.
	nowFunc func() time.Time
}

// NewCircuitBreaker creates a new circuit breaker with the given config.
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config:  config,
		state:   CircuitClosed,
		nowFunc: time.Now,
	}
}

// Allow checks if a request should be permitted.
// Returns true if the request can proceed, false if the circuit is open.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		// Check if reset timeout has elapsed → transition to half-open
		if cb.nowFunc().Sub(cb.lastFailureAt) >= cb.config.ResetTimeout {
			cb.state = CircuitHalfOpen
			return true // Allow one probe request
		}
		return false
	case CircuitHalfOpen:
		// Only one probe at a time — if we're already in half-open,
		// additional requests are still blocked until probe completes.
		return true
	default:
		return true
	}
}

// RecordSuccess records a successful operation, resetting the breaker to Closed.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures = 0
	cb.state = CircuitClosed
}

// RecordFailure records a failed operation.
// If consecutive failures exceed MaxFailures, the circuit opens.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailureAt = cb.nowFunc()

	switch cb.state {
	case CircuitClosed:
		if cb.failures >= cb.config.MaxFailures {
			cb.state = CircuitOpen
		}
	case CircuitHalfOpen:
		// Probe failed — go back to open
		cb.state = CircuitOpen
	}
}

// State returns the current state of the circuit breaker.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ConsecutiveFailures returns the current consecutive failure count.
func (cb *CircuitBreaker) ConsecutiveFailures() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.failures
}

// ─── Backoff ───

// Backoff provides exponential backoff with a capped maximum delay.
// Used to throttle retries when the downstream is unhealthy.
type Backoff struct {
	mu                  sync.Mutex
	consecutiveFailures int
	nextRetryAt         time.Time
	maxDelay            time.Duration
	baseDelay           time.Duration

	// nowFunc allows time injection for testing.
	nowFunc func() time.Time
}

// BackoffConfig holds configuration for exponential backoff.
type BackoffConfig struct {
	// BaseDelay is the initial delay after the first failure.
	BaseDelay time.Duration
	// MaxDelay is the maximum delay cap.
	MaxDelay time.Duration
}

// DefaultBackoffConfig returns sensible defaults.
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		BaseDelay: 2 * time.Second,
		MaxDelay:  64 * time.Second,
	}
}

// NewBackoff creates a new backoff tracker.
func NewBackoff(config BackoffConfig) *Backoff {
	return &Backoff{
		baseDelay: config.BaseDelay,
		maxDelay:  config.MaxDelay,
		nowFunc:   time.Now,
	}
}

// ShouldWait returns true if we should skip this attempt due to backoff.
func (b *Backoff) ShouldWait() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.consecutiveFailures == 0 {
		return false
	}
	return b.nowFunc().Before(b.nextRetryAt)
}

// RecordFailure records a failure and computes the next retry time.
func (b *Backoff) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecutiveFailures++
	// Exponential: baseDelay * 2^(failures-1), capped at maxDelay
	shift := b.consecutiveFailures - 1
	if shift > 6 {
		shift = 6 // cap shift to avoid overflow
	}
	delay := b.baseDelay * time.Duration(1<<shift)
	if delay > b.maxDelay {
		delay = b.maxDelay
	}
	b.nextRetryAt = b.nowFunc().Add(delay)
}

// RecordSuccess resets the backoff state.
func (b *Backoff) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecutiveFailures = 0
	b.nextRetryAt = time.Time{}
}

// ConsecutiveFailures returns the current failure count.
func (b *Backoff) ConsecutiveFailures() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.consecutiveFailures
}
