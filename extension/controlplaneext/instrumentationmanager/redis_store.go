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
	keyRulesHash   = "%s:rules"
	keyTargetsJSON = "%s:targets:%s"
)

// RedisRuleStore persists instrumentation rules and target statuses in Redis.
//
// Key layout:
//   - {keyPrefix}:rules             — Hash: ruleID -> Rule JSON
//   - {keyPrefix}:targets:{ruleID}  — String: []RuleTargetStatus JSON
//
// The store keeps the target list for a rule in a single JSON blob so a full
// target refresh stays atomic at the rule level.
type RedisRuleStore struct {
	logger    *zap.Logger
	client    redis.UniversalClient
	keyPrefix string
	started   atomic.Bool
}

var _ RuleStore = (*RedisRuleStore)(nil)

func NewRedisRuleStore(logger *zap.Logger, client redis.UniversalClient, keyPrefix string) *RedisRuleStore {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(keyPrefix) == "" {
		keyPrefix = "otel:instrumentation"
	}
	return &RedisRuleStore{
		logger:    logger,
		client:    client,
		keyPrefix: strings.TrimSpace(keyPrefix),
	}
}

func (s *RedisRuleStore) rulesKey() string {
	return fmt.Sprintf(keyRulesHash, s.keyPrefix)
}

func (s *RedisRuleStore) targetKey(ruleID string) string {
	return fmt.Sprintf(keyTargetsJSON, s.keyPrefix, strings.TrimSpace(ruleID))
}

func (s *RedisRuleStore) targetsPattern() string {
	return fmt.Sprintf(keyTargetsJSON, s.keyPrefix, "*")
}

func (s *RedisRuleStore) targetsKeyPrefix() string {
	return fmt.Sprintf(keyTargetsJSON, s.keyPrefix, "")
}

func (s *RedisRuleStore) getClient() (redis.UniversalClient, error) {
	if s.client == nil {
		return nil, errors.New("redis client not initialized")
	}
	return s.client, nil
}

func (s *RedisRuleStore) Start(ctx context.Context) error {
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
		return fmt.Errorf("failed to connect to redis: %w", err)
	}
	if err := s.validateStoredData(ctx); err != nil {
		s.started.Store(false)
		return err
	}
	s.logger.Info("Starting Redis instrumentation rule store", zap.String("key_prefix", s.keyPrefix))
	return nil
}

func (s *RedisRuleStore) Close() error {
	s.started.Store(false)
	return nil
}

func (s *RedisRuleStore) SaveRule(ctx context.Context, rule *Rule, isNew bool) error {
	if rule == nil || strings.TrimSpace(rule.ID) == "" {
		return errors.New("rule_id is required")
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}

	payload := cloneRule(rule)
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal rule: %w", err)
	}

	const maxRetries = 3
	for i := 0; i < maxRetries; i++ {
		err = client.Watch(ctx, func(tx *redis.Tx) error {
			exists, err := tx.HExists(ctx, s.rulesKey(), payload.ID).Result()
			if err != nil {
				return err
			}
			if exists && isNew {
				return errors.New("instrumentation rule already exists: " + payload.ID)
			}
			if !exists && !isNew {
				return ErrRuleNotFound
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.HSet(ctx, s.rulesKey(), payload.ID, data)
				return nil
			})
			return err
		}, s.rulesKey())
		if err == nil {
			return nil
		}
		if errors.Is(err, redis.TxFailedErr) {
			sleepBeforeRedisRetry(i + 1)
			continue
		}
		return fmt.Errorf("save rule: %w", err)
	}
	return fmt.Errorf("save rule: %w", redis.TxFailedErr)
}

func (s *RedisRuleStore) GetRule(ctx context.Context, ruleID string) (*Rule, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	data, err := client.HGet(ctx, s.rulesKey(), strings.TrimSpace(ruleID)).Bytes()
	if err == redis.Nil {
		return nil, ErrRuleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get rule: %w", err)
	}

	var rule Rule
	if err := json.Unmarshal(data, &rule); err != nil {
		return nil, fmt.Errorf("unmarshal rule: %w", err)
	}
	return cloneRule(&rule), nil
}

func (s *RedisRuleStore) ListRules(ctx context.Context, query ListRulesQuery) ([]*Rule, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	entries, err := client.HGetAll(ctx, s.rulesKey()).Result()
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}

	out := make([]*Rule, 0, len(entries))
	for ruleID, data := range entries {
		var rule Rule
		if err := json.Unmarshal([]byte(data), &rule); err != nil {
			s.logger.Warn("Skip invalid instrumentation rule JSON while listing",
				zap.String("rule_id", ruleID),
				zap.Error(err),
			)
			continue
		}
		out = append(out, cloneRule(&rule))
	}
	return filterAndSortRules(out, query), nil
}

