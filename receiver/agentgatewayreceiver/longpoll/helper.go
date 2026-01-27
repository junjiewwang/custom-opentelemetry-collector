// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// ComputeEtag computes an ETag for the given value by hashing its canonical JSON representation.
//
// Callers should pass a stable struct matching the stored JSON shape (e.g. legacy DTO),
// so that ETag semantics remain backward-compatible.
func ComputeEtag(v any) string {
	if v == nil {
		return ""
	}

	data, err := json.Marshal(v)
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
func GenerateDefaultConfig(agentID string) *model.AgentConfig {
	_ = agentID
	return &model.AgentConfig{
		Version: fmt.Sprintf("v1.0.0-%d", time.Now().UnixMilli()),
		Sampler: &model.SamplerConfig{
			Type:  model.SamplerTypeTraceIDRatio,
			Ratio: 1.0, // Default to full sampling
		},
	}
}

// NowMillis returns the current time in milliseconds.
func NowMillis() int64 {
	return time.Now().UnixMilli()
}
