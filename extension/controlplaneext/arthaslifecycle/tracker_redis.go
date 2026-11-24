// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthaslifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type redisTracker struct {
	client  redis.UniversalClient
	prefix  string
	idleKey string
}

// NewRedisTracker returns a Tracker whose state is shared between replicas.
func NewRedisTracker(client redis.UniversalClient, prefix string) Tracker {
	if prefix == "" {
		prefix = "otel:arthas:lifecycle"
	}
	return &redisTracker{
		client:  client,
		prefix:  prefix,
		idleKey: prefix + ":idle",
	}
}

func (t *redisTracker) key(agentID string) string {
	return t.prefix + ":agent:" + agentID
}

func (t *redisTracker) lockKey(agentID string) string {
	return t.prefix + ":detach-lock:" + agentID
}

func (t *redisTracker) load(ctx context.Context, agentID string) (State, error) {
	b, err := t.client.Get(ctx, t.key(agentID)).Bytes()
	if err == redis.Nil {
		return State{AgentID: agentID, Runtime: RuntimeUnknown}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, err
	}
	if st.AgentID == "" {
		st.AgentID = agentID
	}
	if st.Runtime == "" {
		st.Runtime = RuntimeUnknown
	}
	return st, nil
}

func (t *redisTracker) save(ctx context.Context, st State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	pipe := t.client.Pipeline()
	pipe.Set(ctx, t.key(st.AgentID), b, 0)
	if st.LastActivityAt > 0 {
		pipe.ZAdd(ctx, t.idleKey, redis.Z{
			Score:  float64(st.LastActivityAt),
			Member: st.AgentID,
		})
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (t *redisTracker) Touch(ctx context.Context, agentID, source string, now int64) error {
	st, err := t.load(ctx, agentID)
	if err != nil {
		return err
	}
	st.LastActivityAt = now
	st.LastActivitySource = source
	return t.save(ctx, st)
}

func (t *redisTracker) SetRuntime(ctx context.Context, agentID string, runtime RuntimeState, now int64) error {
	st, err := t.load(ctx, agentID)
	if err != nil {
		return err
	}
	st.Runtime = runtime
	st.RuntimeObservedAt = now
	if runtime == RuntimeRunning && st.AttachedAt == 0 {
		st.AttachedAt = now
	}
	if runtime == RuntimeStopped {
		st.AttachedAt = 0
		st.ActiveSessions = 0
	}
	return t.save(ctx, st)
}

func (t *redisTracker) ChangeSessions(ctx context.Context, agentID string, delta int, now int64) error {
	st, err := t.load(ctx, agentID)
	if err != nil {
		return err
	}
	st.ActiveSessions += delta
	if st.ActiveSessions < 0 {
		st.ActiveSessions = 0
	}
	st.LastActivityAt = now
	st.LastActivitySource = "session"
	return t.save(ctx, st)
}

func (t *redisTracker) Reset(ctx context.Context, agentID string, now int64) error {
	st, err := t.load(ctx, agentID)
	if err != nil {
		return err
	}
	st.Runtime = RuntimeUnknown
	st.RuntimeObservedAt = now
	st.AttachedAt = 0
	st.ActiveSessions = 0
	st.LastActivityAt = now
	st.LastActivitySource = "agent_re_register"
	return t.save(ctx, st)
}

func (t *redisTracker) Get(ctx context.Context, agentID string) (State, bool, error) {
	b, err := t.client.Get(ctx, t.key(agentID)).Bytes()
	if err == redis.Nil {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, false, err
	}
	return st, true, nil
}

func (t *redisTracker) ListIdle(ctx context.Context, now, idleThresholdMillis int64, limit int) ([]State, error) {
	maxScore := strconv.FormatInt(now-idleThresholdMillis, 10)
	count := int64(limit)
	if count <= 0 {
		count = 200
	}
	ids, err := t.client.ZRangeByScore(ctx, t.idleKey, &redis.ZRangeBy{
		Min:   "0",
		Max:   maxScore,
		Count: count,
	}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]State, 0, len(ids))
	for _, agentID := range ids {
		st, err := t.load(ctx, agentID)
		if err != nil {
			return nil, err
		}
		if st.Runtime != RuntimeRunning || st.ActiveSessions > 0 || st.LastActivityAt <= 0 {
			continue
		}
		if now-st.LastActivityAt < idleThresholdMillis {
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

func (t *redisTracker) TryAcquireDetach(ctx context.Context, agentID string, cooldownMillis, now int64) (bool, error) {
	ttl := time.Duration(cooldownMillis) * time.Millisecond
	if ttl <= 0 {
		ttl = time.Minute
	}
	ok, err := t.client.SetNX(ctx, t.lockKey(agentID), fmt.Sprintf("%d", now), ttl).Result()
	if err != nil || !ok {
		return false, err
	}
	st, err := t.load(ctx, agentID)
	if err != nil {
		return false, err
	}
	st.LastDetachSubmittedAt = now
	return true, t.save(ctx, st)
}
