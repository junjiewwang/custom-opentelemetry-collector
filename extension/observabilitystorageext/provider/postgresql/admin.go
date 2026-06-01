// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// Admin provides administrative operations for the PostgreSQL storage backend.
type Admin struct {
	client         *Client
	config         *Config
	logger         *zap.Logger
	hasTimescaleDB bool
}

// NewAdmin creates a new Admin instance.
func NewAdmin(client *Client, config *Config, logger *zap.Logger, hasTimescaleDB bool) *Admin {
	return &Admin{
		client:         client,
		config:         config,
		logger:         logger.Named("pg-admin"),
		hasTimescaleDB: hasTimescaleDB,
	}
}

// InitSchema is a no-op for PG since migrations handle schema setup.
// It exists to satisfy the interface contract.
func (a *Admin) InitSchema(ctx context.Context) error {
	migrator := NewMigrator(a.config.DSN, a.logger)
	return migrator.Up()
}

// GetStatus returns the current PostgreSQL cluster status.
func (a *Admin) GetStatus(ctx context.Context) (map[string]any, error) {
	version, err := a.client.GetVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get PG version: %w", err)
	}

	status := map[string]any{
		"provider":        "postgresql",
		"version":         version,
		"has_timescaledb": a.hasTimescaleDB,
		"status":          "green",
	}

	// Check connectivity
	if err := a.client.Ping(ctx); err != nil {
		status["status"] = "red"
		status["error"] = err.Error()
	}

	return status, nil
}

// GetIndicesStats returns table size and row count statistics (analogous to ES indices stats).
func (a *Admin) GetIndicesStats(ctx context.Context) (map[string]any, error) {
	tables := []struct {
		name   string
		signal string
	}{
		{a.config.Traces.TableName, "trace"},
		{a.config.Metrics.TableName, "metric"},
		{a.config.Logs.TableName, "log"},
	}

	stats := make(map[string]any)
	var totalSize int64

	for _, t := range tables {
		size, err := a.client.TableSize(ctx, t.name)
		if err != nil {
			a.logger.Warn("Failed to get table size", zap.String("table", t.name), zap.Error(err))
			continue
		}
		rowCount, _ := a.client.TableRowCount(ctx, t.name)
		totalSize += size

		stats[t.signal] = map[string]any{
			"table":     t.name,
			"size":      size,
			"row_count": rowCount,
		}
	}

	stats["total_size"] = totalSize
	return stats, nil
}

// SetRetention updates the retention policy for a signal's table.
// For native PG: drops old partitions. For TimescaleDB: updates data_retention policy.
func (a *Admin) SetRetention(ctx context.Context, tableName string, retention time.Duration) error {
	if retention <= 0 {
		return fmt.Errorf("retention must be positive, got %v", retention)
	}

	if a.hasTimescaleDB && tableName == a.config.Metrics.TableName {
		// Use TimescaleDB retention policy
		_, err := a.client.Exec(ctx, fmt.Sprintf(
			"SELECT add_retention_policy('%s', INTERVAL '%d days', if_not_exists => true)",
			tableName, int(retention.Hours()/24),
		))
		return err
	}

	// For native partitions: record the retention (actual cleanup done by Purge)
	a.logger.Info("Retention policy updated (will be enforced by Purge operation)",
		zap.String("table", tableName),
		zap.Duration("retention", retention),
	)
	return nil
}

// Purge removes data older than the specified time from the given table.
func (a *Admin) Purge(ctx context.Context, tableName string, timestampField string, before time.Time) (int64, error) {
	sql := fmt.Sprintf(
		"DELETE FROM %s WHERE %s < $1",
		tableName, timestampField,
	)
	deleted, err := a.client.Exec(ctx, sql, before)
	if err != nil {
		return 0, fmt.Errorf("purge failed for %s: %w", tableName, err)
	}

	a.logger.Info("Purge completed",
		zap.String("table", tableName),
		zap.Time("before", before),
		zap.Int64("deleted", deleted),
	)
	return deleted, nil
}

// PurgeByApp removes data for a specific app older than the specified time.
func (a *Admin) PurgeByApp(ctx context.Context, tableName string, timestampField string, appID string, before time.Time) (int64, error) {
	sql := fmt.Sprintf(
		"DELETE FROM %s WHERE %s < $1 AND app_id = $2",
		tableName, timestampField,
	)
	deleted, err := a.client.Exec(ctx, sql, before, appID)
	if err != nil {
		return 0, fmt.Errorf("purge by app failed for %s (app=%s): %w", tableName, appID, err)
	}

	a.logger.Info("PurgeByApp completed",
		zap.String("table", tableName),
		zap.String("app_id", appID),
		zap.Time("before", before),
		zap.Int64("deleted", deleted),
	)
	return deleted, nil
}

// DropOldPartitions drops partitions older than the retention period.
func (a *Admin) DropOldPartitions(ctx context.Context, tableName string, retention time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-retention)

	// Query for partitions with upper bound older than cutoff
	rows, err := a.client.Query(ctx, `
		SELECT inhrelid::regclass::text AS partition_name
		FROM pg_inherits
		JOIN pg_class parent ON parent.oid = inhparent
		WHERE parent.relname = $1
		  AND inhrelid::regclass::text != $2
		ORDER BY inhrelid::regclass::text
	`, tableName, tableName+"_default")
	if err != nil {
		return 0, fmt.Errorf("failed to list partitions: %w", err)
	}
	defer rows.Close()

	var dropped int
	for rows.Next() {
		var partName string
		if err := rows.Scan(&partName); err != nil {
			continue
		}

		// Check if partition is entirely before cutoff by querying its max timestamp
		var maxTS *time.Time
		err := a.client.QueryRow(ctx,
			fmt.Sprintf("SELECT MAX(start_time) FROM %s", partName),
		).Scan(&maxTS)
		if err != nil || maxTS == nil {
			continue
		}

		if maxTS.Before(cutoff) {
			_, err := a.client.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", partName))
			if err != nil {
				a.logger.Warn("Failed to drop old partition",
					zap.String("partition", partName),
					zap.Error(err),
				)
				continue
			}
			dropped++
			a.logger.Info("Dropped old partition",
				zap.String("partition", partName),
				zap.Time("max_time", *maxTS),
			)
		}
	}

	return dropped, nil
}
