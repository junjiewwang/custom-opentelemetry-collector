// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

// StatsHelper provides common statistics calculation logic for AgentRegistry implementations.
// This centralizes the statistics logic to avoid duplication between Redis and Memory implementations.
type StatsHelper struct{}

// NewStatsHelper creates a new StatsHelper.
func NewStatsHelper() *StatsHelper {
	return &StatsHelper{}
}

// CalculateStats calculates agent statistics from a list of agents.
// This is the common logic used by both Memory and Redis implementations.
func (h *StatsHelper) CalculateStats(agents []*AgentInfo) *AgentStats {
	stats := &AgentStats{
		TotalAgents: len(agents),
		ByLabel:     make(map[string]int),
	}

	for _, agent := range agents {
		if agent.Status != nil {
			switch agent.Status.State {
			case AgentStateOnline:
				stats.OnlineAgents++
			case AgentStateOffline:
				stats.OfflineAgents++
			case AgentStateUnhealthy:
				stats.UnhealthyAgents++
			}
		}

		// Count by label
		for k, v := range agent.Labels {
			key := k + ":" + v
			stats.ByLabel[key]++
		}
	}

	return stats
}