func (s *RedisRuleStore) SaveTargetStatuses(ctx context.Context, ruleID string, targets []*RuleTargetStatus) error {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return ErrRuleNotFound
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}

	payload := cloneAndSortTargetStatuses(targets)
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal target statuses: %w", err)
	}

	const maxRetries = 3
	for i := 0; i < maxRetries; i++ {
		err = client.Watch(ctx, func(tx *redis.Tx) error {
			exists, err := tx.HExists(ctx, s.rulesKey(), ruleID).Result()
			if err != nil {
				return err
			}
			if !exists {
				return ErrRuleNotFound
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if len(payload) == 0 {
					pipe.Del(ctx, s.targetKey(ruleID))
					return nil
				}
				pipe.Set(ctx, s.targetKey(ruleID), data, 0)
				return nil
			})
			return err
		}, s.rulesKey(), s.targetKey(ruleID))
		if err == nil {
			return nil
		}
		if errors.Is(err, redis.TxFailedErr) {
			sleepBeforeRedisRetry(i + 1)
			continue
		}
		return fmt.Errorf("save target statuses: %w", err)
	}
	return fmt.Errorf("save target statuses: %w", redis.TxFailedErr)
}

func (s *RedisRuleStore) ListTargetStatuses(ctx context.Context, ruleID string) ([]*RuleTargetStatus, error) {
	if _, err := s.GetRule(ctx, ruleID); err != nil {
		return nil, err
	}
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	data, err := client.Get(ctx, s.targetKey(ruleID)).Bytes()
	if err == redis.Nil {
		return []*RuleTargetStatus{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get target statuses: %w", err)
	}

	var targets []*RuleTargetStatus
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, fmt.Errorf("unmarshal target statuses: %w", err)
	}
	return cloneAndSortTargetStatuses(targets), nil
}

func (s *RedisRuleStore) PhysicalDeleteRule(ctx context.Context, ruleID string) error {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return ErrRuleNotFound
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}

	pipe := client.Pipeline()
	pipe.HDel(ctx, s.rulesKey(), ruleID)
	pipe.Del(ctx, s.targetKey(ruleID))
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("physical delete rule %s: %w", ruleID, err)
	}
	return nil
}

func (s *RedisRuleStore) validateStoredData(ctx context.Context) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	entries, err := client.HGetAll(ctx, s.rulesKey()).Result()
	if err != nil {
		return fmt.Errorf("validate rules: %w", err)
	}

	validRuleIDs := make(map[string]struct{}, len(entries))
	for ruleID, data := range entries {
		var rule Rule
		if err := json.Unmarshal([]byte(data), &rule); err != nil || strings.TrimSpace(rule.ID) != strings.TrimSpace(ruleID) {
			s.logger.Warn("Remove invalid instrumentation rule during startup validation",
				zap.String("rule_id", ruleID),
				zap.Error(err),
			)
			if err := client.HDel(ctx, s.rulesKey(), ruleID).Err(); err != nil {
				return fmt.Errorf("cleanup invalid rule %s: %w", ruleID, err)
			}
			if err := client.Del(ctx, s.targetKey(ruleID)).Err(); err != nil {
				return fmt.Errorf("cleanup invalid rule targets %s: %w", ruleID, err)
			}
			continue
		}
		validRuleIDs[ruleID] = struct{}{}
	}

	iter := client.Scan(ctx, 0, s.targetsPattern(), 100).Iterator()
	prefix := s.targetsKeyPrefix()
	for iter.Next(ctx) {
		key := iter.Val()
		ruleID := strings.TrimPrefix(key, prefix)
		if ruleID == "" {
			continue
		}
		if _, ok := validRuleIDs[ruleID]; !ok {
			s.logger.Warn("Remove orphan instrumentation target snapshot during startup validation", zap.String("rule_id", ruleID))
			if err := client.Del(ctx, key).Err(); err != nil {
				return fmt.Errorf("cleanup orphan targets %s: %w", ruleID, err)
			}
			continue
		}

		data, err := client.Get(ctx, key).Bytes()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return fmt.Errorf("read target statuses for %s: %w", ruleID, err)
		}
		var targets []*RuleTargetStatus
		if err := json.Unmarshal(data, &targets); err != nil {
			s.logger.Warn("Remove invalid instrumentation target payload during startup validation",
				zap.String("rule_id", ruleID),
				zap.Error(err),
			)
			if err := client.Del(ctx, key).Err(); err != nil {
				return fmt.Errorf("cleanup invalid targets %s: %w", ruleID, err)
			}
		}
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("scan instrumentation target keys: %w", err)
	}
	return nil
}

func sleepBeforeRedisRetry(attempt int) {
	if attempt <= 0 {
		return
	}
	time.Sleep(time.Duration(attempt) * 10 * time.Millisecond)
}
