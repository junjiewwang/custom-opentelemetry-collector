// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

const (
	// TypeStr is the type string for this extension.
	TypeStr = "storage"
)

var (
	// Type is the component type for this extension.
	Type = component.MustNewType(TypeStr)
)

// NewFactory creates a new factory for the storage extension.
func NewFactory() extension.Factory {
	return extension.NewFactory(
		Type,
		func() component.Config {
			return createDefaultConfig()
		},
		createExtension,
		component.StabilityLevelAlpha,
	)
}

// createExtension creates a new storage extension instance.
func createExtension(
	ctx context.Context,
	set extension.Settings,
	cfg component.Config,
) (extension.Extension, error) {
	config := cfg.(*Config)
	return newStorageExtension(ctx, set, config)
}
