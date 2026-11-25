// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
)

func testInstrumentationConfig() InstrumentationConfig {
	cfg := createDefaultConfig().Instrumentation
	cfg.Enabled = true
	return cfg
}

func TestInstrumentationPolicy_ValidateClass(t *testing.T) {
	tests := []struct {
		name      string
		className string
		mutate    func(*InstrumentationConfig)
		wantErr   string
	}{
		{name: "business class allowed", className: "com.example.OrderService"},
		{name: "empty class", className: "  ", wantErr: "class_name 不能为空"},
		{name: "jdk class denied", className: "java.lang.String", wantErr: "禁用前缀"},
		{name: "otel internal denied", className: "io.opentelemetry.sdk.Span", wantErr: "禁用前缀"},
		{
			name:      "allow list takes precedence",
			className: "com.example.OrderService",
			mutate: func(c *InstrumentationConfig) {
				c.AllowedClassPrefixes = []string{"com.example."}
				c.DeniedClassPrefixes = []string{"com.example."}
			},
		},
		{
			name:      "class outside allow list",
			className: "org.other.Service",
			mutate: func(c *InstrumentationConfig) {
				c.AllowedClassPrefixes = []string{"com.example."}
			},
			wantErr: "白名单",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testInstrumentationConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := newInstrumentationPolicy(cfg).validateClass(tt.className)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestInstrumentationPolicy_ValidateWriteFlags(t *testing.T) {
	cfg := testInstrumentationConfig()
	policy := newInstrumentationPolicy(cfg)

	require.Error(t, policy.validateWriteFlags(true, false, false))
	require.Error(t, policy.validateWriteFlags(false, true, false))
	require.Error(t, policy.validateWriteFlags(false, false, true))
	require.NoError(t, policy.validateWriteFlags(false, false, false))

	cfg.AllowForce = true
	cfg.AllowCapture = true
	require.NoError(t, newInstrumentationPolicy(cfg).validateWriteFlags(true, true, true))
}

func TestInstrumentationPolicy_CountServiceInstances(t *testing.T) {
	ctx := context.Background()
	agents := []*agentregistry.AgentInfo{
		{AgentID: "a1", AppID: "app", ServiceName: "svc"},
		{AgentID: "a2", AppID: "app", ServiceName: "svc"},
		{AgentID: "a3", AppID: "app", ServiceName: "other"},
	}
	policy := newInstrumentationPolicy(testInstrumentationConfig())

	t.Run("prefers shared registry", func(t *testing.T) {
		cp := &controlPlaneWithRegistry{
			stubControlPlane: &stubControlPlane{online: agents},
			instances:        []*agentregistry.AgentInfo{agents[0], agents[1], agents[2]},
		}
		count, err := policy.countServiceInstances(ctx, cp, "app", "svc")
		require.NoError(t, err)
		assert.Equal(t, 3, count)
	})

	t.Run("falls back to online agents", func(t *testing.T) {
		count, err := policy.countServiceInstances(ctx, &stubControlPlane{online: agents}, "app", "svc")
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})

	t.Run("nil control plane", func(t *testing.T) {
		_, err := policy.countServiceInstances(ctx, nil, "app", "svc")
		require.Error(t, err)
	})
}

func TestInstrumentationPolicy_ValidateTargetCount(t *testing.T) {
	cfg := testInstrumentationConfig()
	cfg.MaxTargets = 100
	policy := newInstrumentationPolicy(cfg)

	require.NoError(t, policy.validateTargetCount(0))
	require.NoError(t, policy.validateTargetCount(100))
	require.Error(t, policy.validateTargetCount(101))

	cfg.MaxTargets = 5
	require.Error(t, newInstrumentationPolicy(cfg).validateTargetCount(6))
	assert.Equal(t, 5, newInstrumentationPolicy(cfg).maxTargets())
}

func TestInstrumentationPolicy_CanManage(t *testing.T) {
	cfg := testInstrumentationConfig()
	policy := newInstrumentationPolicy(cfg)

	require.False(t, policy.canManage(nil))
	require.True(t, policy.canManage(&instrumentationmanager.Rule{CreatedBy: "mcp:key-1234"}))
	require.False(t, policy.canManage(&instrumentationmanager.Rule{CreatedBy: "human"}))

	cfg.ManageOnlyOwnRules = false
	require.True(t, newInstrumentationPolicy(cfg).canManage(&instrumentationmanager.Rule{CreatedBy: "human"}))
}
