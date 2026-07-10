// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
)

// serviceStateIdleGrace is how long a serviceState survives without waiters
// before being removed from the map. Set long enough to cover the gap between
// consecutive long polls (~ms), short enough to avoid unbounded memory growth.
// The timeout resets on every poll since a new timer is created each time
// the last waiter exits.
const serviceStateIdleGrace = 2 * time.Minute


// ConfigPollHandler implements LongPollHandler for configuration polling.
// It integrates with OnDemandConfigManager for config reading and change watching.
// It uses ServiceName as the DataID for configuration.
type ConfigPollHandler struct {
	logger    *zap.Logger
	configMgr configmanager.OnDemandConfigManager

	// services maps serviceKey (appID:serviceName) -> *serviceState
	services sync.Map

	// metadataProviders is a list of providers for dynamic config injection
	metadataProviders []ServerMetadataProvider
	providersMu       sync.RWMutex

	// State
	running atomic.Bool
}

// serviceState groups config lifecycle and waiter management for a single service.
// Both components (configCache and WaiterMap) are independently thread-safe —
// serviceState itself holds no locks.
type serviceState struct {
	config  *configCache
	waiters WaiterMap[ConfigWaiter]
}

// ConfigWaiter represents a waiting config poll request.
type ConfigWaiter struct {
	agentID     string
	appID       string
	serviceName string
	resultChan  chan *HandlerResult
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewConfigPollHandler creates a new ConfigPollHandler.
func NewConfigPollHandler(logger *zap.Logger, configMgr configmanager.OnDemandConfigManager) *ConfigPollHandler {
	return &ConfigPollHandler{
		logger:            logger,
		configMgr:         configMgr,
		metadataProviders: make([]ServerMetadataProvider, 0),
	}
}

// RegisterMetadataProvider registers a new metadata provider.
func (h *ConfigPollHandler) RegisterMetadataProvider(p ServerMetadataProvider) {
	h.providersMu.Lock()
	defer h.providersMu.Unlock()
	h.metadataProviders = append(h.metadataProviders, p)
	h.logger.Debug("Registered metadata provider", zap.String("name", p.Name()))
}

// injectMetadata injects dynamic metadata from all registered providers.
func (h *ConfigPollHandler) injectMetadata(ctx context.Context, req *PollRequest, config *model.AgentConfig) {
	if config == nil {
		return
	}

	h.providersMu.RLock()
	defer h.providersMu.RUnlock()

	if len(h.metadataProviders) == 0 {
		return
	}

	if config.ServerMetadata == nil {
		config.ServerMetadata = make(map[string]string)
	}

	for _, p := range h.metadataProviders {
		meta := p.ProvideMetadata(ctx, req)
		for k, v := range meta {
			config.ServerMetadata[k] = v
		}
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

// Stop stops the handler and releases all resources (waiters, watches, service states).
func (h *ConfigPollHandler) Stop() error {
	if !h.running.Swap(false) {
		return nil
	}

	h.services.Range(func(key, value interface{}) bool {
		state := value.(*serviceState)

		// Cancel and clear all waiters
		state.waiters.Clear(func(w *ConfigWaiter) {
			if w.cancel != nil {
				w.cancel()
			}
		})

		// Unwatch Nacos
		state.config.Unwatch()

		// Remove from services map
		h.services.Delete(key)
		return true
	})

	h.logger.Info("ConfigPollHandler stopped")
	return nil
}

// cleanupIdleServiceState unwatches Nacos when the last waiter exits and
// schedules deferred deletion of the serviceState after a grace period.
//
// Design rationale:
//   - Long poll creates a waiter on each request and deregisters it on return.
//     Between consecutive polls (~ms gap), waiters=0 but the agent is still active.
//   - Immediate deletion would destroy the config cache, forcing a Nacos query on
//     every poll cycle (log spam).
//   - Deferred deletion via time.AfterFunc bridges the poll gap: if a new poll
//     arrives before the grace period, the timer sees waiters>0 and no-ops.
//   - If the agent truly disconnects, the timer fires and removes the state,
//     preventing memory leaks.
func (h *ConfigPollHandler) cleanupIdleServiceState(state *serviceState) {
	// Double-check: a new Poll may have registered a waiter between our check and now
	if !state.waiters.IsEmpty() {
		return
	}

	// Unwatch Nacos to conserve resources while idle.
	// Watching will be restarted by EnsureWatching on the next poll.
	state.config.Unwatch()

	// Schedule deferred deletion. If a new poll registers a waiter within the grace
	// period, the timer will see waiters>0 (or a different serviceState) and skip.
	serviceKey := AgentKey(state.config.appID, state.config.serviceName)
	time.AfterFunc(serviceStateIdleGrace, func() {
		if !state.waiters.IsEmpty() {
			return // new poll arrived, keep state
		}
		if val, loaded := h.services.LoadAndDelete(serviceKey); loaded && val == state {
			h.logger.Debug("Cleaned up idle service state after grace period",
				zap.String("app_id", state.config.appID),
				zap.String("service", state.config.serviceName),
			)
		}
	})
}

// ShouldContinue returns whether the handler should continue polling.
func (h *ConfigPollHandler) ShouldContinue() bool {
	return h.running.Load()
}

// getOrCreateServiceState gets or creates a serviceState for the given appID and serviceName.
// The first caller that creates the state wires the onChange callback for waiter notification.
func (h *ConfigPollHandler) getOrCreateServiceState(appID, serviceName string) *serviceState {
	serviceKey := AgentKey(appID, serviceName)
	actual, loaded := h.services.LoadOrStore(serviceKey, &serviceState{
		config: newConfigCache(appID, serviceName, h.configMgr, h.logger),
	})
	s := actual.(*serviceState)
	if !loaded {
		// We won the race — wire the config change → waiter notification callback
		s.config.SetOnChange(func(newConfig *model.AgentConfig) {
			h.notifyServiceWaiters(s, newConfig)
		})
	}
	return s
}

// CheckImmediate checks if there are config changes immediately.
func (h *ConfigPollHandler) CheckImmediate(ctx context.Context, req *PollRequest) (bool, *HandlerResult, error) {
	if h.configMgr == nil {
		return false, nil, errors.New("config manager not initialized")
	}

	state := h.getOrCreateServiceState(req.AppID, req.ServiceName)

	// 1. Get base config (cached or from Nacos)
	config := state.config.Get()

	// If no config in cache, or the client reports a matching version but lacks
	// an ETag (can't confirm staleness), do a proactive check against Nacos.
	// When both version AND ETag match, the agent definitely has the latest config —
	// Nacos watch notifications handle further updates via the normal path.
	if config == nil || (req.CurrentConfigVersion == config.Version && req.CurrentConfigEtag == "") {
		freshConfig, err := state.config.LoadFromConfigMgr(ctx)
		if err == nil && freshConfig != nil {
			// Check if it's actually newer than what we had
			if config == nil || freshConfig.Version != config.Version || freshConfig.Etag != config.Etag {
				if config == nil {
					// First load for this service state — expected on startup or after idle cleanup.
					// Use Debug level to avoid log spam in multi-replica or frequent-poll scenarios.
					h.logger.Debug("Loaded config from Nacos (first load for this state)",
						zap.String("service", req.ServiceName),
						zap.String("version", freshConfig.Version))
				} else {
					// Genuine config update detected — worth Info-level visibility.
					h.logger.Info("Found updated config in Nacos during immediate check",
						zap.String("service", req.ServiceName),
						zap.String("old_version", config.Version),
						zap.String("new_version", freshConfig.Version))
				}

				state.config.Set(freshConfig)
				config = freshConfig
			}
		} else if err != nil && config == nil {
			h.logger.Debug("No config found in Nacos during check, using skeleton", zap.String("service", req.ServiceName))
		}
	}

	// 2. Prepare effective config (Skeleton if no Nacos config exists)
	var effectiveConfig *model.AgentConfig
	if config != nil {
		cloned := *config
		effectiveConfig = &cloned
	} else {
		// Use version "0" as the skeleton config version
		effectiveConfig = &model.AgentConfig{
			Version: "0",
			Etag:    "0",
		}
	}

	// 3. Always inject server metadata
	h.injectMetadata(ctx, req, effectiveConfig)

	// 4. Compare versions and ETags
	currentVersion := effectiveConfig.Version
	currentEtag := effectiveConfig.Etag

	// Check version change
	if req.CurrentConfigVersion != currentVersion {
		result := &HandlerResult{
			HasChanges: true,
			Response:   NewConfigResponse(true, effectiveConfig, currentVersion, currentEtag, "config version changed (or metadata injected)"),
		}
		return true, result, nil
	}

	// Check ETag change
	if req.CurrentConfigEtag != "" && req.CurrentConfigEtag != currentEtag {
		result := &HandlerResult{
			HasChanges: true,
			Response:   NewConfigResponse(true, effectiveConfig, currentVersion, currentEtag, "config content changed"),
		}
		return true, result, nil
	}

	// No changes
	return false, nil, nil
}

// Poll executes the long poll wait for config changes.
func (h *ConfigPollHandler) Poll(ctx context.Context, req *PollRequest) (*HandlerResult, error) {
	if h.configMgr == nil {
		return nil, errors.New("config manager not initialized")
	}

	// Step 1: Check for immediate changes
	hasChanges, result, err := h.CheckImmediate(ctx, req)
	if err != nil {
		return nil, err
	}
	if hasChanges {
		return result, nil
	}

	// Step 2: No changes, register waiter and wait for notification
	state := h.getOrCreateServiceState(req.AppID, req.ServiceName)
	waiterCtx, cancel := context.WithCancel(ctx)
	waiter := &ConfigWaiter{
		agentID:     req.AgentID,
		appID:       req.AppID,
		serviceName: req.ServiceName,
		resultChan:  make(chan *HandlerResult, 1),
		ctx:         waiterCtx,
		cancel:      cancel,
	}

	// Register waiter (WaiterMap is self-synchronized, no external lock needed)
	state.waiters.Register(req.AgentID, waiter)

	defer func() {
		state.waiters.Deregister(req.AgentID, waiter)
		if state.waiters.IsEmpty() {
			h.cleanupIdleServiceState(state)
		}
		cancel()
	}()

	// Ensure Nacos watch is active
	state.config.EnsureWatching()

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

// computeBusinessEtagFromModel computes a content-based ETag excluding volatile fields.
func computeBusinessEtagFromModel(cfg *model.AgentConfig) string {
	if cfg == nil {
		return ""
	}
	cloned := *cfg
	// Exclude metadata fields to keep stable semantics (align with configmanager).
	cloned.Version = ""
	cloned.UpdatedAt = 0
	cloned.Etag = ""
	cloned.ServerMetadata = nil
	return ComputeEtag(&cloned)
}

// notifyServiceWaiters is the onChange callback wired by getOrCreateServiceState.
// It snapshots all waiters for a service and notifies them of a config change.
func (h *ConfigPollHandler) notifyServiceWaiters(state *serviceState, newConfig *model.AgentConfig) {
	h.logger.Info("Config changed via configMgr",
		zap.String("service", state.config.serviceName),
	)

	var waiters []*ConfigWaiter
	state.waiters.Range(func(_ string, w *ConfigWaiter) bool {
		waiters = append(waiters, w)
		return true
	})

	for _, waiter := range waiters {
		h.notifyWaiter(waiter, newConfig)
	}
}

// notifyWaiter notifies a specific waiter.
func (h *ConfigPollHandler) notifyWaiter(waiter *ConfigWaiter, config *model.AgentConfig) {
	// Reconstruct a minimal request for metadata providers
	req := &PollRequest{
		AgentID:     waiter.agentID,
		AppID:       waiter.appID,
		ServiceName: waiter.serviceName,
	}

	var effectiveConfig *model.AgentConfig
	if config != nil {
		cloned := *config
		effectiveConfig = &cloned
	} else {
		// Fallback to skeleton config if business config is deleted
		effectiveConfig = &model.AgentConfig{
			Version: "0",
			Etag:    "0",
		}
	}

	h.injectMetadata(waiter.ctx, req, effectiveConfig)

	result := &HandlerResult{
		HasChanges: true,
		Response:   NewConfigResponse(true, effectiveConfig, effectiveConfig.Version, effectiveConfig.Etag, "config change detected"),
	}

	select {
	case waiter.resultChan <- result:
		h.logger.Debug("Notified waiter of config change",
			zap.String("app_id", waiter.appID),
			zap.String("agent_id", waiter.agentID),
			zap.String("service", waiter.serviceName),
		)
	default:
		// Waiter already processed or timed out
	}
}

// GetWaiterCount returns the number of active waiters.
func (h *ConfigPollHandler) GetWaiterCount() int {
	count := 0
	h.services.Range(func(_, value interface{}) bool {
		state := value.(*serviceState)
		count += state.waiters.Count()
		return true
	})
	return count
}

// GetWatchCount returns the number of active watches.
func (h *ConfigPollHandler) GetWatchCount() int {
	count := 0
	h.services.Range(func(_, value interface{}) bool {
		state := value.(*serviceState)
		if state.config.IsWatching() {
			count++
		}
		return true
	})
	return count
}
