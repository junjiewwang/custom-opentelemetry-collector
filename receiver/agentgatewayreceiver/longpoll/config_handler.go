// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// ConfigPollHandler implements LongPollHandler for configuration polling.
// It integrates with Nacos for config storage and change notification.
type ConfigPollHandler struct {
	logger      *zap.Logger
	nacosClient config_client.IConfigClient

	// Waiters management (per agent)
	waiters sync.Map // agentKey -> *ConfigWaiter

	// Watch management
	watching sync.Map // agentKey -> bool

	// State
	running atomic.Bool
}

// ConfigWaiter represents a waiting config poll request.
type ConfigWaiter struct {
	agentID    string
	token      string
	resultChan chan *HandlerResult
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewConfigPollHandler creates a new ConfigPollHandler.
func NewConfigPollHandler(logger *zap.Logger, nacosClient config_client.IConfigClient) *ConfigPollHandler {
	return &ConfigPollHandler{
		logger:      logger,
		nacosClient: nacosClient,
	}
}

// Ensure ConfigPollHandler implements LongPollHandler.
var _ LongPollHandler = (*ConfigPollHandler)(nil)

// GetType returns the handler type.
func (h *ConfigPollHandler) GetType() LongPollType {
	return LongPollTypeConfig
}

// Start initializes the handler.
func (h *ConfigPollHandler) Start(ctx context.Context) error {
	if h.running.Swap(true) {
		return nil
	}
	h.logger.Info("ConfigPollHandler started")
	return nil
}

// Stop stops the handler.
func (h *ConfigPollHandler) Stop() error {
	if !h.running.Swap(false) {
		return nil
	}

	// Cancel all waiters
	h.waiters.Range(func(key, value interface{}) bool {
		waiter := value.(*ConfigWaiter)
		if waiter.cancel != nil {
			waiter.cancel()
		}
		return true
	})

	// Cancel all watches
	h.watching.Range(func(key, value interface{}) bool {
		agentKey := key.(string)
		token, agentID := parseAgentKey(agentKey)
		h.cancelWatch(token, agentID)
		return true
	})

	h.logger.Info("ConfigPollHandler stopped")
	return nil
}

// ShouldContinue returns whether the handler should continue polling.
func (h *ConfigPollHandler) ShouldContinue() bool {
	return h.running.Load()
}

// CheckImmediate checks if there are config changes immediately.
func (h *ConfigPollHandler) CheckImmediate(ctx context.Context, req *PollRequest) (bool, *HandlerResult, error) {
	if h.nacosClient == nil {
		return false, nil, errors.New("nacos client not initialized")
	}

	// Load current config from Nacos
	config, err := h.loadOrCreateConfig(ctx, req.Token, req.AgentID)
	if err != nil {
		return false, nil, err
	}

	// Compute ETag
	currentEtag := ComputeEtag(config)

	// Check version change
	if req.CurrentConfigVersion != "" && req.CurrentConfigVersion != config.ConfigVersion {
		result := &HandlerResult{
			HasChanges: true,
			Response:   NewConfigResponse(true, config, config.ConfigVersion, currentEtag, "config version changed"),
		}
		return true, result, nil
	}

	// Check ETag change
	if req.CurrentConfigEtag != "" && req.CurrentConfigEtag != currentEtag {
		result := &HandlerResult{
			HasChanges: true,
			Response:   NewConfigResponse(true, config, config.ConfigVersion, currentEtag, "config content changed"),
		}
		return true, result, nil
	}

	// No changes
	return false, nil, nil
}

// Poll executes the long poll wait for config changes.
func (h *ConfigPollHandler) Poll(ctx context.Context, req *PollRequest) (*HandlerResult, error) {
	if h.nacosClient == nil {
		return nil, errors.New("nacos client not initialized")
	}

	agentKey := AgentKey(req.Token, req.AgentID)

	// Step 1: Check for immediate changes
	hasChanges, result, err := h.CheckImmediate(ctx, req)
	if err != nil {
		return nil, err
	}
	if hasChanges {
		return result, nil
	}

	// Step 2: No changes, register waiter and wait for notification
	waiterCtx, cancel := context.WithCancel(ctx)
	waiter := &ConfigWaiter{
		agentID:    req.AgentID,
		token:      req.Token,
		resultChan: make(chan *HandlerResult, 1),
		ctx:        waiterCtx,
		cancel:     cancel,
	}

	// Register waiter
	h.waiters.Store(agentKey, waiter)
	defer func() {
		h.waiters.Delete(agentKey)
		cancel()
	}()

	// Ensure Nacos watch is active
	h.ensureWatching(req.Token, req.AgentID)

	// Step 3: Wait for change notification or timeout
	select {
	case result := <-waiter.resultChan:
		return result, nil
	case <-ctx.Done():
		// Timeout - return no changes
		return &HandlerResult{
			HasChanges: false,
			Response:   NoChangeResponse(LongPollTypeConfig),
		}, nil
	}
}

// loadOrCreateConfig loads config from Nacos, creating default if not exists.
func (h *ConfigPollHandler) loadOrCreateConfig(ctx context.Context, token, agentID string) (*controlplanev1.AgentConfig, error) {
	// Try to load agent-specific config
	config, err := h.loadConfig(ctx, token, agentID)
	if err == nil && config != nil {
		return config, nil
	}

	// Try default config for the token
	config, err = h.loadConfig(ctx, token, "_default_")
	if err == nil && config != nil {
		return config, nil
	}

	// No config exists, create default and write to Nacos
	h.logger.Info("No config found, creating default",
		zap.String("token", token),
		zap.String("agent_id", agentID),
	)

	defaultConfig := GenerateDefaultConfig(agentID)

	// Write default config to Nacos (as default for the token)
	if err := h.saveConfig(ctx, token, "_default_", defaultConfig); err != nil {
		h.logger.Warn("Failed to save default config to Nacos",
			zap.String("token", token),
			zap.Error(err),
		)
		// Return the default config anyway
		return defaultConfig, nil
	}

	h.logger.Info("Created default config in Nacos",
		zap.String("token", token),
		zap.String("version", defaultConfig.ConfigVersion),
	)

	return defaultConfig, nil
}

// loadConfig loads config from Nacos.
func (h *ConfigPollHandler) loadConfig(ctx context.Context, group, dataID string) (*controlplanev1.AgentConfig, error) {
	type result struct {
		content string
		err     error
	}
	resultCh := make(chan result, 1)

	go func() {
		content, err := h.nacosClient.GetConfig(vo.ConfigParam{
			Group:  group,
			DataId: dataID,
		})
		resultCh <- result{content: content, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		if res.content == "" {
			return nil, errors.New("config not found")
		}

		var config controlplanev1.AgentConfig
		if err := json.Unmarshal([]byte(res.content), &config); err != nil {
			return nil, fmt.Errorf("failed to parse config: %w", err)
		}

		return &config, nil
	}
}

// saveConfig saves config to Nacos.
func (h *ConfigPollHandler) saveConfig(ctx context.Context, group, dataID string, config *controlplanev1.AgentConfig) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}

	type result struct {
		success bool
		err     error
	}
	resultCh := make(chan result, 1)

	go func() {
		success, err := h.nacosClient.PublishConfig(vo.ConfigParam{
			Group:   group,
			DataId:  dataID,
			Content: string(data),
			Type:    "json",
		})
		resultCh <- result{success: success, err: err}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-resultCh:
		if res.err != nil {
			return res.err
		}
		if !res.success {
			return errors.New("failed to publish config to Nacos")
		}
		return nil
	}
}

