// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisClient abstracts the Redis operations needed by the store.
// Compatible with *redis.Client, *redis.ClusterClient, etc.
type RedisClient interface {
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	Get(ctx context.Context, key string) *redis.StringCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	ZAdd(ctx context.Context, key string, members ...redis.Z) *redis.IntCmd
	ZRem(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	ZRangeByScore(ctx context.Context, key string, opt *redis.ZRangeBy) *redis.StringSliceCmd
}

// redisStore persists notification records in Redis.
//
// Key layout:
//
//	{prefix}:record:{id}              — JSON record (STRING with TTL)
//	{prefix}:idx:task:{taskID}        — record ID (STRING with TTL)
//	{prefix}:idx:status:{status}      — record IDs sorted by time (ZSET)
type redisStore struct {
	client    RedisClient
	keyPrefix string
	recordTTL time.Duration
}

// NewRedisStore creates a Redis-backed notification store.
func NewRedisStore(client RedisClient, keyPrefix string, recordTTL time.Duration) Store {
	if recordTTL <= 0 {
		recordTTL = 72 * time.Hour
	}
	return &redisStore{
		client:    client,
		keyPrefix: keyPrefix,
		recordTTL: recordTTL,
	}
}

func (s *redisStore) recordKey(id string) string {
	return fmt.Sprintf("%s:record:%s", s.keyPrefix, id)
}

func (s *redisStore) taskIndexKey(taskID string) string {
	return fmt.Sprintf("%s:idx:task:%s", s.keyPrefix, taskID)
}

func (s *redisStore) statusIndexKey(status Status) string {
	return fmt.Sprintf("%s:idx:status:%s", s.keyPrefix, status)
}

func (s *redisStore) Save(ctx context.Context, record *Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}

	if err := s.client.Set(ctx, s.recordKey(record.ID), data, s.recordTTL).Err(); err != nil {
		return fmt.Errorf("save record: %w", err)
	}

	// Task → record index
	if err := s.client.Set(ctx, s.taskIndexKey(record.TaskID), record.ID, s.recordTTL).Err(); err != nil {
		return fmt.Errorf("save task index: %w", err)
	}

	// Status index (score = unix timestamp for ordering)
	score := float64(record.CreatedAt.Unix())
	if err := s.client.ZAdd(ctx, s.statusIndexKey(record.Status), redis.Z{
		Score:  score,
		Member: record.ID,
	}).Err(); err != nil {
		return fmt.Errorf("save status index: %w", err)
	}

	return nil
}

func (s *redisStore) Get(ctx context.Context, id string) (*Record, error) {
	data, err := s.client.Get(ctx, s.recordKey(id)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get record: %w", err)
	}

	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("unmarshal record: %w", err)
	}
	return &record, nil
}

func (s *redisStore) GetByTaskID(ctx context.Context, taskID string) (*Record, error) {
	id, err := s.client.Get(ctx, s.taskIndexKey(taskID)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get task index: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *redisStore) ListByStatus(ctx context.Context, status Status, limit int) ([]*Record, error) {
	if limit <= 0 {
		limit = 50
	}

	ids, err := s.client.ZRangeByScore(ctx, s.statusIndexKey(status), &redis.ZRangeBy{
		Min:   "-inf",
		Max:   "+inf",
		Count: int64(limit),
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("list by status: %w", err)
	}

	records := make([]*Record, 0, len(ids))
	for _, id := range ids {
		record, err := s.Get(ctx, id)
		if err != nil {
			continue
		}
		if record != nil {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s *redisStore) Update(ctx context.Context, record *Record) error {
	// Load old record to handle status index migration
	old, err := s.Get(ctx, record.ID)
	if err != nil {
		return err
	}

	// Save updated record
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}

	if err := s.client.Set(ctx, s.recordKey(record.ID), data, s.recordTTL).Err(); err != nil {
		return fmt.Errorf("update record: %w", err)
	}

	// Migrate status index if status changed
	if old != nil && old.Status != record.Status {
		_ = s.client.ZRem(ctx, s.statusIndexKey(old.Status), record.ID).Err()
		score := float64(record.CreatedAt.Unix())
		_ = s.client.ZAdd(ctx, s.statusIndexKey(record.Status), redis.Z{
			Score:  score,
			Member: record.ID,
		}).Err()
	}

	return nil
}
