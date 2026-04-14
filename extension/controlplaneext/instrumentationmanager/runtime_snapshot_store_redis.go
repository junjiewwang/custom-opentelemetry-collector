// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	keyRuntimeSnapshotJSON         = "%s:runtime:agent:%s"
	keyRuntimeSnapshotLease        = "%s:runtime:lease:%s"
	keyRuntimeSnapshotDirtyChannel = "%s:runtime:events:dirty"
)

type runtimeSnapshotDirtyEvent struct {
	AgentIDs         []string `json:"agent_ids"`
	DirtyAtMillis    int64    `json:"dirty_at_millis"`
	SourceInstanceID string   `json:"source_instance_id,omitempty"`
}

type redisRuntimeSnapshotStore struct {
	logger             *zap.Logger
	client             redis.UniversalClient
	keyPrefix          string
	instanceID         string
	sharedSyncInterval time.Duration
	local              *memoryRuntimeSnapshotStore

	pubsub  *redis.PubSub
	stopCh  chan struct{}
	doneCh  chan struct{}
	started atomic.Bool
}

var _ RuntimeSnapshotStore = (*redisRuntimeSnapshotStore)(nil)

func newRedisRuntimeSnapshotStore(logger *zap.Logger, client redis.UniversalClient, keyPrefix, instanceID string, sharedSyncInterval time.Duration) *redisRuntimeSnapshotStore {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(keyPrefix) == "" {
		keyPrefix = "otel:instrumentation"
	}
	if strings.TrimSpace(instanceID) == "" {
		instanceID = newRuntimeSnapshotInstanceID()
	}
	if sharedSyncInterval <= 0 {
		sharedSyncInterval = defaultRuntimeSnapshotSharedSyncInterval
	}
	return &redisRuntimeSnapshotStore{
		logger:             logger,
		client:             client,
		keyPrefix:          strings.TrimSpace(keyPrefix),
		instanceID:         strings.TrimSpace(instanceID),
		sharedSyncInterval: sharedSyncInterval,
		local:              newMemoryRuntimeSnapshotStore(),
		stopCh:             make(chan struct{}),
		doneCh:             make(chan struct{}),
	}
}

func (s *redisRuntimeSnapshotStore) snapshotKey(agentID string) string {
	return fmt.Sprintf(keyRuntimeSnapshotJSON, s.keyPrefix, strings.TrimSpace(agentID))
}

func (s *redisRuntimeSnapshotStore) leaseKey(agentID string) string {
	return fmt.Sprintf(keyRuntimeSnapshotLease, s.keyPrefix, strings.TrimSpace(agentID))
}

func (s *redisRuntimeSnapshotStore) dirtyChannel() string {
	return fmt.Sprintf(keyRuntimeSnapshotDirtyChannel, s.keyPrefix)
}

func (s *redisRuntimeSnapshotStore) getClient() (redis.UniversalClient, error) {
	if s.client == nil {
		return nil, errors.New("redis client not initialized")
	}
	return s.client, nil
}

func (s *redisRuntimeSnapshotStore) Start(ctx context.Context) error {
	if s.started.Swap(true) {
		return nil
	}
	client, err := s.getClient()
	if err != nil {
		s.started.Store(false)
		return err
	}
	if err := client.Ping(ctx).Err(); err != nil {
		s.started.Store(false)
		return fmt.Errorf("failed to connect redis runtime snapshot store: %w", err)
	}
	if err := s.local.Start(ctx); err != nil {
		s.started.Store(false)
		return err
	}

	pubsub := client.Subscribe(ctx, s.dirtyChannel())
	msg, err := pubsub.Receive(ctx)
	if err != nil {
		_ = pubsub.Close()
		s.started.Store(false)
		return fmt.Errorf("subscribe runtime snapshot dirty channel: %w", err)
	}
	if sub, ok := msg.(*redis.Subscription); !ok || sub.Kind != "subscribe" {
		s.logger.Warn("Unexpected runtime snapshot pubsub confirmation", zap.Any("message", msg))
	}
	
	s.pubsub = pubsub
	go s.handleDirtyEvents()
	return nil
}

