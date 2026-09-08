// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package hybrid

// VMConfig is the VictoriaMetrics sub-provider configuration for hybrid
// routing. It wraps the concrete provider Config as an opaque value because
// importing the victoriametrics package here would be an import cycle
// (victoriametrics imports the extension package, which imports this
// package). The bridge package (providerregistry) sets Inner via
// SetVMConfigFactory when the collector wires components.
type VMConfig struct {
	// Inner is the *victoriametrics.Config value, passed through as any.
	Inner any `mapstructure:"-"`
}

// SetInner attaches the concrete config value.
func (c *VMConfig) SetInner(v any) { c.Inner = v }
