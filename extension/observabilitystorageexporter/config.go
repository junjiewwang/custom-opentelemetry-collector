// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageexporter

import (
	"fmt"

	"go.opentelemetry.io/collector/component"
)

// Config holds the configuration for the observability storage exporter.
type Config struct {
	// StorageExtension is the component ID of the observability_storage extension
	// that this exporter delegates write operations to.
	StorageExtension component.ID `mapstructure:"storage_extension"`
}

// Validate checks that the configuration is valid.
func (cfg *Config) Validate() error {
	if cfg.StorageExtension.String() == "" {
		return fmt.Errorf("storage_extension must be specified")
	}
	return nil
}
