// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package opsconfig stores admin-managed, global runtime configuration and
// makes it available to config-poll metadata providers on every replica.
package opsconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	// KeyTunnelBaseURL is a partial override (scheme + host[:port] + optional
	// path prefix). The probe appends /v1/arthas/ws to it. It covers topologies
	// where the control plane gRPC/HTTP hosts differ, without forcing the
	// operator to hand-write a full WebSocket URL.
	KeyTunnelBaseURL = "ops.arthas.tunnel_base_url"

	DefaultKeyPrefix = "otel:ops:config"
	DefaultChannel   = "otel:ops:config:changed"

	expectedTunnelPath = "/v1/arthas/ws"
	maxValueLength     = 512
	maxAuditEntries    = 100
)

// ChangeLog records one admin configuration change. It never holds tokens.
type ChangeLog struct {
	Key       string `json:"key"`
	OldValue  string `json:"old_value,omitempty"`
	NewValue  string `json:"new_value,omitempty"`
	Operator  string `json:"operator,omitempty"`
	Revision  int64  `json:"revision"`
	Timestamp int64  `json:"timestamp"`
}

// Store is a Redis-backed, multi-replica-safe runtime config store.
type Store struct {
	client      redis.UniversalClient
	hashKey     string
	revisionKey string
	auditKey    string
	channel     string
	logger      *zap.Logger

	mu       sync.RWMutex
	values   map[string]string
	revision int64
	subs     []chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}
	once   sync.Once
}

// NewStore creates a Store. redisNilClient may be nil; callers should degrade
// gracefully when Redis is unavailable.
func NewStore(client redis.UniversalClient, logger *zap.Logger, prefix string) *Store {
	if prefix == "" {
		prefix = DefaultKeyPrefix
	}
	return &Store{
		client:      client,
		hashKey:     prefix + ":values",
		revisionKey: prefix + ":revision",
		auditKey:    prefix + ":audit",
		channel:     prefix + ":changed",
		logger:      logger,
		values:      make(map[string]string),
		done:        make(chan struct{}),
	}
}

// Done is closed when the store stops; subscribers should use it to exit.
func (s *Store) Done() <-chan struct{} { return s.done }

// Start loads the current snapshot and subscribes to cross-replica changes.
// The listener is deliberately rooted at context.Background(): the collector
// startup context may be cancelled after Start returns, which would silently
// kill the subscription. Shutdown is explicit via Stop().
func (s *Store) Start() error {
	if s.client == nil {
		return errors.New("opsconfig: redis client is nil")
	}
	if err := s.refresh(context.Background()); err != nil {
		return err
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	sub := s.client.Subscribe(s.ctx, s.channel)
	if _, err := sub.Receive(s.ctx); err != nil {
		s.cancel()
		return fmt.Errorf("opsconfig: subscribe: %w", err)
	}
	s.wg.Add(1)
	go s.listen(sub)
	return nil
}

// Stop terminates the pub/sub listener.
func (s *Store) Stop() {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.done) })
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

func (s *Store) listen(sub *redis.PubSub) {
	defer s.wg.Done()
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-s.ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			if err := s.refresh(context.Background()); err != nil {
				s.logger.Warn("opsconfig refresh failed", zap.Error(err))
				continue
			}
			s.broadcast()
		}
	}
}

func (s *Store) refresh(ctx context.Context) error {
	values, err := s.client.HGetAll(ctx, s.hashKey).Result()
	if err != nil {
		return fmt.Errorf("opsconfig: read values: %w", err)
	}
	raw, err := s.client.Get(ctx, s.revisionKey).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("opsconfig: read revision: %w", err)
	}
	var revision int64
	if raw != "" {
		revision, _ = strconv.ParseInt(raw, 10, 64)
	}
	s.mu.Lock()
	s.values = values
	s.revision = revision
	s.mu.Unlock()
	return nil
}

// Get returns the current value of a key.
func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

