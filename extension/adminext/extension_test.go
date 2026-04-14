// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
)

func TestExtension_StartInitializesInstrumentationManager(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig().(*Config)
	cfg.HTTP.Endpoint = "127.0.0.1:0"
	cfg.Auth.Enabled = false
	cfg.WSToken.Type = "memory"
	cfg.InstrumentationManager.Type = "memory"

	set := newFactoryTestSettings()
	ext, err := factory.Create(context.Background(), set, cfg)
	require.NoError(t, err)

	adminExt, ok := ext.(*Extension)
	require.True(t, ok)

	err = adminExt.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, adminExt.Shutdown(context.Background()))
	}()

	assert.NotNil(t, adminExt.instrMgr)
}
