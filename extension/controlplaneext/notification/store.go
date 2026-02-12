// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package notification

import "context"

// Store defines the persistence interface for notification records.
// Implementations must be safe for concurrent use.
type Store interface {
	// Save creates or updates a notification record.
	Save(ctx context.Context, record *Record) error

	// Get retrieves a record by ID.
	Get(ctx context.Context, id string) (*Record, error)

	// GetByTaskID retrieves a record by task ID.
	GetByTaskID(ctx context.Context, taskID string) (*Record, error)

	// ListByStatus returns records matching the given status, ordered by creation time.
	ListByStatus(ctx context.Context, status Status, limit int) ([]*Record, error)

	// Update modifies an existing record.
	Update(ctx context.Context, record *Record) error
}
