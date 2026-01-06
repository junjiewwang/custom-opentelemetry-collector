// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package pending

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RedisPendingStore is a Redis-based implementation of PendingStore.
// It stores pending metadata in Redis for cross-node visibility,
// but actual tunnel delivery still happens locally.
type RedisPendingStore struct {
	logger    *zap.Logger
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration
	nodeID    string
	nodeAddr  string

	// Local store for actual tunnel delivery
	local *LocalPendingStore
}

// NewRedisPendingStore creates a new Redis-based pending store.
func NewRedisPendingStore(
	logger *zap.Logger,
	client redis.UniversalClient,
	keyPrefix string,
	ttl time.Duration,
	nodeID, nodeAddr string,
	local *LocalPendingStore,
) *RedisPendingStore {
	return &RedisPendingStore{
		logger:    logger,
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
		nodeID:    nodeID,
		nodeAddr:  nodeAddr,
		local:     local,
	}
}

func (s *RedisPendingStore) pendingKey(clientConnID string) string {
	return s.keyPrefix + "pending:" + clientConnID
}

// Create creates a new pending connection in both local and Redis.
// Uses Lua script for atomicity.
func (s *RedisPendingStore) Create(ctx context.Context, info *PendingInfo) error {
	// Ensure node info is set
	info.NodeID = s.nodeID
	info.NodeAddr = s.nodeAddr

	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal pending info: %w", err)
	}

	// Use SETNX to ensure atomicity
	key := s.pendingKey(info.ClientConnID)
	ok, err := s.client.SetNX(ctx, key, data, s.ttl).Result()
	if err != nil {
		return fmt.Errorf("create pending in redis: %w", err)
	}
	if !ok {
		return ErrPendingAlreadyExists
	}

	s.logger.Debug("Pending connection created in Redis",
		zap.String("client_conn_id", info.ClientConnID),
		zap.String("agent_id", info.AgentID),
		zap.Duration("ttl", s.ttl),
	)
	return nil
}

// CreateWithBrowserConn creates a pending with browser connection.
// Stores in both local (for tunnel delivery) and Redis (for cross-node lookup).
func (s *RedisPendingStore) CreateWithBrowserConn(ctx context.Context, info *PendingInfo, browserConn *websocket.Conn) error {
	// Create in local store first (for tunnel delivery)
	if err := s.local.CreateWithBrowserConn(ctx, info, browserConn); err != nil {
		return err
	}

	// Then create in Redis (for cross-node lookup)
	if err := s.Create(ctx, info); err != nil {
		// Rollback local
		_ = s.local.Delete(ctx, info.ClientConnID)
		return err
	}

	return nil
}

// Get retrieves pending info by client connection ID.
func (s *RedisPendingStore) Get(ctx context.Context, clientConnID string) (*PendingInfo, error) {
	// Check local first
	info, err := s.local.Get(ctx, clientConnID)
	if err != nil {
		return nil, err
	}
	if info != nil {
		return info, nil
	}

	// Check Redis
	data, err := s.client.Get(ctx, s.pendingKey(clientConnID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pending from redis: %w", err)
	}

	var pendingInfo PendingInfo
	if err := json.Unmarshal(data, &pendingInfo); err != nil {
		return nil, fmt.Errorf("unmarshal pending info: %w", err)
	}
	return &pendingInfo, nil
}

// Delete removes a pending connection from both local and Redis.
func (s *RedisPendingStore) Delete(ctx context.Context, clientConnID string) error {
	// Delete from local
	if err := s.local.Delete(ctx, clientConnID); err != nil {
		s.logger.Warn("Failed to delete pending from local",
			zap.String("client_conn_id", clientConnID),
			zap.Error(err),
		)
	}

	// Delete from Redis
	if err := s.client.Del(ctx, s.pendingKey(clientConnID)).Err(); err != nil {
		return fmt.Errorf("delete pending from redis: %w", err)
	}

	s.logger.Debug("Pending connection deleted from Redis",
		zap.String("client_conn_id", clientConnID),
	)
	return nil
}

// IsLocal returns true if the pending was created on this node.
func (s *RedisPendingStore) IsLocal(clientConnID string) bool {
	return s.local.IsLocal(clientConnID)
}

// DeliverTunnel delivers a tunnel connection to a local pending.
func (s *RedisPendingStore) DeliverTunnel(clientConnID string, conn *websocket.Conn) error {
	return s.local.DeliverTunnel(clientConnID, conn)
}

// WaitForTunnel waits for a tunnel connection to be delivered.
func (s *RedisPendingStore) WaitForTunnel(ctx context.Context, clientConnID string, timeout time.Duration) (*websocket.Conn, error) {
	return s.local.WaitForTunnel(ctx, clientConnID, timeout)
}

// GetBrowserConn returns the browser connection for a local pending.
func (s *RedisPendingStore) GetBrowserConn(clientConnID string) *websocket.Conn {
	return s.local.GetBrowserConn(clientConnID)
}

// Close releases resources.
func (s *RedisPendingStore) Close() error {
	return s.local.Close()
}

// ClaimPending attempts to claim a pending connection.
// This is used when openTunnel arrives at a different node.
// Uses Lua script for atomicity.
func (s *RedisPendingStore) ClaimPending(ctx context.Context, clientConnID string) (*PendingInfo, error) {
	// Lua script: get and delete atomically
	script := redis.NewScript(`
		local key = KEYS[1]
		local data = redis.call('GET', key)
		if data then
			redis.call('DEL', key)
			return data
		end
		return nil
	`)

	result, err := script.Run(ctx, s.client, []string{s.pendingKey(clientConnID)}).Result()
	if err == redis.Nil || result == nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim pending from redis: %w", err)
	}

	data, ok := result.(string)
	if !ok {
		return nil, fmt.Errorf("unexpected result type: %T", result)
	}

	var info PendingInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, fmt.Errorf("unmarshal pending info: %w", err)
	}

	s.logger.Debug("Pending connection claimed from Redis",
		zap.String("client_conn_id", clientConnID),
		zap.String("original_node", info.NodeID),
	)
	return &info, nil
}

// GetNodeAddr returns the node address for a pending connection.
func (s *RedisPendingStore) GetNodeAddr(ctx context.Context, clientConnID string) (string, error) {
	info, err := s.Get(ctx, clientConnID)
	if err != nil {
		return "", err
	}
	if info == nil {
		return "", nil
	}
	return info.NodeAddr, nil
}

// GetLocalStore returns the underlying local store.
func (s *RedisPendingStore) GetLocalStore() *LocalPendingStore {
	return s.local
}
