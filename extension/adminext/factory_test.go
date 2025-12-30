// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/extension"
)

func newFactoryTestSettings() extension.Settings {
	return extension.Settings{
		ID:                component.NewIDWithName(Type, "test"),
		TelemetrySettings: componenttest.NewNopTelemetrySettings(),
		BuildInfo:         component.NewDefaultBuildInfo(),
	}
}

func TestNewFactory(t *testing.T) {
	factory := NewFactory()

	require.NotNil(t, factory)
	assert.Equal(t, Type, factory.Type())
}

func TestFactory_CreateDefaultConfig(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig()

	require.NotNil(t, cfg)
	adminConfig, ok := cfg.(*Config)
	require.True(t, ok)

	assert.Equal(t, "0.0.0.0:8080", adminConfig.HTTP.Endpoint)
	assert.False(t, adminConfig.Auth.Enabled)
}

func TestFactory_CreateExtension(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig()
	set := newFactoryTestSettings()

	ext, err := factory.Create(context.Background(), set, cfg)
	require.NoError(t, err)
	require.NotNil(t, ext)
}

func TestFactory_Type(t *testing.T) {
	factory := NewFactory()
	assert.Equal(t, Type, factory.Type())
}

func TestFactory_CreateExtension_WithValidConfig(t *testing.T) {
	factory := NewFactory()
	set := newFactoryTestSettings()

	cfg := &Config{
		HTTP: HTTPConfig{
			Endpoint: "localhost:9090",
		},
		Auth: AuthConfig{
			Enabled: true,
			Type:    "api_key",
			APIKey: APIKeyAuthConfig{
				Keys: []string{"test-key"},
			},
		},
	}

	ext, err := factory.Create(context.Background(), set, cfg)
	require.NoError(t, err)
	require.NotNil(t, ext)

	// Cast to Extension to verify internal state
	adminExt, ok := ext.(*Extension)
	require.True(t, ok)
	assert.Equal(t, cfg, adminExt.config)
}
