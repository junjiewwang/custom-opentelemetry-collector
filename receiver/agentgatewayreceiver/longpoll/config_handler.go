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

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

type legacySamplerConfig struct {
	Type      int32   `json:"type"`
	Ratio     float64 `json:"ratio"`
	RulesJSON string  `json:"rules_json,omitempty"`
}

type legacyBatchConfig struct {
	MaxExportBatchSize  int32 `json:"max_export_batch_size"`
	MaxQueueSize        int32 `json:"max_queue_size"`
	ScheduleDelayMillis int64 `json:"schedule_delay_millis"`
	ExportTimeoutMillis int64 `json:"export_timeout_millis"`
}

type legacyAgentConfig struct {
	ConfigVersion             string               `json:"config_version"`
	Sampler                   *legacySamplerConfig `json:"sampler,omitempty"`
	Batch                     *legacyBatchConfig   `json:"batch,omitempty"`
	DynamicResourceAttributes map[string]string    `json:"dynamic_resource_attributes,omitempty"`
	ExtensionConfigJSON       string               `json:"extension_config_json,omitempty"`
}

func legacyAgentConfigToModel(cfg *legacyAgentConfig, etag string) *model.AgentConfig {
	if cfg == nil {
		return nil
	}
	out := &model.AgentConfig{
		Version:                   cfg.ConfigVersion,
		Etag:                      etag,
		DynamicResourceAttributes: cfg.DynamicResourceAttributes,
		ExtensionConfigJSON:       cfg.ExtensionConfigJSON,
	}

	if cfg.Sampler != nil {
		out.Sampler = &model.SamplerConfig{
			Type:      model.SamplerType(cfg.Sampler.Type),
			Ratio:     cfg.Sampler.Ratio,
			RulesJSON: cfg.Sampler.RulesJSON,
		}
	}
	if cfg.Batch != nil {
		out.Batch = &model.BatchConfig{
			MaxExportBatchSize:  cfg.Batch.MaxExportBatchSize,
			MaxQueueSize:        cfg.Batch.MaxQueueSize,
			ScheduleDelayMillis: cfg.Batch.ScheduleDelayMillis,
			ExportTimeoutMillis: cfg.Batch.ExportTimeoutMillis,
		}
	}
	return out
}

func legacyAgentConfigFromModel(cfg *model.AgentConfig) *legacyAgentConfig {
	if cfg == nil {
		return nil
	}
	out := &legacyAgentConfig{
		ConfigVersion:             cfg.Version,
		DynamicResourceAttributes: cfg.DynamicResourceAttributes,
		ExtensionConfigJSON:       cfg.ExtensionConfigJSON,
	}
	if cfg.Sampler != nil {
		out.Sampler = &legacySamplerConfig{
			Type:      int32(cfg.Sampler.Type),
			Ratio:     cfg.Sampler.Ratio,
			RulesJSON: cfg.Sampler.RulesJSON,
		}
	}
	if cfg.Batch != nil {
		out.Batch = &legacyBatchConfig{
			MaxExportBatchSize:  cfg.Batch.MaxExportBatchSize,
			MaxQueueSize:        cfg.Batch.MaxQueueSize,
			ScheduleDelayMillis: cfg.Batch.ScheduleDelayMillis,
			ExportTimeoutMillis: cfg.Batch.ExportTimeoutMillis,
		}
	}
	return out
}

// ConfigPollHandler implements LongPollHandler for configuration polling.
// It integrates with Nacos for config storage and change notification.
// It uses ServiceName as the DataID for Nacos configuration.
type ConfigPollHandler struct {
	logger      *zap.Logger
	nacosClient config_client.IConfigClient

	// services maps serviceKey (token:serviceName) -> *serviceState
	services sync.Map

	// metadataProviders is a list of providers for dynamic config injection
	metadataProviders []ServerMetadataProvider
	providersMu       sync.RWMutex

	// State
	running atomic.Bool
}

