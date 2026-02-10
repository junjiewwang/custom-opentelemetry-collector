// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import "errors"

var (
	// ErrNotFound is returned when a blob does not exist.
	ErrNotFound = errors.New("blob not found")

	// ErrTooLarge is returned when the blob exceeds the maximum allowed size.
	ErrTooLarge = errors.New("blob exceeds maximum size")
)
