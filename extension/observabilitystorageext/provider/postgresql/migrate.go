// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // pgx v5 driver for migrate
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"go.uber.org/zap"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrator handles database schema migrations using golang-migrate.
type Migrator struct {
	dsn    string
	logger *zap.Logger
}

// NewMigrator creates a new Migrator instance.
func NewMigrator(dsn string, logger *zap.Logger) *Migrator {
	return &Migrator{
		dsn:    dsn,
		logger: logger.Named("pg-migrator"),
	}
}

// Up applies all pending migrations.
func (m *Migrator) Up() error {
	mig, err := m.newMigrate()
	if err != nil {
		return err
	}
	defer mig.Close()

	if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migration up failed: %w", err)
	}

	version, dirty, _ := mig.Version()
	m.logger.Info("Schema migration completed",
		zap.Uint("version", version),
		zap.Bool("dirty", dirty),
	)
	return nil
}

// Version returns the current migration version.
func (m *Migrator) Version() (uint, bool, error) {
	mig, err := m.newMigrate()
	if err != nil {
		return 0, false, err
	}
	defer mig.Close()

	return mig.Version()
}

// Down rolls back one migration step.
func (m *Migrator) Down() error {
	mig, err := m.newMigrate()
	if err != nil {
		return err
	}
	defer mig.Close()

	if err := mig.Steps(-1); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migration down failed: %w", err)
	}
	return nil
}

// newMigrate creates a new migrate instance with embedded SQL files.
func (m *Migrator) newMigrate() (*migrate.Migrate, error) {
	// Get the migrations subdirectory from the embedded FS
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("failed to get migrations sub-filesystem: %w", err)
	}

	source, err := iofs.New(subFS, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to create migration source: %w", err)
	}

	// golang-migrate expects pgx5:// scheme for pgx v5 driver
	dbURL := convertDSNToMigrateURL(m.dsn)
	mig, err := migrate.NewWithSourceInstance("iofs", source, dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create migrate instance: %w", err)
	}

	return mig, nil
}

// convertDSNToMigrateURL converts a standard postgres:// DSN to pgx5:// scheme
// that golang-migrate expects for the pgx v5 driver.
func convertDSNToMigrateURL(dsn string) string {
	if len(dsn) > 11 && dsn[:11] == "postgres://" {
		return "pgx5://" + dsn[11:]
	}
	if len(dsn) > 13 && dsn[:13] == "postgresql://" {
		return "pgx5://" + dsn[13:]
	}
	return dsn
}
