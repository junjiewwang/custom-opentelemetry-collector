// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"fmt"
	"sort"
	"strings"
)

// SortField defines the supported sort fields for agent listing.
type SortField string

const (
	// SortFieldStatusHeartbeat sorts by online status weight first, then by last heartbeat time.
	// This is the default sort field: online agents appear first, most recently active at the top.
	SortFieldStatusHeartbeat SortField = "status_heartbeat"

	// SortFieldLastHeartbeat sorts by the last heartbeat timestamp.
	SortFieldLastHeartbeat SortField = "last_heartbeat"

	// SortFieldRegisteredAt sorts by the registration timestamp.
	SortFieldRegisteredAt SortField = "registered_at"

	// SortFieldStartTime sorts by the agent start time.
	SortFieldStartTime SortField = "start_time"
)

// SortOrder defines the sort direction.
type SortOrder string

const (
	SortOrderAsc  SortOrder = "asc"
	SortOrderDesc SortOrder = "desc"
)

// validSortFields is the whitelist of allowed sort fields.
var validSortFields = map[SortField]bool{
	SortFieldStatusHeartbeat: true,
	SortFieldLastHeartbeat:   true,
	SortFieldRegisteredAt:    true,
	SortFieldStartTime:       true,
}

// validSortOrders is the whitelist of allowed sort orders.
var validSortOrders = map[SortOrder]bool{
	SortOrderAsc:  true,
	SortOrderDesc: true,
}

// SortOptions holds the sort configuration parsed from query parameters.
type SortOptions struct {
	Field SortField
	Order SortOrder
}

// DefaultSortOptions returns the default sort options.
// Default: sort by status_heartbeat descending (online first, most recently active at top).
func DefaultSortOptions() SortOptions {
	return SortOptions{
		Field: SortFieldStatusHeartbeat,
		Order: SortOrderDesc,
	}
}

// ParseSortOptions parses and validates sort parameters from query strings.
// Empty values fall back to defaults. Invalid values return an error.
func ParseSortOptions(field, order string) (SortOptions, error) {
	opts := DefaultSortOptions()

	if field != "" {
		f := SortField(field)
		if !validSortFields[f] {
			return opts, fmt.Errorf("invalid sort_by: %s, valid values: %s", field, validSortFieldNames())
		}
		opts.Field = f
	}

	if order != "" {
		o := SortOrder(strings.ToLower(order))
		if !validSortOrders[o] {
			return opts, fmt.Errorf("invalid sort_order: %s, valid values: asc, desc", order)
		}
		opts.Order = o
	}

	return opts, nil
}

// SortAgents sorts the agent list in place based on the given options.
// Uses stable sort to preserve relative order of equal elements.
// Uses AgentID as a tie-breaker for deterministic ordering.
func SortAgents(agents []*AgentInfo, opts SortOptions) {
	if len(agents) <= 1 {
		return
	}

	isDesc := opts.Order == SortOrderDesc

	sort.SliceStable(agents, func(i, j int) bool {
		a, b := agents[i], agents[j]

		switch opts.Field {
		case SortFieldStatusHeartbeat:
			// Primary: state weight (online > unhealthy > offline)
			wa, wb := stateWeight(a), stateWeight(b)
			if wa != wb {
				if isDesc {
					return wa > wb
				}
				return wa < wb
			}
			// Secondary: last heartbeat time
			if a.LastHeartbeat != b.LastHeartbeat {
				if isDesc {
					return a.LastHeartbeat > b.LastHeartbeat
				}
				return a.LastHeartbeat < b.LastHeartbeat
			}

		case SortFieldLastHeartbeat:
			if a.LastHeartbeat != b.LastHeartbeat {
				if isDesc {
					return a.LastHeartbeat > b.LastHeartbeat
				}
				return a.LastHeartbeat < b.LastHeartbeat
			}

		case SortFieldRegisteredAt:
			if a.RegisteredAt != b.RegisteredAt {
				if isDesc {
					return a.RegisteredAt > b.RegisteredAt
				}
				return a.RegisteredAt < b.RegisteredAt
			}

		case SortFieldStartTime:
			if a.StartTime != b.StartTime {
				if isDesc {
					return a.StartTime > b.StartTime
				}
				return a.StartTime < b.StartTime
			}
		}

		// Tie-breaker: AgentID ascending for deterministic order
		return a.AgentID < b.AgentID
	})
}

// stateWeight maps agent state to a numeric weight for sorting.
// Higher weight = higher priority: online(2) > unhealthy(1) > offline(0).
func stateWeight(agent *AgentInfo) int {
	if agent.Status == nil {
		return 0
	}
	switch agent.Status.State {
	case AgentStateOnline:
		return 2
	case AgentStateUnhealthy:
		return 1
	case AgentStateOffline:
		return 0
	default:
		return 0
	}
}

// validSortFieldNames returns a comma-separated string of valid sort field names.
func validSortFieldNames() string {
	names := make([]string, 0, len(validSortFields))
	for f := range validSortFields {
		names = append(names, string(f))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
