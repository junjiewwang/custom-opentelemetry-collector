// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// LongPollType defines the type of long polling.
type LongPollType string

const (
	// LongPollTypeConfig is for configuration polling.
	LongPollTypeConfig LongPollType = "CONFIG"
	// LongPollTypeTask is for task polling.
	LongPollTypeTask LongPollType = "TASK"
)

// PollRequest represents a unified long poll request.
type PollRequest struct {
	AgentID              string `json:"agent_id"`
	Token                string `json:"token"`                            // AppID/Token for multi-tenant
	CurrentConfigVersion string `json:"current_config_version,omitempty"` // Current config version
	CurrentConfigEtag    string `json:"current_config_etag,omitempty"`    // Current config ETag
	TimeoutMillis        int64  `json:"timeout_millis,omitempty"`         // Client expected timeout
}

// PollResponse represents a unified long poll response.
type PollResponse struct {
	Type       LongPollType `json:"type"`
	HasChanges bool         `json:"has_changes"`

	// Config related fields
	Config        *controlplanev1.AgentConfig `json:"config,omitempty"`
	ConfigVersion string                      `json:"config_version,omitempty"`
	ConfigEtag    string                      `json:"config_etag,omitempty"`

	// Task related fields
	Tasks []*controlplanev1.Task `json:"tasks,omitempty"`

	Message string `json:"message,omitempty"`
}

// CombinedPollResponse represents the combined response from multiple handlers.
type CombinedPollResponse struct {
	HasAnyChanges bool                         `json:"has_any_changes"`
	Results       map[LongPollType]*PollResponse `json:"results,omitempty"`
	Message       string                       `json:"message,omitempty"`
}

// HandlerResult represents the result from a poll handler.
type HandlerResult struct {
	HasChanges bool
	Response   *PollResponse
	Error      error
}

// NewConfigResponse creates a config poll response.
func NewConfigResponse(hasChanges bool, config *controlplanev1.AgentConfig, version, etag, message string) *PollResponse {
	return &PollResponse{
		Type:          LongPollTypeConfig,
		HasChanges:    hasChanges,
		Config:        config,
		ConfigVersion: version,
		ConfigEtag:    etag,
		Message:       message,
	}
}

// NewTaskResponse creates a task poll response.
func NewTaskResponse(hasChanges bool, tasks []*controlplanev1.Task, message string) *PollResponse {
	return &PollResponse{
		Type:       LongPollTypeTask,
		HasChanges: hasChanges,
		Tasks:      tasks,
		Message:    message,
	}
}

// NoChangeResponse creates a no-change response.
func NoChangeResponse(pollType LongPollType) *PollResponse {
	return &PollResponse{
		Type:       pollType,
		HasChanges: false,
		Message:    "no changes",
	}
}
