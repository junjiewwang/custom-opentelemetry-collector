// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// TestNewClient_RejectsEmptyAddresses guards against the divide-by-zero panic
// in getNextAddress when no ES nodes are configured.
func TestNewClient_RejectsEmptyAddresses(t *testing.T) {
	_, err := NewClient(&Config{}, zaptest.NewLogger(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "addresses")
}

// TestNewClient_AcceptsAddresses confirms a non-empty address list constructs
// without error.
func TestNewClient_AcceptsAddresses(t *testing.T) {
	c, err := NewClient(&Config{Addresses: []string{"http://localhost:9200"}}, zaptest.NewLogger(t))
	require.NoError(t, err)
	assert.NotNil(t, c)
}
