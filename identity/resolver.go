// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package identity provides unified collector node identity resolution.
// Both controlplaneext and arthastunnelext use this to ensure consistent
// node identification across the same process.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// ResolveNodeID returns the effective node ID for this collector instance.
// Resolution order:
//  1. configured value (if non-empty)
//  2. POD_NAME environment variable (for Kubernetes)
//  3. os.Hostname()
//  4. "unknown" as final fallback
//
// Use ResolveUniqueNodeID instead wherever the ID is used for distributed
// coordination (Redis claims, leader election, watermarks), because the fixed
// "unknown" fallback would collide across multiple hostname-less instances.
func ResolveNodeID(configured string) string {
	return resolveNodeID(configured, os.Getenv, os.Hostname, false)
}

// ResolveUniqueNodeID is like ResolveNodeID but guarantees uniqueness when
// neither POD_NAME nor os.Hostname() provides a stable identifier. Use this
// wherever nodeID participates in distributed coordination: two collector
// instances must never resolve to the same ID, or their Redis claim/watermark/
// leader-election keys collide.
//
// Resolution order is identical to ResolveNodeID except the final fallback:
//  1. configured value (if non-empty)
//  2. POD_NAME environment variable (for Kubernetes)
//  3. os.Hostname() (if non-empty)
//  4. "node-" + 8 hex chars from crypto/rand (unique per process lifetime)
func ResolveUniqueNodeID(configured string) string {
	return resolveNodeID(configured, os.Getenv, os.Hostname, true)
}

// resolveNodeID is the shared implementation. getenv and hostname are injected
// for testability; unique selects the terminal fallback (random suffix vs the
// fixed "unknown" string).
func resolveNodeID(configured string, getenv func(string) string, hostname func() (string, error), unique bool) string {
	if configured != "" {
		return configured
	}
	if podName := getenv("POD_NAME"); podName != "" {
		return podName
	}
	if h, err := hostname(); err == nil && h != "" {
		return h
	}
	if !unique {
		return "unknown"
	}
	// crypto/rand gives a 32-bit space — collision-free enough for the handful of
	// collector instances that might run without a hostname, and short enough to
	// read in logs and metric labels.
	b := make([]byte, 4)
	if _, err := rand.Read(b); err == nil {
		return "node-" + hex.EncodeToString(b)
	}
	// crypto/rand failure is catastrophic; fall back to a time-based suffix.
	return fmt.Sprintf("node-%d", time.Now().UnixNano())
}
