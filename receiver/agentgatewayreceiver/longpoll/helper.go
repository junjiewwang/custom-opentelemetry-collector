// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// ComputeEtag computes an ETag for the given config.
func ComputeEtag(config *controlplanev1.AgentConfig) string {
	if config == nil {
		return ""
	}

	data, err := json.Marshal(config)
	if err != nil {
		return ""
	}

	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:])
}

// AgentKey generates a unique key for an agent.
func AgentKey(token, agentID string) string {
	if token == "" {
		return agentID
	}
	return fmt.Sprintf("%s:%s", token, agentID)
}

// GenerateDefaultConfig generates a default config for a new agent.
func GenerateDefaultConfig(agentID string) *controlplanev1.AgentConfig {
	return &controlplanev1.AgentConfig{
		ConfigVersion: fmt.Sprintf("v1.0.0-%d", time.Now().UnixMilli()),
		Sampler: &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerTypeTraceIDRatio,
			Ratio: 1.0, // Default to full sampling
		},
	}
}

// NowMillis returns the current time in milliseconds.
func NowMillis() int64 {
	return time.Now().UnixMilli()
}
