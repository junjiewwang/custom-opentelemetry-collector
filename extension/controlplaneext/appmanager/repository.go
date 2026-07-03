// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("app not found")

// AppRepository is a narrow, storage-agnostic persistence abstraction for AppInfo.
// It contains NO business rules (no dup-check, no token-conflict logic) —
// those belong to AppService. This keeps Repository implementations trivial
// and interchangeable (OCP).
//
// Each method operates on a single entity; the caller is responsible for
// composing operations into transactions when needed (e.g., token swap).
type AppRepository interface {
	// Insert stores a new AppInfo. Returns an error if an app with the same ID
	// or token already exists (implementation-specific constraint).
	Insert(ctx context.Context, app *AppInfo) error

	// FindByID returns the AppInfo for the given ID, or ErrNotFound.
	FindByID(ctx context.Context, id string) (*AppInfo, error)

	// FindByToken returns the AppInfo associated with the given token, or ErrNotFound.
	FindByToken(ctx context.Context, token string) (*AppInfo, error)

	// Save fully overwrites the stored AppInfo for the given ID.
	// Returns ErrNotFound if no app exists with that ID.
	Save(ctx context.Context, app *AppInfo) error

	// Delete removes the app and its token mapping. Returns ErrNotFound if not found.
	Delete(ctx context.Context, id string) error

	// List returns all stored AppInfo entries. Returns an empty slice if none exist.
	List(ctx context.Context) ([]*AppInfo, error)
}
