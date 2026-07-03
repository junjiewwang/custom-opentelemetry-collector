// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
)

const (
	defaultRuntimeSnapshotSharedSyncInterval = time.Second
	defaultRuntimeSnapshotLeaseTTL           = 2 * time.Second
)

type RuntimeSnapshotStore interface {
	Start(ctx context.Context) error
	Close() error
	Get(ctx context.Context, agentID string) (*agentRuntimeSnapshotCacheEntry, error)
	Upsert(ctx context.Context, agentID string, updater func(current *agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry) (*agentRuntimeSnapshotCacheEntry, error)
	MarkDirty(ctx context.Context, agentIDs []string) error
	TryAcquireRefreshLease(ctx context.Context, agentID, owner string, ttl time.Duration) (bool, error)
}

func runtimeSnapshotSharedSyncIntervalFromConfig(cfg Config) time.Duration {
	if cfg.RuntimeSnapshotSharedSyncInterval <= 0 {
		return defaultRuntimeSnapshotSharedSyncInterval
	}
	return time.Duration(cfg.RuntimeSnapshotSharedSyncInterval) * time.Millisecond
}

func runtimeSnapshotLeaseTTLFromConfig(cfg Config) time.Duration {
	if cfg.RuntimeSnapshotLeaseTTL <= 0 {
		return defaultRuntimeSnapshotLeaseTTL
	}
	return time.Duration(cfg.RuntimeSnapshotLeaseTTL) * time.Millisecond
}

func newRuntimeSnapshotInstanceID() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "collector"
	}
	instanceID, err := appmanager.GenerateID()
	if err != nil {
		return fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%s", strings.TrimSpace(hostname), instanceID)
}