// ensureWatching ensures Nacos watch is active for the agent.
func (h *ConfigPollHandler) ensureWatching(token, agentID string) {
	// Watch agent-specific config
	h.setupWatch(token, agentID)

	// Also watch default config
	h.setupWatch(token, "_default_")
}

// setupWatch sets up Nacos config watch.
func (h *ConfigPollHandler) setupWatch(token, dataID string) {
	agentKey := AgentKey(token, dataID)

	// Check if already watching
	if _, ok := h.watching.Load(agentKey); ok {
		return
	}

	err := h.nacosClient.ListenConfig(vo.ConfigParam{
		Group:  token,
		DataId: dataID,
		OnChange: func(namespace, group, dataId, data string) {
			h.handleConfigChange(group, dataId, data)
		},
	})

	if err != nil {
		h.logger.Warn("Failed to setup config watch",
			zap.String("token", token),
			zap.String("data_id", dataID),
			zap.Error(err),
		)
		return
	}

	h.watching.Store(agentKey, true)
	h.logger.Debug("Setup config watch",
		zap.String("token", token),
		zap.String("data_id", dataID),
	)
}

// cancelWatch cancels Nacos config watch.
func (h *ConfigPollHandler) cancelWatch(token, dataID string) {
	agentKey := AgentKey(token, dataID)

	if _, ok := h.watching.Load(agentKey); !ok {
		return
	}

	err := h.nacosClient.CancelListenConfig(vo.ConfigParam{
		Group:  token,
		DataId: dataID,
	})

	if err != nil {
		h.logger.Warn("Failed to cancel config watch",
			zap.String("token", token),
			zap.String("data_id", dataID),
			zap.Error(err),
		)
	}

	h.watching.Delete(agentKey)
}