func (s *redisRuntimeSnapshotStore) Close() error {
	if !s.started.Swap(false) {
		return nil
	}
	if s.pubsub != nil {
		_ = s.pubsub.Close()
	}
	close(s.stopCh)
	<-s.doneCh
	return s.local.Close()
}

func (s *redisRuntimeSnapshotStore) handleDirtyEvents() {
	defer close(s.doneCh)
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		msg, err := s.pubsub.ReceiveMessage(context.Background())
		if err != nil {
			if !s.started.Load() {
				return
			}
			s.logger.Warn("Receive runtime snapshot dirty event failed", zap.Error(err))
			continue
		}
		var event runtimeSnapshotDirtyEvent
		if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
			s.logger.Warn("Ignore invalid runtime snapshot dirty event payload",
				zap.String("payload", msg.Payload),
				zap.Error(err),
			)
			continue
		}
		if len(event.AgentIDs) == 0 {
			continue
		}
		if err := s.local.MarkDirty(context.Background(), event.AgentIDs); err != nil {
			s.logger.Warn("Mark local runtime snapshot dirty from event failed", zap.Error(err))
		}
	}
}

func (s *redisRuntimeSnapshotStore) Get(ctx context.Context, agentID string) (*agentRuntimeSnapshotCacheEntry, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, nil
	}
	now := time.Now().UnixMilli()
	localEntry, err := s.local.Get(ctx, agentID)
	if err == nil && shouldUseLocalRuntimeSnapshot(localEntry, now, s.sharedSyncInterval) {
		return localEntry, nil
	}

	entry, err := s.getShared(ctx, agentID)
	if err != nil {
		if localEntry != nil {
			s.logger.Warn("Fallback to local runtime snapshot cache after shared read failure",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
			return localEntry, nil
		}
		return nil, err
	}
	if entry == nil {
		_, _ = s.local.Upsert(context.Background(), agentID, func(*agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
			return nil
		})
		return nil, nil
	}
	s.persistLocal(entry, now)
	return cloneAgentRuntimeSnapshotCacheEntry(entry), nil
}

func (s *redisRuntimeSnapshotStore) getShared(ctx context.Context, agentID string) (*agentRuntimeSnapshotCacheEntry, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	data, err := client.Get(ctx, s.snapshotKey(agentID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get runtime snapshot: %w", err)
	}
	var entry agentRuntimeSnapshotCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal runtime snapshot: %w", err)
	}
	entry.LocalSyncedAtMillis = 0
	return cloneAgentRuntimeSnapshotCacheEntry(&entry), nil
}

func (s *redisRuntimeSnapshotStore) persistLocal(entry *agentRuntimeSnapshotCacheEntry, syncedAt int64) {
	if entry == nil {
		return
	}
	copyEntry := cloneAgentRuntimeSnapshotCacheEntry(entry)
	copyEntry.LocalSyncedAtMillis = syncedAt
	_, _ = s.local.Upsert(context.Background(), copyEntry.AgentID, func(*agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
		return copyEntry
	})
}

