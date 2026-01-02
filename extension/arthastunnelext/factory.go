// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

const (
	typeStr   = "arthas_tunnel"
	stability = component.StabilityLevelDevelopment
)

// Type is the component type for the Arthas tunnel extension.
var Type = component.MustNewType(typeStr)

// NewFactory creates a new factory for the Arthas tunnel extension.
func NewFactory() extension.Factory {
	return extension.NewFactory(
		Type,
		func() component.Config { return createDefaultConfig() },
		createExtension,
		stability,
	)
}

func createExtension(
	_ context.Context,
	set extension.Settings,
	cfg component.Config,
) (extension.Extension, error) {
	return newExtension(set, cfg.(*Config))
}
