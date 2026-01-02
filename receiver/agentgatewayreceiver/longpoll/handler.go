// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
)

// LongPollHandler defines the interface for long poll handlers.
// This follows the Open-Closed Principle - new poll types can be added
// by implementing this interface without modifying existing code.
type LongPollHandler interface {
	// GetType returns the handler type.
	GetType() LongPollType

	// Poll executes the long poll wait (blocks until change or timeout).
	// ctx: context with timeout
	// req: poll request containing version/etag info
	Poll(ctx context.Context, req *PollRequest) (*HandlerResult, error)

	// CheckImmediate checks if there are changes immediately (non-blocking).
	// Returns true if there are changes and should return immediately.
	CheckImmediate(ctx context.Context, req *PollRequest) (hasChanges bool, result *HandlerResult, err error)

	// ShouldContinue returns whether the handler should continue polling.
	ShouldContinue() bool

	// Start initializes the handler.
	Start(ctx context.Context) error

	// Stop stops the handler and releases resources.
	Stop() error
}
