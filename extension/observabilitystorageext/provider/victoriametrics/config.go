// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"errors"
	"fmt"
	"time"
)

// Config holds the VictoriaMetrics provider configuration.
type Config struct {
	// Endpoint is the vmsingle base URL, e.g. "http://vmsingle:8428".
	// For vmcluster, set WriteEndpoint/ReadEndpoint instead (vminsert/vmselect).
	Endpoint      string `mapstructure:"endpoint"`
	WriteEndpoint string `mapstructure:"write_endpoint"` // overrides Endpoint for writes (vminsert)
	ReadEndpoint  string `mapstructure:"read_endpoint"`  // overrides Endpoint for reads (vmselect)

	// BatchSize is the max number of JSON lines per import POST. The VM line
	// format carries a whole series (values+timestamps arrays) per line, so
	// this bounds lines, not samples.
	BatchSize int `mapstructure:"batch_size"`

	// FlushInterval is the max time between import POSTs.
	FlushInterval time.Duration `mapstructure:"flush_interval"`

	// WriteTimeout/ReadTimeout bound individual HTTP requests.
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`

	// MaxRetries is the number of retry attempts for failed writes.
	// Retries only apply to 5xx/timeout; 4xx is a data-format bug and fails fast.
	MaxRetries int `mapstructure:"max_retries"`

	// ExtraLabels are injected into every written series (e.g. cluster="prod").
	ExtraLabels map[string]string `mapstructure:"extra_labels"`

	// AccountScope enables VictoriaMetrics native multitenancy: read/write URLs
	// become /select/<accountID>/prometheus/... and /insert/<accountID>/prometheus/...
	// (requires WriteEndpoint/ReadEndpoint pointing at vminsert/vmselect). When
	// false (default), the provider uses the vmsingle single-tenant endpoints and
	// the label-based tenant_id soft isolation.
	AccountScope bool `mapstructure:"account_scope"`
}

// ApplyDefaults fills zero-valued fields with sensible defaults.
func (c *Config) ApplyDefaults() {
	if c.BatchSize == 0 {
		c.BatchSize = 5000
	}
	if c.FlushInterval == 0 {
		c.FlushInterval = 3 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 10 * time.Second
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = 30 * time.Second
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 3
	}
}

// Validate reports configuration errors before the provider starts.
func (c *Config) Validate() error {
	if c.Endpoint == "" && c.WriteEndpoint == "" {
		return errors.New("victoriametrics: endpoint (or write_endpoint) is required")
	}
	if c.WriteEndpoint != "" && c.ReadEndpoint == "" && c.Endpoint == "" {
		return errors.New("victoriametrics: read_endpoint or endpoint is required when write_endpoint is set")
	}
	if c.BatchSize < 0 || c.MaxRetries < 0 {
		return fmt.Errorf("victoriametrics: batch_size and max_retries must be >= 0, got %d/%d", c.BatchSize, c.MaxRetries)
	}
	return nil
}

// writeBase returns the effective write endpoint.
func (c *Config) writeBase() string {
	if c.WriteEndpoint != "" {
		return c.WriteEndpoint
	}
	return c.Endpoint
}

// readBase returns the effective read endpoint.
func (c *Config) readBase() string {
	if c.ReadEndpoint != "" {
		return c.ReadEndpoint
	}
	return c.Endpoint
}
