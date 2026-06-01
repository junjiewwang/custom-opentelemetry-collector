// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Client wraps a pgxpool connection pool and provides basic database operations.
type Client struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
}

// NewClient creates a new PostgreSQL client with a connection pool.
func NewClient(config *Config, logger *zap.Logger) (*Client, error) {
	poolCfg, err := pgxpool.ParseConfig(config.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to parse DSN: %w", err)
	}

	// Apply pool configuration
	if config.MaxConns > 0 {
		poolCfg.MaxConns = config.MaxConns
	}
	if config.MinConns > 0 {
		poolCfg.MinConns = config.MinConns
	}
	if config.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = config.MaxConnLifetime
	}
	if config.MaxConnIdleTime > 0 {
		poolCfg.MaxConnIdleTime = config.MaxConnIdleTime
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	return &Client{
		pool:   pool,
		logger: logger.Named("pg-client"),
	}, nil
}

// Ping verifies the database connection is alive.
func (c *Client) Ping(ctx context.Context) error {
	return c.pool.Ping(ctx)
}

// Pool returns the underlying connection pool for advanced operations.
func (c *Client) Pool() *pgxpool.Pool {
	return c.pool
}

// Exec executes a SQL statement and returns the number of rows affected.
func (c *Client) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	ct, err := c.pool.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// Query executes a query and returns rows.
func (c *Client) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return c.pool.Query(ctx, sql, args...)
}

// QueryRow executes a query that returns at most one row.
func (c *Client) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return c.pool.QueryRow(ctx, sql, args...)
}

// Close closes all connections in the pool.
func (c *Client) Close() {
	c.pool.Close()
}

// GetVersion returns the PostgreSQL server version string.
func (c *Client) GetVersion(ctx context.Context) (string, error) {
	var version string
	err := c.pool.QueryRow(ctx, "SELECT version()").Scan(&version)
	return version, err
}

// HasTimescaleDB checks if the TimescaleDB extension is available.
func (c *Client) HasTimescaleDB(ctx context.Context) (bool, error) {
	var exists bool
	err := c.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'timescaledb')",
	).Scan(&exists)
	return exists, err
}

// DatabaseSize returns the total size of the current database in bytes.
func (c *Client) DatabaseSize(ctx context.Context) (int64, error) {
	var size int64
	err := c.pool.QueryRow(ctx,
		"SELECT pg_database_size(current_database())",
	).Scan(&size)
	return size, err
}

// TableSize returns the total size (data + indexes) of a table in bytes.
func (c *Client) TableSize(ctx context.Context, tableName string) (int64, error) {
	var size int64
	err := c.pool.QueryRow(ctx,
		"SELECT pg_total_relation_size($1::regclass)",
		tableName,
	).Scan(&size)
	if err != nil {
		// Table might not exist yet
		return 0, nil
	}
	return size, nil
}

// TableRowCount returns the approximate row count using pg_stat.
func (c *Client) TableRowCount(ctx context.Context, tableName string) (int64, error) {
	var count int64
	err := c.pool.QueryRow(ctx,
		"SELECT COALESCE(n_live_tup, 0) FROM pg_stat_user_tables WHERE relname = $1",
		tableName,
	).Scan(&count)
	if err != nil {
		return 0, nil
	}
	return count, nil
}