// handleConfigChange handles config change notification from Nacos.
func (h *ConfigPollHandler) handleConfigChange(token, dataID, data string) {
	h.logger.Info("Config changed in Nacos",
		zap.String("token", token),
		zap.String("data_id", dataID),
	)

	// Parse new config
	var newConfig *controlplanev1.AgentConfig
	if data != "" {
		var config controlplanev1.AgentConfig
		if err := json.Unmarshal([]byte(data), &config); err != nil {
			h.logger.Error("Failed to parse changed config",
				zap.String("token", token),
				zap.String("data_id", dataID),
				zap.Error(err),
			)
			return
		}
		newConfig = &config
	}

	// Notify waiting agents
	if dataID == "_default_" {
		// Default config changed - notify all waiters for this token
		h.notifyWaitersForToken(token, newConfig)
	} else {
		// Agent-specific config changed
		h.notifyWaiter(token, dataID, newConfig)
	}
}

// notifyWaiter notifies a specific waiter.
func (h *ConfigPollHandler) notifyWaiter(token, agentID string, config *controlplanev1.AgentConfig) {
	agentKey := AgentKey(token, agentID)

	if waiterVal, ok := h.waiters.Load(agentKey); ok {
		waiter := waiterVal.(*ConfigWaiter)

		var result *HandlerResult
		if config != nil {
			etag := ComputeEtag(config)
			result = &HandlerResult{
				HasChanges: true,
				Response:   NewConfigResponse(true, config, config.ConfigVersion, etag, "config updated"),
			}
		} else {
			result = &HandlerResult{
				HasChanges: true,
				Response:   NewConfigResponse(true, nil, "", "", "config deleted"),
			}
		}

		select {
		case waiter.resultChan <- result:
			h.logger.Debug("Notified waiter of config change",
				zap.String("token", token),
				zap.String("agent_id", agentID),
			)
		default:
			// Waiter already processed or timed out
		}
	}
}

// notifyWaitersForToken notifies all waiters for a token (default config change).
func (h *ConfigPollHandler) notifyWaitersForToken(token string, config *controlplanev1.AgentConfig) {
	h.waiters.Range(func(key, value interface{}) bool {
		waiter := value.(*ConfigWaiter)
		if waiter.token == token {
			var result *HandlerResult
			if config != nil {
				etag := ComputeEtag(config)
				result = &HandlerResult{
					HasChanges: true,
					Response:   NewConfigResponse(true, config, config.ConfigVersion, etag, "default config updated"),
				}
			} else {
				result = &HandlerResult{
					HasChanges: true,
					Response:   NewConfigResponse(true, nil, "", "", "default config deleted"),
				}
			}

			select {
			case waiter.resultChan <- result:
				h.logger.Debug("Notified waiter of default config change",
					zap.String("token", token),
					zap.String("agent_id", waiter.agentID),
				)
			default:
			}
		}
		return true
	})
}

// parseAgentKey parses an agent key into token and agentID.
func parseAgentKey(key string) (token, agentID string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ':' {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}

// GetWaiterCount returns the number of active waiters.
func (h *ConfigPollHandler) GetWaiterCount() int {
	count := 0
	h.waiters.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// GetWatchCount returns the number of active watches.
func (h *ConfigPollHandler) GetWatchCount() int {
	count := 0
	h.watching.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}
