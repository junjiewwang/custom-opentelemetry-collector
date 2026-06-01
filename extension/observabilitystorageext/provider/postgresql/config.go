// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import "time"

// Config holds the PostgreSQL provider configuration.
// This is a package-local type to avoid circular imports with the parent package.
type Config struct {
	// DSN is the PostgreSQL connection string.
	// Format: postgres://user:password@host:port/dbname?sslmode=disable
	DSN string

	// MaxConns is the maximum number of connections in the pool.
	MaxConns int32

	// MinConns is the minimum number of idle connections in the pool.
	MinConns int32

	// MaxConnLifetime is the maximum amount of time a connection may be reused.
	MaxConnLifetime time.Duration

	// MaxConnIdleTime is the maximum amount of time a connection may be idle.
	MaxConnIdleTime time.Duration

	// BatchSize is the number of rows per COPY batch.
	BatchSize int

	// FlushInterval is the max time between batch flushes.
	FlushInterval time.Duration

	// MaxRetries is the number of retry attempts for failed operations.
	MaxRetries int

	// Traces holds trace table configuration.
	Traces TableConfig

	// Metrics holds metric table configuration.
	Metrics TableConfig

	// Logs holds log table configuration.
	Logs TableConfig

	// UseTimescaleDB enables TimescaleDB hypertable features for metrics.
	UseTimescaleDB bool
}

// TableConfig holds configuration for a single signal's table.
type TableConfig struct {
	// TableName is the base table name (e.g., "otel_traces").
	TableName string

	// Retention is the data retention duration for this signal.
	Retention time.Duration

	// PartitionInterval is the interval for time-based partitioning.
	// For standard PG: native range partitions. For TimescaleDB: chunk interval.
	PartitionInterval time.Duration
}