func (s *redisRuntimeSnapshotStore) Upsert(ctx context.Context, agentID string, updater func(current *agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry) (*agentRuntimeSnapshotCacheEntry, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || updater == nil {
		return nil, nil
	}
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	key := s.snapshotKey(agentID)
	const maxRetries = 5
	for i := 0; i < maxRetries; i++ {
		var nextCopy *agentRuntimeSnapshotCacheEntry
		err = client.Watch(ctx, func(tx *redis.Tx) error {
			var current *agentRuntimeSnapshotCacheEntry
			data, err := tx.Get(ctx, key).Bytes()
			if err != nil && err != redis.Nil {
				return err
			}
			if err != redis.Nil {
				var decoded agentRuntimeSnapshotCacheEntry
				if err := json.Unmarshal(data, &decoded); err != nil {
					return fmt.Errorf("unmarshal runtime snapshot: %w", err)
				}
				current = &decoded
			}
			next := updater(cloneAgentRuntimeSnapshotCacheEntry(current))
			if next == nil {
				nextCopy = nil
				_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.Del(ctx, key)
					return nil
				})
				return err
			}
			next.AgentID = agentID
			nextCopy = cloneAgentRuntimeSnapshotCacheEntry(next)
			payload, err := json.Marshal(nextCopy)
			if err != nil {
				return fmt.Errorf("marshal runtime snapshot: %w", err)
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, payload, 0)
				return nil
			})
			return err
		}, key)
		if err == nil {
			syncedAt := time.Now().UnixMilli()
			if nextCopy == nil {
				_, _ = s.local.Upsert(context.Background(), agentID, func(*agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
					return nil
				})
				return nil, nil
			}
			s.persistLocal(nextCopy, syncedAt)
			return cloneAgentRuntimeSnapshotCacheEntry(nextCopy), nil
		}
		if errors.Is(err, redis.TxFailedErr) {
			sleepBeforeRedisRetry(i + 1)
			continue
		}
		return nil, fmt.Errorf("upsert runtime snapshot: %w", err)
	}
	return nil, fmt.Errorf("upsert runtime snapshot: %w", redis.TxFailedErr)
}

func (s *redisRuntimeSnapshotStore) MarkDirty(ctx context.Context, agentIDs []string) error {
	agentIDs = uniqueStrings(agentIDs)
	if len(agentIDs) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	for _, agentID := range agentIDs {
		if _, err := s.Upsert(ctx, agentID, func(current *agentRuntimeSnapshotCacheEntry) *agentRuntimeSnapshotCacheEntry {
			next := current
			if next == nil {
				next = &agentRuntimeSnapshotCacheEntry{AgentID: agentID}
			}
			next.Dirty = true
			next.ExpiresAtMillis = now
			next.UpdatedAtMillis = now
			next.OwnerInstanceID = s.instanceID
			return next
		}); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(&runtimeSnapshotDirtyEvent{
		AgentIDs:         agentIDs,
		DirtyAtMillis:    now,
		SourceInstanceID: s.instanceID,
	})
	if err != nil {
		return fmt.Errorf("marshal runtime snapshot dirty event: %w", err)
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	if err := client.Publish(ctx, s.dirtyChannel(), payload).Err(); err != nil {
		return fmt.Errorf("publish runtime snapshot dirty event: %w", err)
	}
	return nil
}

func (s *redisRuntimeSnapshotStore) TryAcquireRefreshLease(ctx context.Context, agentID, owner string, ttl time.Duration) (bool, error) {
	agentID = strings.TrimSpace(agentID)
	owner = strings.TrimSpace(owner)
	if agentID == "" {
		return false, nil
	}
	if ttl <= 0 {
		return true, nil
	}
	client, err := s.getClient()
	if err != nil {
		return false, err
	}
	ok, err := client.SetNX(ctx, s.leaseKey(agentID), owner, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("acquire runtime snapshot lease: %w", err)
	}
	if ok {
		return true, nil
	}
	if owner == "" {
		return false, nil
	}
	holder, err := client.Get(ctx, s.leaseKey(agentID)).Result()
	if err == nil && holder == owner {
		if expireErr := client.PExpire(ctx, s.leaseKey(agentID), ttl).Err(); expireErr != nil {
			return false, fmt.Errorf("extend runtime snapshot lease: %w", expireErr)
		}
		return true, nil
	}
	if err != nil && err != redis.Nil {
		return false, fmt.Errorf("read runtime snapshot lease owner: %w", err)
	}
	return false, nil
}

func shouldUseLocalRuntimeSnapshot(entry *agentRuntimeSnapshotCacheEntry, now int64, syncInterval time.Duration) bool {
	if entry == nil {
		return false
	}
	if entry.Dirty || entry.ExpiresAtMillis <= now {
		return false
	}
	if syncInterval <= 0 || entry.LocalSyncedAtMillis <= 0 {
		return false
	}
	return entry.LocalSyncedAtMillis+syncInterval.Milliseconds() > now
}
