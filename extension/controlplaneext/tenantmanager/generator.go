// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantmanager

import (
	"crypto/rand"
)

// base62Chars is the character set for Base62 encoding (URL-safe, human-friendly).
const base62Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// defaultTenantIDLength is the length for generated tenant IDs. 16 chars of
// Base62 provides ~95 bits of entropy.
const defaultTenantIDLength = 16

// IDGenerator provides unique tenant IDs. Injected into TenantService to make
// ID generation deterministic for testing.
type IDGenerator interface {
	Generate() (string, error)
}

// cryptoIDGenerator uses crypto/rand for secure IDs.
type cryptoIDGenerator struct{}

// NewIDGenerator returns the production ID generator.
func NewIDGenerator() IDGenerator {
	return &cryptoIDGenerator{}
}

func (g *cryptoIDGenerator) Generate() (string, error) {
	return generateBase62String(defaultTenantIDLength)
}

// FixedIDGenerator returns a fixed ID string. Useful for deterministic tests.
type FixedIDGenerator string

func (g FixedIDGenerator) Generate() (string, error) {
	return string(g), nil
}

// generateBase62String generates a random Base62 string of the given length.
func generateBase62String(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	result := make([]byte, length)
	for i, b := range bytes {
		result[i] = base62Chars[int(b)%len(base62Chars)]
	}
	return string(result), nil
}
