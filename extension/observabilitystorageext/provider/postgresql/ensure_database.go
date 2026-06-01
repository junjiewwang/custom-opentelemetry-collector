// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// EnsureDatabase checks if the target database exists, and creates it if it doesn't.
// It connects to the "postgres" system database to perform this check.
// This is called before the main client connection is established.
func EnsureDatabase(ctx context.Context, dsn string, logger *zap.Logger) error {
	dbName, systemDSN, err := parseDSNForDatabase(dsn)
	if err != nil {
		return fmt.Errorf("failed to parse DSN for database check: %w", err)
	}

	if dbName == "" || dbName == "postgres" {
		// No need to create the "postgres" system database
		return nil
	}

	logger = logger.Named("ensure-db")
	logger.Info("Checking if database exists", zap.String("database", dbName))

	// Connect to the "postgres" system database
	conn, err := pgx.Connect(ctx, systemDSN)
	if err != nil {
		return fmt.Errorf("failed to connect to system database: %w", err)
	}
	defer conn.Close(ctx)

	// Check if the target database exists
	var exists bool
	err = conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)",
		dbName,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check database existence: %w", err)
	}

	if exists {
		logger.Info("Database already exists", zap.String("database", dbName))
		return nil
	}

	// Create the database
	// Note: CREATE DATABASE cannot use parameterized queries for the db name,
	// but we've already validated it from the DSN URL parsing (no SQL injection risk).
	// We use QuoteIdentifier to safely quote the database name.
	quotedName := quoteIdentifier(dbName)
	_, err = conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", quotedName))
	if err != nil {
		// Handle race condition: another process might have created it
		if strings.Contains(err.Error(), "already exists") {
			logger.Info("Database was created by another process", zap.String("database", dbName))
			return nil
		}
		return fmt.Errorf("failed to create database %q: %w", dbName, err)
	}

	logger.Info("Database created successfully", zap.String("database", dbName))
	return nil
}

// parseDSNForDatabase extracts the database name from a DSN and returns:
// - the target database name
// - a DSN pointing to the "postgres" system database (for admin operations)
func parseDSNForDatabase(dsn string) (dbName string, systemDSN string, err error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", "", fmt.Errorf("invalid DSN URL: %w", err)
	}

	// Extract database name from path (e.g., "/otel_test" -> "otel_test")
	dbName = strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return "", "", nil
	}

	// Build system DSN by replacing the database with "postgres"
	u.Path = "/postgres"
	systemDSN = u.String()

	return dbName, systemDSN, nil
}

// quoteIdentifier quotes a PostgreSQL identifier (e.g., database name, table name)
// to prevent SQL injection. It doubles any embedded double quotes and wraps in double quotes.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