// serviceState manages waiters and cached config for a specific service.
type serviceState struct {
	sync.RWMutex
	token       string
	serviceName string
	config      *model.AgentConfig
	waiters     map[string]*ConfigWaiter // agentID -> waiter
	isWatching  bool
}

func (s *serviceState) getWaiters() []*ConfigWaiter {
	s.RLock()
	defer s.RUnlock()
	res := make([]*ConfigWaiter, 0, len(s.waiters))
	for _, w := range s.waiters {
		res = append(res, w)
	}
	return res
}

// ConfigWaiter represents a waiting config poll request.
type ConfigWaiter struct {
	agentID     string
	token       string
	serviceName string
	resultChan  chan *HandlerResult
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewConfigPollHandler creates a new ConfigPollHandler.
func NewConfigPollHandler(logger *zap.Logger, nacosClient config_client.IConfigClient) *ConfigPollHandler {
	return &ConfigPollHandler{
		logger:            logger,
		nacosClient:       nacosClient,
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

// Stop stops the handler.
func (h *ConfigPollHandler) Stop() error {
	if !h.running.Swap(false) {
		return nil
	}

	h.services.Range(func(key, value interface{}) bool {
		state := value.(*serviceState)
		state.Lock()
		defer state.Unlock()

		// Cancel all waiters
		for _, waiter := range state.waiters {
			if waiter.cancel != nil {
				waiter.cancel()
			}
		}
		state.waiters = make(map[string]*ConfigWaiter)

		// Cancel watch
		if state.isWatching {
			h.cancelWatch(state.token, state.serviceName)
			state.isWatching = false
		}
		return true
	})

	h.logger.Info("ConfigPollHandler stopped")
	return nil
}

// ShouldContinue returns whether the handler should continue polling.
func (h *ConfigPollHandler) ShouldContinue() bool {
	return h.running.Load()
}

// getOrCreateServiceState gets or creates a serviceState for the given token and serviceName.
func (h *ConfigPollHandler) getOrCreateServiceState(token, serviceName string) *serviceState {
	serviceKey := AgentKey(token, serviceName)
	actual, _ := h.services.LoadOrStore(serviceKey, &serviceState{
		token:       token,
		serviceName: serviceName,
		waiters:     make(map[string]*ConfigWaiter),
	})
	return actual.(*serviceState)
}

// CheckImmediate checks if there are config changes immediately.
func (h *ConfigPollHandler) CheckImmediate(ctx context.Context, req *PollRequest) (bool, *HandlerResult, error) {
	if h.nacosClient == nil {
		return false, nil, errors.New("nacos client not initialized")
	}

	state := h.getOrCreateServiceState(req.Token, req.ServiceName)

	// 1. Get base config (cached or from Nacos)
	state.RLock()
	config := state.config
	state.RUnlock()

	if config == nil {
		var err error
		config, err = h.loadConfigFromNacos(ctx, req.Token, req.ServiceName)
		if err != nil {
			h.logger.Debug("No config found in Nacos, using default skeleton", zap.String("service", req.ServiceName))
		}
		state.Lock()
		state.config = config
		state.Unlock()
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
	if h.nacosClient == nil {
		return nil, errors.New("nacos client not initialized")
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
	state := h.getOrCreateServiceState(req.Token, req.ServiceName)
	waiterCtx, cancel := context.WithCancel(ctx)
	waiter := &ConfigWaiter{
		agentID:     req.AgentID,
		token:       req.Token,
		serviceName: req.ServiceName,
		resultChan:  make(chan *HandlerResult, 1),
		ctx:         waiterCtx,
		cancel:      cancel,
	}

	// Register waiter
	state.Lock()
	state.waiters[req.AgentID] = waiter
	state.Unlock()

	defer func() {
		state.Lock()
		delete(state.waiters, req.AgentID)
		state.Unlock()
		cancel()
	}()

	// Ensure Nacos watch is active
	h.ensureWatching(state)

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

// loadConfigFromNacos loads config from Nacos for the specific service.
func (h *ConfigPollHandler) loadConfigFromNacos(ctx context.Context, token, serviceName string) (*model.AgentConfig, error) {
	if serviceName == "" {
		return nil, nil
	}

	legacyCfg, err := h.loadConfig(ctx, token, serviceName)
	if err != nil {
		return nil, err
	}

	etag := ComputeEtag(legacyCfg)
	return legacyAgentConfigToModel(legacyCfg, etag), nil
}

// loadConfig loads config from Nacos.
func (h *ConfigPollHandler) loadConfig(ctx context.Context, group, dataID string) (*legacyAgentConfig, error) {
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

		var config legacyAgentConfig
		if err := json.Unmarshal([]byte(res.content), &config); err != nil {
			return nil, fmt.Errorf("failed to parse config: %w", err)
		}

		return &config, nil
	}
}

// ensureWatching ensures Nacos watch is active for the service.
func (h *ConfigPollHandler) ensureWatching(state *serviceState) {
	state.Lock()
	defer state.Unlock()

	if state.isWatching || state.serviceName == "" {
		return
	}

	err := h.nacosClient.ListenConfig(vo.ConfigParam{
		Group:  state.token,
		DataId: state.serviceName,
		OnChange: func(namespace, group, dataId, data string) {
			h.handleConfigChange(group, dataId, data)
		},
	})

	if err != nil {
		h.logger.Warn("Failed to setup config watch",
			zap.String("token", state.token),
			zap.String("service", state.serviceName),
			zap.Error(err),
		)
		return
	}

	state.isWatching = true
	h.logger.Debug("Setup config watch",
		zap.String("token", state.token),
		zap.String("service", state.serviceName),
	)
}

// cancelWatch cancels Nacos config watch.
func (h *ConfigPollHandler) cancelWatch(token, serviceName string) {
	err := h.nacosClient.CancelListenConfig(vo.ConfigParam{
		Group:  token,
		DataId: serviceName,
	})

	if err != nil {
		h.logger.Warn("Failed to cancel config watch",
			zap.String("token", token),
			zap.String("service", serviceName),
			zap.Error(err),
		)
	}
}

// handleConfigChange handles config change notification from Nacos.
func (h *ConfigPollHandler) handleConfigChange(token, serviceName, data string) {
	h.logger.Info("Config changed in Nacos",
		zap.String("token", token),
		zap.String("service", serviceName),
	)

	// Parse new config
	var newConfig *model.AgentConfig
	if data != "" {
		var legacyCfg legacyAgentConfig
		if err := json.Unmarshal([]byte(data), &legacyCfg); err != nil {
			h.logger.Error("Failed to parse changed config",
				zap.String("token", token),
				zap.String("service", serviceName),
				zap.Error(err),
			)
			return
		}
		etag := ComputeEtag(&legacyCfg)
		newConfig = legacyAgentConfigToModel(&legacyCfg, etag)
	}

	// Update state and notify waiters
	serviceKey := AgentKey(token, serviceName)
	if val, ok := h.services.Load(serviceKey); ok {
		state := val.(*serviceState)
		state.Lock()
		state.config = newConfig
		waiters := make([]*ConfigWaiter, 0, len(state.waiters))
		for _, w := range state.waiters {
			waiters = append(waiters, w)
		}
		state.Unlock()

		for _, waiter := range waiters {
			h.notifyWaiter(waiter, newConfig)
		}
	}
}

// notifyWaiter notifies a specific waiter.
func (h *ConfigPollHandler) notifyWaiter(waiter *ConfigWaiter, config *model.AgentConfig) {
	// Reconstruct a minimal request for metadata providers
	req := &PollRequest{
		AgentID:     waiter.agentID,
		Token:       waiter.token,
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
			zap.String("token", waiter.token),
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
		state.RLock()
		count += len(state.waiters)
		state.RUnlock()
		return true
	})
	return count
}

// GetWatchCount returns the number of active watches.
func (h *ConfigPollHandler) GetWatchCount() int {
	count := 0
	h.services.Range(func(_, value interface{}) bool {
		state := value.(*serviceState)
		state.RLock()
		if state.isWatching {
			count++
		}
		state.RUnlock()
		return true
	})
	return count
}
