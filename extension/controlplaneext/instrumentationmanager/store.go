// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

var ErrRuleNotFound = errors.New("instrumentation rule not found")

type RuleStore interface {
	SaveRule(ctx context.Context, rule *Rule, isNew bool) error
	GetRule(ctx context.Context, ruleID string) (*Rule, error)
	ListRules(ctx context.Context, query ListRulesQuery) ([]*Rule, error)
	SaveTargetStatuses(ctx context.Context, ruleID string, targets []*RuleTargetStatus) error
	ListTargetStatuses(ctx context.Context, ruleID string) ([]*RuleTargetStatus, error)
	Start(ctx context.Context) error
	Close() error
}

type MemoryRuleStore struct {
	mu      sync.RWMutex
	rules   map[string]*Rule
	targets map[string][]*RuleTargetStatus
}

func NewMemoryRuleStore() *MemoryRuleStore {
	return &MemoryRuleStore{
		rules:   make(map[string]*Rule),
		targets: make(map[string][]*RuleTargetStatus),
	}
}

func (s *MemoryRuleStore) SaveRule(_ context.Context, rule *Rule, isNew bool) error {
	if rule == nil || rule.ID == "" {
		return errors.New("rule_id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.rules[rule.ID]; exists && isNew {
		return errors.New("instrumentation rule already exists: " + rule.ID)
	}
	if _, exists := s.rules[rule.ID]; !exists && !isNew {
		return ErrRuleNotFound
	}

	s.rules[rule.ID] = cloneRule(rule)
	return nil
}

func (s *MemoryRuleStore) GetRule(_ context.Context, ruleID string) (*Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rule, ok := s.rules[ruleID]
	if !ok {
		return nil, ErrRuleNotFound
	}
	return cloneRule(rule), nil
}

func (s *MemoryRuleStore) ListRules(_ context.Context, query ListRulesQuery) ([]*Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Rule, 0, len(s.rules))
	for _, rule := range s.rules {
		out = append(out, cloneRule(rule))
	}
	return filterAndSortRules(out, query), nil
}

func (s *MemoryRuleStore) SaveTargetStatuses(_ context.Context, ruleID string, targets []*RuleTargetStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.rules[ruleID]; !ok {
		return ErrRuleNotFound
	}
	copied := make([]*RuleTargetStatus, 0, len(targets))
	for _, target := range targets {
		copied = append(copied, cloneTargetStatus(target))
	}
	s.targets[ruleID] = copied
	return nil
}

func (s *MemoryRuleStore) ListTargetStatuses(_ context.Context, ruleID string) ([]*RuleTargetStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.rules[ruleID]; !ok {
		return nil, ErrRuleNotFound
	}
	return cloneAndSortTargetStatuses(s.targets[ruleID]), nil
}

func (s *MemoryRuleStore) Start(_ context.Context) error { return nil }
func (s *MemoryRuleStore) Close() error                  { return nil }

func cloneRule(rule *Rule) *Rule {
	if rule == nil {
		return nil
	}
	copied := *rule
	if rule.TargetAgentIDs != nil {
		copied.TargetAgentIDs = append([]string(nil), rule.TargetAgentIDs...)
	}
	if rule.LastOperation != nil {
		op := *rule.LastOperation
		copied.LastOperation = &op
	}
	if len(rule.RecentAudits) > 0 {
		copied.RecentAudits = make([]*RuleAuditEntry, 0, len(rule.RecentAudits))
		for _, audit := range rule.RecentAudits {
			copied.RecentAudits = append(copied.RecentAudits, cloneRuleAuditEntry(audit))
		}
	}
	return &copied
}

func cloneTargetStatus(target *RuleTargetStatus) *RuleTargetStatus {
	if target == nil {
		return nil
	}
	copied := *target
	return &copied
}

func cloneAndSortTargetStatuses(items []*RuleTargetStatus) []*RuleTargetStatus {
	out := make([]*RuleTargetStatus, 0, len(items))
	for _, item := range items {
		out = append(out, cloneTargetStatus(item))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i] == nil || out[j] == nil {
			return out[j] != nil
		}
		if out[i].State != out[j].State {
			return out[i].State < out[j].State
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out
}

func filterAndSortRules(items []*Rule, query ListRulesQuery) []*Rule {
	search := strings.ToLower(strings.TrimSpace(query.Search))
	out := make([]*Rule, 0, len(items))
	for _, rule := range items {
		if rule == nil {
			continue
		}
		if !query.IncludeDeleted && rule.DesiredState == RuleDesiredStateDeleted {
			continue
		}
		if query.AppID != "" && rule.AppID != query.AppID {
			continue
		}
		if query.ServiceName != "" && rule.ServiceName != query.ServiceName {
			continue
		}
		if query.InstrumentType != "" && rule.InstrumentType != query.InstrumentType {
			continue
		}
		if query.DesiredState != "" && rule.DesiredState != query.DesiredState {
			continue
		}
		if search != "" {
			haystack := strings.ToLower(strings.Join([]string{rule.Name, rule.Description, rule.AppID, rule.ServiceName, rule.ClassName, rule.MethodName, string(rule.InstrumentType)}, " "))
			if !strings.Contains(haystack, search) {
				continue
			}
		}
		out = append(out, rule)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAtMillis != out[j].UpdatedAtMillis {
			return out[i].UpdatedAtMillis > out[j].UpdatedAtMillis
		}
		return out[i].ID > out[j].ID
	})
	return out
}
