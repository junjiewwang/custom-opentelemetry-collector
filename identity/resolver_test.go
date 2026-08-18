// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"errors"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveNodeID_Precedence(t *testing.T) {
	tests := []struct {
		name      string
		configured string
		podName   string
		hostname  string
		hostErr   error
		want      string
	}{
		{"configured wins", "cfg-node", "", "host", nil, "cfg-node"},
		{"pod name over hostname", "", "pod-123", "host", nil, "pod-123"},
		{"hostname fallback", "", "", "my-host", nil, "my-host"},
		{"unknown fallback", "", "", "", errors.New("no hostname"), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string {
				if k == "POD_NAME" {
					return tt.podName
				}
				return ""
			}
			hostname := func() (string, error) { return tt.hostname, tt.hostErr }
			got := resolveNodeID(tt.configured, getenv, hostname, false)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveUniqueNodeID_UniqueFallback(t *testing.T) {
	// Same precedence as ResolveNodeID for the non-random paths.
	t.Run("configured wins", func(t *testing.T) {
		got := resolveNodeID("cfg", func(string) string { return "" }, func() (string, error) { return "h", nil }, true)
		assert.Equal(t, "cfg", got)
	})
	t.Run("pod name wins", func(t *testing.T) {
		got := resolveNodeID("", func(string) string { return "pod-9" }, func() (string, error) { return "h", nil }, true)
		assert.Equal(t, "pod-9", got)
	})
	t.Run("hostname wins", func(t *testing.T) {
		got := resolveNodeID("", func(string) string { return "" }, func() (string, error) { return "my-host", nil }, true)
		assert.Equal(t, "my-host", got)
	})

	// When nothing is available, produce a unique random suffix (NOT "unknown").
	t.Run("random suffix when no identity source", func(t *testing.T) {
		noEnv := func(string) string { return "" }
		noHost := func() (string, error) { return "", errors.New("none") }
		a := resolveNodeID("", noEnv, noHost, true)
		b := resolveNodeID("", noEnv, noHost, true)
		assert.Regexp(t, regexp.MustCompile(`^node-[0-9a-f]{8}$`), a)
		assert.NotEqual(t, a, b, "two calls must yield distinct node IDs")
	})
}
