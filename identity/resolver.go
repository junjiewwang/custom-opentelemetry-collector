// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package identity provides unified collector node identity resolution.
// Both controlplaneext and arthastunnelext use this to ensure consistent
// node identification across the same process.
package identity

import "os"

// ResolveNodeID returns the effective node ID for this collector instance.
// Resolution order:
//  1. configured value (if non-empty)
//  2. POD_NAME environment variable (for Kubernetes)
//  3. os.Hostname()
//  4. "unknown" as final fallback
func ResolveNodeID(configured string) string {
	if configured != "" {
		return configured
	}
	if podName := os.Getenv("POD_NAME"); podName != "" {
		return podName
	}
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}
	return "unknown"
}