// Snapshot returns a copy of all values and the current revision.
func (s *Store) Snapshot() (map[string]string, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out, s.revision
}

// Revision returns the current global revision.
func (s *Store) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

// RevisionTag is a stable string used in config ETags.
func (s *Store) RevisionTag() string {
	return "ops-" + strconv.FormatInt(s.Revision(), 10)
}

// SubscribeChanges returns a channel that receives a signal on every change.
func (s *Store) SubscribeChanges() <-chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs = append(s.subs, ch)
	s.mu.Unlock()
	return ch
}

func (s *Store) broadcast() {
	s.mu.RLock()
	subs := append([]chan struct{}(nil), s.subs...)
	s.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Put validates and stores a config value, atomically increments the revision,
// records a change log entry and notifies all replicas.
func (s *Store) Put(ctx context.Context, key, value, operator string) (int64, error) {
	if err := Validate(key, value); err != nil {
		return 0, err
	}
	old, _ := s.Get(key)
	entry := ChangeLog{
		Key: key, OldValue: old, NewValue: value,
		Operator: operator, Timestamp: time.Now().UnixMilli(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return 0, err
	}
	rev, err := s.write(ctx, key, value, false, payload)
	if err == nil {
		s.refreshAndBroadcast(ctx)
	}
	return rev, err
}

// Delete removes a config value, reverting agents to their derived endpoint.
func (s *Store) Delete(ctx context.Context, key, operator string) (int64, error) {
	// Deletion only requires that the key is registered in the schema.
	if !schemaHasKey(key) {
		return 0, fmt.Errorf("%w: unsupported key %q", ErrValidation, key)
	}
	old, _ := s.Get(key)
	entry := ChangeLog{
		Key: key, OldValue: old, Operator: operator,
		Timestamp: time.Now().UnixMilli(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return 0, err
	}
	rev, err := s.write(ctx, key, "", true, payload)
	if err == nil {
		s.refreshAndBroadcast(ctx)
	}
	return rev, err
}

const writeScript = `
local rev = redis.call('INCR', KEYS[2])
if ARGV[4] == '1' then
  redis.call('HDEL', KEYS[1], ARGV[1])
else
  redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
end
local audit = string.gsub(ARGV[3], '@REV@', tostring(rev))
redis.call('LPUSH', KEYS[3], audit)
redis.call('LTRIM', KEYS[3], 0, ARGV[5])
redis.call('PUBLISH', KEYS[4], tostring(rev))
return rev
`

func (s *Store) write(ctx context.Context, key, value string, delete bool, audit []byte) (int64, error) {
	delFlag := "0"
	if delete {
		delFlag = "1"
	}
	auditStr := strings.ReplaceAll(string(audit), `"revision":0`, `"revision":@REV@`)
	res, err := s.client.Eval(ctx, writeScript,
		[]string{s.hashKey, s.revisionKey, s.auditKey, s.channel},
		key, value, auditStr, delFlag, maxAuditEntries,
	).Result()
	if err != nil {
		return 0, fmt.Errorf("opsconfig: write: %w", err)
	}
	rev, ok := res.(int64)
	if !ok {
		return 0, errors.New("opsconfig: unexpected revision type")
	}
	return rev, nil
}

func (s *Store) refreshAndBroadcast(ctx context.Context) {
	if err := s.refresh(ctx); err != nil {
		s.logger.Warn("opsconfig refresh after write failed", zap.Error(err))
	}
	s.broadcast()
}

// Audit returns recent change log entries (newest first).
func (s *Store) Audit(ctx context.Context, count int64) ([]ChangeLog, error) {
	if count <= 0 || count > maxAuditEntries {
		count = 20
	}
	rows, err := s.client.LRange(ctx, s.auditKey, 0, count-1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]ChangeLog, 0, len(rows))
	for _, row := range rows {
		var entry ChangeLog
		if err := json.Unmarshal([]byte(row), &entry); err == nil {
			out = append(out, entry)
		}
	}
	return out, nil
}
