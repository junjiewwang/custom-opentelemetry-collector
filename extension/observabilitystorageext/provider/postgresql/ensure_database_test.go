// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package postgresql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDSNForDatabase(t *testing.T) {
	tests := []struct {
		name         string
		dsn          string
		wantDB       string
		wantSysDSN   string
		wantErr      bool
	}{
		{
			name:       "standard postgres DSN",
			dsn:        "postgres://user:pass@localhost:5432/otel_test?sslmode=disable",
			wantDB:     "otel_test",
			wantSysDSN: "postgres://user:pass@localhost:5432/postgres?sslmode=disable",
		},
		{
			name:       "postgresql scheme",
			dsn:        "postgresql://admin:secret@db.example.com:5433/mydb?sslmode=require",
			wantDB:     "mydb",
			wantSysDSN: "postgresql://admin:secret@db.example.com:5433/postgres?sslmode=require",
		},
		{
			name:       "postgres system database (no-op)",
			dsn:        "postgres://user:pass@localhost:5432/postgres?sslmode=disable",
			wantDB:     "postgres",
			wantSysDSN: "postgres://user:pass@localhost:5432/postgres?sslmode=disable",
		},
		{
			name:       "no database in path",
			dsn:        "postgres://user:pass@localhost:5432/?sslmode=disable",
			wantDB:     "",
			wantSysDSN: "",
		},
		{
			name:       "password with special characters",
			dsn:        "postgres://user:p%40ss%23w0rd@localhost:5432/otel_data?sslmode=disable",
			wantDB:     "otel_data",
			wantSysDSN: "postgres://user:p%40ss%23w0rd@localhost:5432/postgres?sslmode=disable",
		},
		{
			name:    "invalid URL",
			dsn:     "://not-a-valid-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbName, sysDSN, err := parseDSNForDatabase(tt.dsn)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantDB, dbName)
			if tt.wantSysDSN != "" {
				assert.Equal(t, tt.wantSysDSN, sysDSN)
			}
		})
	}
}

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "simple name",
			input:  "otel_test",
			expect: `"otel_test"`,
		},
		{
			name:   "name with double quotes",
			input:  `my"db`,
			expect: `"my""db"`,
		},
		{
			name:   "name with spaces",
			input:  "my database",
			expect: `"my database"`,
		},
		{
			name:   "name with uppercase",
			input:  "OtelDB",
			expect: `"OtelDB"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := quoteIdentifier(tt.input)
			assert.Equal(t, tt.expect, result)
		})
	}
}
