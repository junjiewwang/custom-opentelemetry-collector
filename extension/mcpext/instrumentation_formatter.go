// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
)

// The views below keep MCP responses small and stable: only fields an AI agent
// needs for diagnosis are exposed, instead of serializing internal models.

type instrumentRuleView struct {
	RuleID          string                                  `json:"rule_id"`
	Name            string                                  `json:"name"`
	AppID           string                                  `json:"app_id"`
	ServiceName     string                                  `json:"service_name"`
	ScopeType       instrumentationmanager.ScopeType        `json:"scope_type"`
	ClassName       string                                  `json:"class_name"`
	MethodName      string                                  `json:"method_name"`
	InstrumentType  instrumentationmanager.InstrumentType   `json:"instrument_type"`
	DesiredState    instrumentationmanager.RuleDesiredState `json:"desired_state"`
	CreatedBy       string                                  `json:"created_by,omitempty"`
	UpdatedAtMillis int64                                   `json:"updated_at_millis"`
	Summary         instrumentRuleSummaryView               `json:"summary"`
}

type instrumentRuleSummaryView struct {
	Status         instrumentationmanager.OperationStatus `json:"status"`
	TotalTargets   int                                    `json:"total_targets"`
	AppliedTargets int                                    `json:"applied_targets"`
	RunningTargets int                                    `json:"running_targets"`
	PendingTargets int                                    `json:"pending_targets"`
	FailedTargets  int                                    `json:"failed_targets"`
}

type instrumentTargetView struct {
	AgentID          string                             `json:"agent_id"`
	State            instrumentationmanager.TargetState `json:"state"`
	TaskID           string                             `json:"task_id,omitempty"`
	TaskStatus       string                             `json:"task_status,omitempty"`
	LastErrorMessage string                             `json:"last_error_message,omitempty"`
}

type instrumentRuleDetailView struct {
	Rule    instrumentRuleView     `json:"rule"`
	Targets []instrumentTargetView `json:"targets"`
}

type instrumentRuleListView struct {
	Total int                  `json:"total"`
	Rules []instrumentRuleView `json:"rules"`
}

type instrumentSnapshotView struct {
	RuleID            string                                              `json:"rule_id"`
	DesiredState      instrumentationmanager.RuleDesiredState             `json:"desired_state"`
	GeneratedAtMillis int64                                               `json:"generated_at_millis"`
	Summary           instrumentationmanager.RuleRuntimeSnapshotSummary   `json:"summary"`
	Targets           []*instrumentationmanager.RuleRuntimeSnapshotTarget `json:"targets"`
}

func newInstrumentRuleView(rule *instrumentationmanager.Rule) instrumentRuleView {
	return instrumentRuleView{
		RuleID:          rule.ID,
		Name:            rule.Name,
		AppID:           rule.AppID,
		ServiceName:     rule.ServiceName,
		ScopeType:       rule.ScopeType,
		ClassName:       rule.ClassName,
		MethodName:      rule.MethodName,
		InstrumentType:  rule.InstrumentType,
		DesiredState:    rule.DesiredState,
		CreatedBy:       rule.CreatedBy,
		UpdatedAtMillis: rule.UpdatedAtMillis,
		Summary: instrumentRuleSummaryView{
			Status:         rule.Summary.Status,
			TotalTargets:   rule.Summary.TotalTargets,
			AppliedTargets: rule.Summary.AppliedTargets,
			RunningTargets: rule.Summary.RunningTargets,
			PendingTargets: rule.Summary.PendingTargets,
			FailedTargets:  rule.Summary.FailedTargets,
		},
	}
}

func newInstrumentTargetViews(targets []*instrumentationmanager.RuleTargetStatus) []instrumentTargetView {
	views := make([]instrumentTargetView, 0, len(targets))
	for _, target := range targets {
		if target == nil {
			continue
		}
		views = append(views, instrumentTargetView{
			AgentID:          target.AgentID,
			State:            target.State,
			TaskID:           target.TaskID,
			TaskStatus:       target.TaskStatus,
			LastErrorMessage: target.LastErrorMessage,
		})
	}
	return views
}

func marshalInstrumentView(view any) (string, error) {
	data, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化动态插桩结果失败: %w", err)
	}
	return string(data), nil
}

func formatInstrumentRuleList(rules []*instrumentationmanager.Rule) (string, error) {
	view := instrumentRuleListView{
		Total: len(rules),
		Rules: make([]instrumentRuleView, 0, len(rules)),
	}
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		view.Rules = append(view.Rules, newInstrumentRuleView(rule))
	}
	return marshalInstrumentView(view)
}

func formatInstrumentRuleDetail(
	rule *instrumentationmanager.Rule,
	targets []*instrumentationmanager.RuleTargetStatus,
) (string, error) {
	return marshalInstrumentView(instrumentRuleDetailView{
		Rule:    newInstrumentRuleView(rule),
		Targets: newInstrumentTargetViews(targets),
	})
}

func formatInstrumentSnapshot(snapshot *instrumentationmanager.RuleRuntimeSnapshot) (string, error) {
	return marshalInstrumentView(instrumentSnapshotView{
		RuleID:            snapshot.RuleID,
		DesiredState:      snapshot.DesiredState,
		GeneratedAtMillis: snapshot.GeneratedAtMillis,
		Summary:           snapshot.Summary,
		Targets:           snapshot.Targets,
	})
}
