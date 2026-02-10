// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"fmt"

	"go.uber.org/zap"
)

// NewBlobStore creates a BlobStore based on the given configuration.
func NewBlobStore(logger *zap.Logger, cfg Config) (BlobStore, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	cfg.ApplyDefaults()

	switch cfg.Type {
	case "local":
		return NewLocalBlobStore(logger, cfg)
	case "cos":
		return NewCOSBlobStore(logger, cfg)
	case "noop", "":
		return NewNoopBlobStore(), nil
	default:
		return nil, fmt.Errorf("unsupported blob store type: %s", cfg.Type)
	}
}
