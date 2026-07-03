// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

// ═══════════════════════════════════════════════════
// IDGenerator — injectable ID generation
// ═══════════════════════════════════════════════════

// IDGenerator provides unique application IDs. Injected into AppService
// to make ID generation deterministic for testing.
type IDGenerator interface {
	Generate() (string, error)
}

// TokenGenerator provides unique authentication tokens.
type TokenGenerator interface {
	Generate() (string, error)
}

// ═══════════════════════════════════════════════════
// Default (production) implementations
// ═══════════════════════════════════════════════════

// cryptoIDGenerator uses crypto/rand for secure IDs.
type cryptoIDGenerator struct{}

// NewIDGenerator returns the production ID generator.
func NewIDGenerator() IDGenerator {
	return &cryptoIDGenerator{}
}

func (g *cryptoIDGenerator) Generate() (string, error) {
	return GenerateID()
}

// cryptoTokenGenerator uses crypto/rand for secure tokens.
type cryptoTokenGenerator struct{}

// NewTokenGenerator returns the production token generator.
func NewTokenGenerator() TokenGenerator {
	return &cryptoTokenGenerator{}
}

func (g *cryptoTokenGenerator) Generate() (string, error) {
	return GenerateToken(0)
}

// ═══════════════════════════════════════════════════
// Test doubles (deterministic, fixed output)
// ═══════════════════════════════════════════════════

// FixedIDGenerator returns a fixed ID string. Useful for deterministic tests.
type FixedIDGenerator string

func (g FixedIDGenerator) Generate() (string, error) {
	return string(g), nil
}

// FixedTokenGenerator returns a fixed token string. Useful for deterministic tests.
type FixedTokenGenerator string

func (g FixedTokenGenerator) Generate() (string, error) {
	return string(g), nil
}
