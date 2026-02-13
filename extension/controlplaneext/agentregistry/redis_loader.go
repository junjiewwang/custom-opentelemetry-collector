// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

// RedisLoader provides batch loading capabilities for Redis-based registry.
// It encapsulates the Pipeline-based batch read logic for reuse across
// GetAllAgents, GetOnlineAgents, GetInstancesByService, etc.
type RedisLoader struct {
	client redis.UniversalClient
	keys   *KeyBuilder
}

// NewRedisLoader creates a new RedisLoader.
func NewRedisLoader(client redis.UniversalClient, keys *KeyBuilder) *RedisLoader {
	return &RedisLoader{
		client: client,
		keys:   keys,
	}
}

// LoadAgentsByPaths loads multiple agents by their full paths using Pipeline.
// Returns agents that were successfully loaded (skips expired/invalid entries).
// This reduces N+1 Redis calls to just 2 calls (HGETALL + Pipeline GET).
// Deduplicates by instanceKey to avoid returning the same agent twice when
// multiple agentIDs (e.g., stale entries in _ids hash) point to the same fullPath.
func (l *RedisLoader) LoadAgentsByPaths(ctx context.Context, paths map[string]string) ([]*AgentInfo, error) {
	if len(paths) == 0 {
		return []*AgentInfo{}, nil
	}

	// Build instance keys for batch retrieval, deduplicating by instanceKey.
	// Multiple agentIDs can map to the same fullPath (stale _ids entries),
	// which would result in duplicate agents in the response.
	type keyInfo struct {
		agentID     string
		instanceKey string
	}
	seen := make(map[string]struct{})
	keyInfos := make([]keyInfo, 0, len(paths))

	for agentID, fullPath := range paths {
		appIDEsc, serviceNameEsc, instanceKey, err := l.keys.ParseFullKeyPath(fullPath)
		if err != nil {
			continue
		}
		key := l.keys.InstanceKeyFromParts(appIDEsc, serviceNameEsc, instanceKey)
		if _, exists := seen[key]; exists {
			continue // skip duplicate instanceKey
		}
		seen[key] = struct{}{}
		keyInfos = append(keyInfos, keyInfo{agentID: agentID, instanceKey: key})
	}

	if len(keyInfos) == 0 {
		return []*AgentInfo{}, nil
	}

	// Execute Pipeline to batch GET all instance info
	pipe := l.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(keyInfos))
	for i, ki := range keyInfos {
		cmds[i] = pipe.Get(ctx, ki.instanceKey)
	}

	// Execute pipeline (allows partial failures - some keys may have expired)
	_, _ = pipe.Exec(ctx)

	// Parse results
	agents := make([]*AgentInfo, 0, len(cmds))
	for _, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			// Key expired or not found, skip
			continue
		}
		var agent AgentInfo
		if err := json.Unmarshal([]byte(data), &agent); err != nil {
			continue
		}
		agents = append(agents, &agent)
	}

	return agents, nil
}

// LoadAgentsByFullPaths loads multiple agents by a slice of full paths using Pipeline.
// This is useful when you already have a list of paths (e.g., from ZRange on online set).
func (l *RedisLoader) LoadAgentsByFullPaths(ctx context.Context, fullPaths []string) ([]*AgentInfo, error) {
	if len(fullPaths) == 0 {
		return []*AgentInfo{}, nil
	}

	// Build instance keys for batch retrieval
	instanceKeys := make([]string, 0, len(fullPaths))
	for _, fullPath := range fullPaths {
		appIDEsc, serviceNameEsc, instanceKey, err := l.keys.ParseFullKeyPath(fullPath)
		if err != nil {
			continue
		}
		key := l.keys.InstanceKeyFromParts(appIDEsc, serviceNameEsc, instanceKey)
		instanceKeys = append(instanceKeys, key)
	}

	if len(instanceKeys) == 0 {
		return []*AgentInfo{}, nil
	}

	// Execute Pipeline to batch GET all instance info
	pipe := l.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(instanceKeys))
	for i, key := range instanceKeys {
		cmds[i] = pipe.Get(ctx, key)
	}

	// Execute pipeline
	_, _ = pipe.Exec(ctx)

	// Parse results
	agents := make([]*AgentInfo, 0, len(cmds))
	for _, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue
		}
		var agent AgentInfo
		if err := json.Unmarshal([]byte(data), &agent); err != nil {
			continue
		}
		agents = append(agents, &agent)
	}

	return agents, nil
}

// LoadAgentsByInstanceKeys loads multiple agents by their instance keys using Pipeline.
// This is useful for GetInstancesByService where you already have the instance keys.
func (l *RedisLoader) LoadAgentsByInstanceKeys(ctx context.Context, appID, serviceName string, instanceKeys []string) ([]*AgentInfo, error) {
	if len(instanceKeys) == 0 {
		return []*AgentInfo{}, nil
	}

	appIDEsc := l.keys.Encode(appID)
	serviceNameEsc := l.keys.Encode(serviceName)

	// Execute Pipeline to batch GET all instance info
	pipe := l.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(instanceKeys))
	for i, instanceKey := range instanceKeys {
		key := l.keys.InstanceKeyFromParts(appIDEsc, serviceNameEsc, instanceKey)
		cmds[i] = pipe.Get(ctx, key)
	}

	// Execute pipeline
	_, _ = pipe.Exec(ctx)

	// Parse results
	agents := make([]*AgentInfo, 0, len(cmds))
	for _, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue
		}
		var agent AgentInfo
		if err := json.Unmarshal([]byte(data), &agent); err != nil {
			continue
		}
		agents = append(agents, &agent)
	}

	return agents, nil
}

// GetAllAgentPaths returns all agent ID -> fullPath mappings from the _ids hash.
func (l *RedisLoader) GetAllAgentPaths(ctx context.Context) (map[string]string, error) {
	return l.client.HGetAll(ctx, l.keys.AgentIDsKey()).Result()
}
