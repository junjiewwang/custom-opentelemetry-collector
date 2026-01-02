// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// DefaultMaxTimeout is the maximum allowed timeout for long polling.
	DefaultMaxTimeout = 60 * time.Second
	// DefaultTimeout is the default timeout if not specified.
	DefaultTimeout = 30 * time.Second
	// MinTimeout is the minimum allowed timeout.
	MinTimeout = 1 * time.Second
)

// ManagerConfig holds configuration for LongPollManager.
type ManagerConfig struct {
	MaxTimeout     time.Duration `mapstructure:"max_timeout"`
	DefaultTimeout time.Duration `mapstructure:"default_timeout"`
}

// DefaultManagerConfig returns the default configuration.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		MaxTimeout:     DefaultMaxTimeout,
		DefaultTimeout: DefaultTimeout,
	}
}

// Manager manages long poll handlers and coordinates polling.
// This follows the Selector pattern similar to Java NIO Selector.
type Manager struct {
	logger   *zap.Logger
	config   ManagerConfig
	handlers map[LongPollType]LongPollHandler
	mu       sync.RWMutex
	started  bool
}

// NewManager creates a new LongPollManager.
func NewManager(logger *zap.Logger, config ManagerConfig) *Manager {
	if config.MaxTimeout <= 0 {
		config.MaxTimeout = DefaultMaxTimeout
	}
	if config.DefaultTimeout <= 0 {
		config.DefaultTimeout = DefaultTimeout
	}

	return &Manager{
		logger:   logger,
		config:   config,
		handlers: make(map[LongPollType]LongPollHandler),
	}
}

// RegisterHandler registers a poll handler (Open-Closed Principle).
func (m *Manager) RegisterHandler(handler LongPollHandler) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pollType := handler.GetType()
	if _, exists := m.handlers[pollType]; exists {
		m.logger.Warn("Replacing existing handler", zap.String("type", string(pollType)))
	}

	m.handlers[pollType] = handler
	m.logger.Info("Registered long poll handler", zap.String("type", string(pollType)))

	return nil
}

// UnregisterHandler removes a poll handler.
func (m *Manager) UnregisterHandler(pollType LongPollType) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.handlers[pollType]; exists {
		delete(m.handlers, pollType)
		m.logger.Info("Unregistered long poll handler", zap.String("type", string(pollType)))
		return true
	}
	return false
}

// GetHandler returns a handler by type.
func (m *Manager) GetHandler(pollType LongPollType) (LongPollHandler, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	handler, ok := m.handlers[pollType]
	return handler, ok
}

// Start initializes all registered handlers.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	for pollType, handler := range m.handlers {
		if err := handler.Start(ctx); err != nil {
			m.logger.Error("Failed to start handler",
				zap.String("type", string(pollType)),
				zap.Error(err))
			return fmt.Errorf("failed to start handler %s: %w", pollType, err)
		}
	}

	m.started = true
	m.logger.Info("LongPollManager started", zap.Int("handlers", len(m.handlers)))
	return nil
}

// Stop stops all registered handlers.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return nil
	}

	var errs []error
	for pollType, handler := range m.handlers {
		if err := handler.Stop(); err != nil {
			m.logger.Error("Failed to stop handler",
				zap.String("type", string(pollType)),
				zap.Error(err))
			errs = append(errs, err)
		}
	}

	m.started = false
	m.logger.Info("LongPollManager stopped")

	if len(errs) > 0 {
		return fmt.Errorf("errors stopping handlers: %v", errs)
	}
	return nil
}

// Poll executes long polling for specified types (or all if none specified).
// This is the main entry point for long poll requests.
func (m *Manager) Poll(ctx context.Context, req *PollRequest, types ...LongPollType) (*CombinedPollResponse, error) {
	// Normalize timeout
	timeout := m.normalizeTimeout(req.TimeoutMillis)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Get handlers to poll
	handlers := m.getHandlersToPoll(types)
	if len(handlers) == 0 {
		return &CombinedPollResponse{
			HasAnyChanges: false,
			Message:       "no handlers registered",
		}, nil
	}

	// Execute all handlers in parallel
	return m.executePoll(ctx, req, handlers)
}

