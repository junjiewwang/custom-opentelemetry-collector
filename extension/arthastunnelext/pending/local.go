// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package pending

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

var (
	// ErrPendingNotFound is returned when a pending connection is not found.
	ErrPendingNotFound = errors.New("pending connection not found")
	// ErrPendingAlreadyExists is returned when trying to create a duplicate pending.
	ErrPendingAlreadyExists = errors.New("pending connection already exists")
	// ErrTunnelAlreadyDelivered is returned when trying to deliver a tunnel twice.
	ErrTunnelAlreadyDelivered = errors.New("tunnel already delivered")
	// ErrNotLocalPending is returned when trying to operate on a non-local pending.
	ErrNotLocalPending = errors.New("pending is not on this node")
)

// localPending represents a local pending connection with its channels.
type localPending struct {
	info        *PendingInfo
	browserConn *websocket.Conn
	tunnelCh    chan *websocket.Conn
	closeOnce   sync.Once
}

// LocalPendingStore is an in-memory implementation of PendingStore.
type LocalPendingStore struct {
	logger   *zap.Logger
	nodeID   string
	nodeAddr string

	mu       sync.RWMutex
	pendings map[string]*localPending
}

// NewLocalPendingStore creates a new local pending store.
func NewLocalPendingStore(logger *zap.Logger, nodeID, nodeAddr string) *LocalPendingStore {
	return &LocalPendingStore{
		logger:   logger,
		nodeID:   nodeID,
		nodeAddr: nodeAddr,
		pendings: make(map[string]*localPending),
	}
}

// Create creates a new pending connection.
func (s *LocalPendingStore) Create(_ context.Context, info *PendingInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.pendings[info.ClientConnID]; exists {
		return ErrPendingAlreadyExists
	}

	// Ensure node info is set
	info.NodeID = s.nodeID
	info.NodeAddr = s.nodeAddr

	s.pendings[info.ClientConnID] = &localPending{
		info:     info,
		tunnelCh: make(chan *websocket.Conn, 1),
	}

	s.logger.Debug("Pending connection created locally",
		zap.String("client_conn_id", info.ClientConnID),
		zap.String("agent_id", info.AgentID),
	)
	return nil
}

// CreateWithBrowserConn creates a pending with the browser connection.
func (s *LocalPendingStore) CreateWithBrowserConn(ctx context.Context, info *PendingInfo, browserConn *websocket.Conn) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.pendings[info.ClientConnID]; exists {
		return ErrPendingAlreadyExists
	}

	// Ensure node info is set
	info.NodeID = s.nodeID
	info.NodeAddr = s.nodeAddr

	s.pendings[info.ClientConnID] = &localPending{
		info:        info,
		browserConn: browserConn,
		tunnelCh:    make(chan *websocket.Conn, 1),
	}

	s.logger.Debug("Pending connection created locally with browser conn",
		zap.String("client_conn_id", info.ClientConnID),
		zap.String("agent_id", info.AgentID),
	)
	return nil
}

// Get retrieves pending info by client connection ID.
func (s *LocalPendingStore) Get(_ context.Context, clientConnID string) (*PendingInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.pendings[clientConnID]
	if !ok {
		return nil, nil
	}
	// Return a copy
	copied := *p.info
	return &copied, nil
}

// Delete removes a pending connection.
func (s *LocalPendingStore) Delete(_ context.Context, clientConnID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if p, ok := s.pendings[clientConnID]; ok {
		p.closeOnce.Do(func() {
			close(p.tunnelCh)
		})
		delete(s.pendings, clientConnID)
		s.logger.Debug("Pending connection deleted locally",
			zap.String("client_conn_id", clientConnID),
		)
	}
	return nil
}

// IsLocal returns true if the pending was created on this node.
func (s *LocalPendingStore) IsLocal(clientConnID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.pendings[clientConnID]
	return ok
}

// DeliverTunnel delivers a tunnel connection to a local pending.
func (s *LocalPendingStore) DeliverTunnel(clientConnID string, conn *websocket.Conn) error {
	s.mu.RLock()
	p, ok := s.pendings[clientConnID]
	s.mu.RUnlock()

	if !ok {
		return ErrPendingNotFound
	}

	select {
	case p.tunnelCh <- conn:
		s.logger.Debug("Tunnel delivered to pending",
			zap.String("client_conn_id", clientConnID),
		)
		return nil
	default:
		return ErrTunnelAlreadyDelivered
	}
}

// WaitForTunnel waits for a tunnel connection to be delivered.
func (s *LocalPendingStore) WaitForTunnel(ctx context.Context, clientConnID string, timeout time.Duration) (*websocket.Conn, error) {
	s.mu.RLock()
	p, ok := s.pendings[clientConnID]
	s.mu.RUnlock()

	if !ok {
		return nil, ErrPendingNotFound
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, context.DeadlineExceeded
	case conn := <-p.tunnelCh:
		return conn, nil
	}
}

// GetBrowserConn returns the browser connection for a local pending.
func (s *LocalPendingStore) GetBrowserConn(clientConnID string) *websocket.Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if p, ok := s.pendings[clientConnID]; ok {
		return p.browserConn
	}
	return nil
}

// Close releases resources.
func (s *LocalPendingStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, p := range s.pendings {
		p.closeOnce.Do(func() {
			close(p.tunnelCh)
		})
		delete(s.pendings, id)
	}
	return nil
}

// Count returns the number of pending connections.
func (s *LocalPendingStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pendings)
}

// ListIDs returns all pending connection IDs.
func (s *LocalPendingStore) ListIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.pendings))
	for id := range s.pendings {
		ids = append(ids, id)
	}
	return ids
}
