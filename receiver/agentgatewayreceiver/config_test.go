// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/config/confighttp"
)

// ═══════════════════════════════════════════════════════════════════════
// config.go — pure getters + Validate
// ═══════════════════════════════════════════════════════════════════════

func TestConfig_GetPaths_DefaultsWhenUnset(t *testing.T) {
	cfg := &Config{}
	assert.Equal(t, "/v1/traces", cfg.GetTracesPath())
	assert.Equal(t, "/v1/metrics", cfg.GetMetricsPath())
	assert.Equal(t, "/v1/logs", cfg.GetLogsPath())
	assert.Equal(t, "/v1/control", cfg.GetControlPlanePathPrefix())
	assert.Equal(t, "/v1/arthas/ws", cfg.GetArthasTunnelPath())
}

func TestConfig_GetPaths_CustomWhenSet(t *testing.T) {
	cfg := &Config{
		OTLP:         OTLPConfig{TracesPath: "/t", MetricsPath: "/m", LogsPath: "/l"},
		ControlPlane: ControlPlaneConfig{PathPrefix: "/cp"},
		ArthasTunnel: ArthasTunnelConfig{Path: "/ws"},
	}
	assert.Equal(t, "/t", cfg.GetTracesPath())
	assert.Equal(t, "/m", cfg.GetMetricsPath())
	assert.Equal(t, "/l", cfg.GetLogsPath())
	assert.Equal(t, "/cp", cfg.GetControlPlanePathPrefix())
	assert.Equal(t, "/ws", cfg.GetArthasTunnelPath())
}

func TestConfig_GetTokenAuthHeader_DefaultsAndCustom(t *testing.T) {
	cfg := &Config{}
	assert.Equal(t, "Authorization", cfg.GetTokenAuthHeaderName())
	assert.Equal(t, "Bearer ", cfg.GetTokenAuthHeaderPrefix())

	cfg.TokenAuth = TokenAuthConfig{HeaderName: "X-Token", HeaderPrefix: "Token "}
	assert.Equal(t, "X-Token", cfg.GetTokenAuthHeaderName())
	assert.Equal(t, "Token ", cfg.GetTokenAuthHeaderPrefix())
}

func TestConfig_IsPathSkipped(t *testing.T) {
	cfg := &Config{TokenAuth: TokenAuthConfig{SkipPaths: []string{"/health", "/ready"}}}
	assert.True(t, cfg.IsPathSkipped("/health"))
	assert.True(t, cfg.IsPathSkipped("/ready"))
	assert.False(t, cfg.IsPathSkipped("/v1/traces"))
	assert.False(t, cfg.IsPathSkipped(""))
}

func TestConfig_Validate_RequiresHTTP(t *testing.T) {
	// No HTTP → error
	err := (&Config{}).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http configuration is required")
}

func TestConfig_Validate_ControlPlaneRequiresHTTP(t *testing.T) {
	// HTTP set but ControlPlane.Enabled without... HTTP is set, so this passes
	// only when ControlPlane/ArthasTunnel don't require extra. Actually the
	// guards check HTTP==nil; with HTTP set they pass. Verify the happy path.
	cfg := &Config{
		HTTP:         &confighttp.ServerConfig{},
		ControlPlane: ControlPlaneConfig{Enabled: true},
		ArthasTunnel: ArthasTunnelConfig{Enabled: true},
	}
	assert.NoError(t, cfg.Validate())

	// ControlPlane enabled but HTTP nil → error (HTTP==nil short-circuits first,
	// so this is covered by the top-level check, but assert explicitly).
	err := (&Config{ControlPlane: ControlPlaneConfig{Enabled: true}}).Validate()
	assert.Error(t, err)
}

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.HTTP, "default config must set HTTP")
	require.NotNil(t, cfg.GRPC, "default config must set GRPC")
	assert.NotEmpty(t, cfg.HTTP.Endpoint)
	// Defaults provide known paths.
	assert.Equal(t, "/v1/traces", cfg.GetTracesPath())
}