// PollSingle executes long polling for a single type.
func (m *Manager) PollSingle(ctx context.Context, req *PollRequest, pollType LongPollType) (*PollResponse, error) {
	handler, ok := m.GetHandler(pollType)
	if !ok {
		return nil, fmt.Errorf("handler not found: %s", pollType)
	}

	// Normalize timeout
	timeout := m.normalizeTimeout(req.TimeoutMillis)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := handler.Poll(ctx, req)
	if err != nil {
		return nil, err
	}

	if result == nil {
		return NoChangeResponse(pollType), nil
	}

	return result.Response, nil
}

// normalizeTimeout ensures timeout is within allowed bounds.
func (m *Manager) normalizeTimeout(timeoutMillis int64) time.Duration {
	timeout := time.Duration(timeoutMillis) * time.Millisecond

	if timeout <= 0 {
		return m.config.DefaultTimeout
	}
	if timeout < MinTimeout {
		return MinTimeout
	}
	if timeout > m.config.MaxTimeout {
		return m.config.MaxTimeout
	}
	return timeout
}

// getHandlersToPoll returns handlers for the specified types.
func (m *Manager) getHandlersToPoll(types []LongPollType) []LongPollHandler {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(types) == 0 {
		// Poll all registered handlers
		handlers := make([]LongPollHandler, 0, len(m.handlers))
		for _, h := range m.handlers {
			handlers = append(handlers, h)
		}
		return handlers
	}

	// Poll specified handlers
	handlers := make([]LongPollHandler, 0, len(types))
	for _, t := range types {
		if h, ok := m.handlers[t]; ok {
			handlers = append(handlers, h)
		}
	}
	return handlers
}

// pollResult holds the result from a single handler.
type pollResult struct {
	pollType LongPollType
	result   *HandlerResult
	err      error
}

// executePoll executes polling for multiple handlers in parallel.
func (m *Manager) executePoll(ctx context.Context, req *PollRequest, handlers []LongPollHandler) (*CombinedPollResponse, error) {
	resultCh := make(chan *pollResult, len(handlers))

	// Launch all handlers in parallel
	for _, handler := range handlers {
		go func(h LongPollHandler) {
			result, err := h.Poll(ctx, req)
			resultCh <- &pollResult{
				pollType: h.GetType(),
				result:   result,
				err:      err,
			}
		}(handler)
	}

	// Collect results
	response := &CombinedPollResponse{
		Results: make(map[LongPollType]*PollResponse),
	}

	// Wait for all handlers or context cancellation
	for i := 0; i < len(handlers); i++ {
		select {
		case r := <-resultCh:
			if r.err != nil {
				m.logger.Warn("Poll handler error",
					zap.String("type", string(r.pollType)),
					zap.Error(r.err))
				continue
			}

			if r.result != nil && r.result.Response != nil {
				response.Results[r.pollType] = r.result.Response
				if r.result.HasChanges {
					response.HasAnyChanges = true
				}
			}

		case <-ctx.Done():
			// Context cancelled or timeout - collect remaining results
			m.collectRemainingResults(resultCh, response, len(handlers)-i-1)
			return response, nil
		}
	}

	return response, nil
}

// collectRemainingResults collects any remaining results after context cancellation.
func (m *Manager) collectRemainingResults(resultCh <-chan *pollResult, response *CombinedPollResponse, remaining int) {
	for i := 0; i < remaining; i++ {
		select {
		case r := <-resultCh:
			if r.err == nil && r.result != nil && r.result.Response != nil {
				response.Results[r.pollType] = r.result.Response
				if r.result.HasChanges {
					response.HasAnyChanges = true
				}
			}
		default:
			return
		}
	}
}

// GetRegisteredTypes returns all registered handler types.
func (m *Manager) GetRegisteredTypes() []LongPollType {
	m.mu.RLock()
	defer m.mu.RUnlock()

	types := make([]LongPollType, 0, len(m.handlers))
	for t := range m.handlers {
		types = append(types, t)
	}
	return types
}
