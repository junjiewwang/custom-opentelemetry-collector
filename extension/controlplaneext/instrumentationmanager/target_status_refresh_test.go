// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
)

// resultExpiredTaskManagerStub reproduces the state after task results are
// evicted by result_ttl: GetTaskResult reports no result, while the task record
// (and therefore GetTaskStatus) still carries the recorded terminal status.
// Only the two methods called by refreshTargetStatus are implemented; the
// embedded interface is nil and must not be used by this code path.
type resultExpiredTaskManagerStub struct {
	taskmanager.TaskManager
	result *model.TaskResult
	found  bool
	info   *taskmanager.TaskInfo
}

func (s *resultExpiredTaskManagerStub) GetTaskResult(_ context.Context, _ string) (*model.TaskResult, bool, error) {
	return s.result, s.found, nil
}

func (s *resultExpiredTaskManagerStub) GetTaskStatus(_ context.Context, _ string) (*taskmanager.TaskInfo, error) {
	return s.info, nil
}

func newRefreshTestService(mgr taskmanager.TaskManager) *InstrumentationService {
	return &InstrumentationService{logger: zap.NewNop(), taskMgr: mgr}
}

// Regression: an applied target used to be rolled back to "pending" once the
// task result outlived result_ttl, making the console report Low Coverage even
// though the rule had been applied successfully.
func TestRefreshTargetStatusAfterResultExpiry(t *testing.T) {
	cases := []struct {
		name    string
		current TargetState
		desired RuleDesiredState
		status  model.TaskStatus
		want    TargetState
	}{
		{"success keeps applied", TargetStateApplied, RuleDesiredStateActive, model.TaskStatusSuccess, TargetStateApplied},
		{"success repairs a rolled-back pending", TargetStatePending, RuleDesiredStateActive, model.TaskStatusSuccess, TargetStateApplied},
		{"success on paused resolves to removed", TargetStateApplied, RuleDesiredStatePaused, model.TaskStatusSuccess, TargetStateRemoved},
		{"running does not roll back applied", TargetStateApplied, RuleDesiredStateActive, model.TaskStatusRunning, TargetStateApplied},
		{"pending does not roll back removed", TargetStateRemoved, RuleDesiredStatePaused, model.TaskStatusPending, TargetStateRemoved},
		{"failed is terminal and overrides", TargetStateApplied, RuleDesiredStateActive, model.TaskStatusFailed, TargetStateFailed},
		{"timeout is terminal and overrides", TargetStateApplied, RuleDesiredStateActive, model.TaskStatusTimeout, TargetStateFailed},
		{"new dispatched task can still progress", TargetStateDispatched, RuleDesiredStateActive, model.TaskStatusRunning, TargetStateRunning},
		{"pending maps to dispatched when not terminal", TargetStatePending, RuleDesiredStateActive, model.TaskStatusPending, TargetStateDispatched},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &resultExpiredTaskManagerStub{
				found: false,
				info:  &taskmanager.TaskInfo{Task: &model.Task{ID: "task-1"}, Status: tc.status},
			}
			svc := newRefreshTestService(stub)
			rule := &Rule{ID: "rule-1", DesiredState: tc.desired}
			target := &RuleTargetStatus{
				RuleID:     rule.ID,
				TaskID:     "task-1",
				State:      tc.current,
				TaskStatus: "pending",
			}
			changed, err := svc.refreshTargetStatus(context.Background(), rule, target, time.Now().UnixMilli())
			require.NoError(t, err)
			assert.Equal(t, tc.want, target.State, "target state after result expiry")
			assert.NotEmpty(t, target.TaskStatus)
			assert.NotZero(t, target.UpdatedAtMillis)
			// "changed" also covers task_status refresh, so assert it only
			// where the state itself must change.
			if tc.current != tc.want {
				assert.True(t, changed)
			}
		})
	}
}

// When a result is still present it remains authoritative, even if the task
// status snapshot disagrees.
func TestRefreshTargetStatusResultIsAuthoritative(t *testing.T) {
	stub := &resultExpiredTaskManagerStub{
		found:  true,
		result: &model.TaskResult{Status: model.TaskStatusFailed, ErrorMessage: " redefine failed "},
		info:   &taskmanager.TaskInfo{Task: &model.Task{ID: "task-1"}, Status: model.TaskStatusSuccess},
	}
	svc := newRefreshTestService(stub)
	rule := &Rule{ID: "rule-1", DesiredState: RuleDesiredStateActive}
	target := &RuleTargetStatus{RuleID: rule.ID, TaskID: "task-1", State: TargetStateApplied}

	changed, err := svc.refreshTargetStatus(context.Background(), rule, target, time.Now().UnixMilli())
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, TargetStateFailed, target.State)
	assert.Equal(t, "redefine failed", target.LastErrorMessage)
}

// Offline / expired targets are authoritative and must not be rewritten by a
// stale task status (existing guard, kept as a regression).
func TestRefreshTargetStatusSkipsOfflineAndExpired(t *testing.T) {
	for _, state := range []TargetState{TargetStateOffline, TargetStateExpired} {
		t.Run(string(state), func(t *testing.T) {
			stub := &resultExpiredTaskManagerStub{
				info: &taskmanager.TaskInfo{Task: &model.Task{ID: "task-1"}, Status: model.TaskStatusSuccess},
			}
			svc := newRefreshTestService(stub)
			rule := &Rule{ID: "rule-1", DesiredState: RuleDesiredStateActive}
			target := &RuleTargetStatus{RuleID: rule.ID, TaskID: "task-1", State: state, TaskStatus: "pending"}

			changed, err := svc.refreshTargetStatus(context.Background(), rule, target, time.Now().UnixMilli())
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, state, target.State)
		})
	}
}

func TestMapInfoStatusToTargetStateTerminalOutcomes(t *testing.T) {
	// Terminal outcomes must agree with the result mapper (this is the fix).
	for _, desired := range []RuleDesiredState{RuleDesiredStateActive, RuleDesiredStatePaused} {
		for _, status := range []model.TaskStatus{
			model.TaskStatusSuccess, model.TaskStatusFailed, model.TaskStatusTimeout,
			model.TaskStatusCancelled, model.TaskStatusResultTooLarge,
		} {
			assert.Equal(t,
				mapResultStatusToTargetState(status, desired),
				mapInfoStatusToTargetState(status, desired),
				"terminal status=%v desired=%v", status, desired)
		}
	}
	// Non-terminal info states intentionally keep the historical semantics:
	// a task record without a result means the operation was dispatched.
	for _, status := range []model.TaskStatus{model.TaskStatusPending, model.TaskStatusUnspecified} {
		assert.Equal(t, TargetStateDispatched, mapInfoStatusToTargetState(status, RuleDesiredStateActive))
	}
	assert.Equal(t, TargetStateRunning, mapInfoStatusToTargetState(model.TaskStatusRunning, RuleDesiredStateActive))
}
