// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// TaskClaimEngine abstracts the task engine operations needed by the poll handler.
// This interface avoids a hard dependency on the taskengine package from the
// longpoll package, and makes the handler testable with mocks.
type TaskClaimEngine interface {
	// GetPendingTasks returns pending tasks available for the given agent.
	GetPendingTasks(ctx context.Context, agentID string) ([]*model.Task, error)

	// ClaimTaskForAgent atomically claims a pending task for the given agent.
	// Returns nil, nil if no tasks are available.
	ClaimTaskForAgent(ctx context.Context, agentID string) (*model.Task, error)

	// IsTaskCancelled checks if a task has been cancelled.
	IsTaskCancelled(ctx context.Context, taskID string) (bool, error)
}

// TaskPollHandlerEngine implements LongPollHandler for task polling using the
// unified task engine instead of direct Redis operations.
//
// Key differences from the legacy TaskPollHandler:
//   - Uses TaskClaimEngine interface (backed by taskengine.Engine) instead of Redis
//   - Claim-on-dispatch uses Engine.Claim() (atomic dequeue + status transition)
//   - No Lua scripts or raw Redis commands
//   - Pub/Sub notification is simplified to a polling loop with notify channel
type TaskPollHandlerEngine struct {
	logger *zap.Logger
	engine TaskClaimEngine

	// Waiters management (per agent)
	waiters sync.Map // agentID -> *TaskWaiter

	// Task notification channel — triggered when engine publishes a "submitted" event.
	// This replaces the Redis Pub/Sub mechanism.
	notifyCh chan taskNotification

	// State
	running atomic.Bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// taskNotification carries information about a newly submitted task.
type taskNotification struct {
	taskID        string
	targetAgentID string // empty means global/broadcast
}

// NewTaskPollHandlerEngine creates a new engine-backed TaskPollHandler.
func NewTaskPollHandlerEngine(logger *zap.Logger, engine TaskClaimEngine) *TaskPollHandlerEngine {
	return &TaskPollHandlerEngine{
		logger:   logger,
		engine:   engine,
		notifyCh: make(chan taskNotification, 256),
		stopCh:   make(chan struct{}),
	}
}

// Ensure TaskPollHandlerEngine implements LongPollHandler.
var _ LongPollHandler = (*TaskPollHandlerEngine)(nil)

// GetType returns the handler type.
func (h *TaskPollHandlerEngine) GetType() LongPollType {
	return LongPollTypeTask
}

// Start initializes the handler.
func (h *TaskPollHandlerEngine) Start(_ context.Context) error {
	if h.running.Swap(true) {
		return nil
	}

	// Start notification dispatcher
	h.wg.Add(1)
	go h.dispatchNotifications()

	h.logger.Info("TaskPollHandlerEngine started (engine-backed)")
	return nil
}

// Stop stops the handler.
func (h *TaskPollHandlerEngine) Stop() error {
	if !h.running.Swap(false) {
		return nil
	}

	close(h.stopCh)

	// Cancel all waiters
	h.waiters.Range(func(key, value interface{}) bool {
		waiter := value.(*TaskWaiter)
		if waiter.cancel != nil {
			waiter.cancel()
		}
		return true
	})

	h.wg.Wait()
	h.logger.Info("TaskPollHandlerEngine stopped")
	return nil
}

// ShouldContinue returns whether the handler should continue polling.
func (h *TaskPollHandlerEngine) ShouldContinue() bool {
	return h.running.Load()
}

// CheckImmediate checks if there are pending tasks immediately.
func (h *TaskPollHandlerEngine) CheckImmediate(ctx context.Context, req *PollRequest) (bool, *HandlerResult, error) {
	if h.engine == nil {
		return false, nil, fmt.Errorf("task engine not initialized")
	}

	// Get pending tasks for this agent via engine
	tasks, err := h.engine.GetPendingTasks(ctx, req.AgentID)
	if err != nil {
		return false, nil, fmt.Errorf("get pending tasks: %w", err)
	}

	h.logger.Debug("CheckImmediate: pending tasks result",
		zap.String("agent_id", req.AgentID),
		zap.Int("task_count", len(tasks)),
	)

	if len(tasks) == 0 {
		return false, nil, nil
	}

	// Claim-on-dispatch: claim all available tasks for this agent
	var claimedTasks []*model.Task
	for range tasks {
		claimed, err := h.engine.ClaimTaskForAgent(ctx, req.AgentID)
		if err != nil {
			h.logger.Warn("Failed to claim task", zap.String("agent_id", req.AgentID), zap.Error(err))
			break
		}
		if claimed == nil {
			break // No more tasks to claim
		}
		claimedTasks = append(claimedTasks, claimed)
	}

	if len(claimedTasks) == 0 {
		return false, nil, nil
	}

	result := &HandlerResult{
		HasChanges: true,
		Response:   NewTaskResponse(true, claimedTasks, fmt.Sprintf("%d tasks available", len(claimedTasks))),
	}
	return true, result, nil
}

// Poll executes the long poll wait for new tasks.
func (h *TaskPollHandlerEngine) Poll(ctx context.Context, req *PollRequest) (*HandlerResult, error) {
	if h.engine == nil {
		return nil, fmt.Errorf("task engine not initialized")
	}

	// Step 1: Check for immediate tasks
	hasChanges, result, err := h.CheckImmediate(ctx, req)
	if err != nil {
		return nil, err
	}
	if hasChanges {
		return result, nil
	}

	// Step 2: No tasks, register waiter and wait for notification
	waiterCtx, cancel := context.WithCancel(ctx)
	waiter := &TaskWaiter{
		agentID:    req.AgentID,
		resultChan: make(chan *HandlerResult, 1),
		ctx:        waiterCtx,
		cancel:     cancel,
	}

	h.waiters.Store(req.AgentID, waiter)
	defer func() {
		h.waiters.Delete(req.AgentID)
		cancel()
	}()

	// Step 3: Double-check after waiter registration (race condition prevention)
	hasChanges, result, err = h.CheckImmediate(ctx, req)
	if err != nil {
		return nil, err
	}
	if hasChanges {
		return result, nil
	}

	// Step 4: Wait for notification or timeout
	select {
	case result := <-waiter.resultChan:
		return result, nil
	case <-ctx.Done():
		return &HandlerResult{
			HasChanges: false,
			Response:   NoChangeResponse(LongPollTypeTask),
		}, nil
	}
}

// NotifyTaskSubmitted is called by the engine event listener when a task is submitted.
// It wakes up the appropriate waiters.
func (h *TaskPollHandlerEngine) NotifyTaskSubmitted(taskID string, targetAgentID string) {
	if !h.running.Load() {
		return
	}

	select {
	case h.notifyCh <- taskNotification{taskID: taskID, targetAgentID: targetAgentID}:
	default:
		h.logger.Warn("Notification channel full, dropping task notification",
			zap.String("task_id", taskID),
		)
	}
}

// dispatchNotifications processes task notification events and wakes up waiters.
func (h *TaskPollHandlerEngine) dispatchNotifications() {
	defer h.wg.Done()

	for {
		select {
		case <-h.stopCh:
			return
		case notif := <-h.notifyCh:
			h.handleNotification(notif)
		}
	}
}

func (h *TaskPollHandlerEngine) handleNotification(notif taskNotification) {
	if notif.targetAgentID == "" {
		// Global task — notify all waiters
		h.waiters.Range(func(key, value interface{}) bool {
			waiter := value.(*TaskWaiter)
			h.wakeWaiter(waiter, notif.taskID)
			return true
		})
	} else {
		// Agent-specific task — notify only the target agent's waiter
		if waiterVal, ok := h.waiters.Load(notif.targetAgentID); ok {
			waiter := waiterVal.(*TaskWaiter)
			h.wakeWaiter(waiter, notif.taskID)
		}
	}
}

// wakeWaiter sends a signal to a waiter indicating tasks are available.
// The waiter will re-check and claim tasks from the engine.
func (h *TaskPollHandlerEngine) wakeWaiter(waiter *TaskWaiter, taskID string) {
	// Attempt to claim a task for this agent
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	claimed, err := h.engine.ClaimTaskForAgent(ctx, waiter.agentID)
	if err != nil {
		h.logger.Warn("Failed to claim task for notified waiter",
			zap.String("agent_id", waiter.agentID),
			zap.String("task_id", taskID),
			zap.Error(err),
		)
		return
	}
	if claimed == nil {
		return // Task already claimed by someone else
	}

	result := &HandlerResult{
		HasChanges: true,
		Response:   NewTaskResponse(true, []*model.Task{claimed}, "new task available"),
	}

	select {
	case waiter.resultChan <- result:
		h.logger.Debug("Notified waiter of new task via engine",
			zap.String("agent_id", waiter.agentID),
			zap.String("task_id", claimed.ID),
		)
	default:
		// Channel full — waiter will get it on next check
	}
}

// GetWaiterCount returns the number of active waiters.
func (h *TaskPollHandlerEngine) GetWaiterCount() int {
	count := 0
	h.waiters.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}
