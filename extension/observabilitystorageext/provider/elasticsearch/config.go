// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import "time"

// Config holds the Elasticsearch provider configuration.
// This is a package-local type to avoid circular imports with the parent package.
type Config struct {
	// Addresses is the list of ES node URLs.
	Addresses []string

	// Username for basic auth (optional).
	Username string

	// Password for basic auth (optional).
	Password string

	// BatchSize is the number of documents per bulk request.
	BatchSize int

	// FlushInterval is the max time between bulk flushes.
	FlushInterval time.Duration

	// MaxRetries is the number of retry attempts for failed requests.
	MaxRetries int

	// Traces holds trace index configuration.
	Traces IndexConfig

	// Metrics holds metric index configuration.
	Metrics IndexConfig

	// Logs holds log index configuration.
	Logs IndexConfig
}

// IndexConfig holds configuration for a single signal's index.
type IndexConfig struct {
	// IndexPrefix is the prefix for index names (e.g., "otel-traces").
	IndexPrefix string

	// IndexDateFormat is the Go time format for date-based index rotation.
	IndexDateFormat string

	// Shards is the number of primary shards.
	Shards int

	// Replicas is the number of replica shards.
	Replicas int

	// Retention is the data retention duration for this signal.
	Retention time.Duration

	// RefreshInterval is the ES refresh interval for the index.
	RefreshInterval string
}
