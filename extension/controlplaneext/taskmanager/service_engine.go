// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/taskengine"
)

// TaskServiceEngine implements the TaskManager interface by delegating to the
// unified taskengine.Engine. It serves as a backward-compatible Facade during
// the migration from the legacy store-based implementation.
//
// Design decisions:
//   - The TaskManager interface remains unchanged for all existing callers.
//   - Model conversion is performed at the boundary (this struct).
//   - FetchTask uses a polling loop over Engine.Claim() to provide blocking semantics.
//   - The helper is reused for validation and utility functions.
//   - StaleTaskReaperEngine is integrated for timeout detection.
type TaskServiceEngine struct {
	engine taskengine.Engine
	logger *zap.Logger
	config Config
	helper *TaskHelper
	reaper *StaleTaskReaperEngine
}

// NewTaskServiceEngine creates a new engine-backed TaskManager implementation.
func NewTaskServiceEngine(engine taskengine.Engine, logger *zap.Logger, config Config) *TaskServiceEngine {
	return &TaskServiceEngine{
		engine: engine,
		logger: logger,
		config: config,
		helper: NewTaskHelper(),
		reaper: NewStaleTaskReaperEngine(logger.Named("engine-reaper"), config.StaleTaskReaper, engine),
	}
}

// Ensure TaskServiceEngine implements TaskManager at compile time.
var _ TaskManager = (*TaskServiceEngine)(nil)

// ═══════════════════════════════════════════════════
// Producer API
// ═══════════════════════════════════════════════════

// SubmitTask submits a task to the global queue.
func (s *TaskServiceEngine) SubmitTask(ctx context.Context, task *model.Task) error {
	return s.submitTaskInternal(ctx, nil, task)
}

// SubmitTaskForAgent submits a task for a specific agent.
func (s *TaskServiceEngine) SubmitTaskForAgent(ctx context.Context, agentMeta *AgentMeta, task *model.Task) error {
	return s.submitTaskInternal(ctx, agentMeta, task)
}

func (s *TaskServiceEngine) submitTaskInternal(ctx context.Context, agentMeta *AgentMeta, task *model.Task) error {
	// Step 1: Validate task (reuse existing helper)
	if _, err := s.helper.ValidateTask(task); err != nil {
		return err
	}

	// Step 2: Determine routing — agentMeta overrides task.TargetAgentID
	agentID := s.helper.ExtractAgentID(agentMeta)
	if agentID != "" {
		task.TargetAgentID = agentID
	}

	// Step 3: Convert to engine model
	engineTask := controlplaneTaskToEngine(task)

	// Step 4: Attach metadata for agent context
	if agentMeta != nil {
		if engineTask.Metadata == nil {
			engineTask.Metadata = make(map[string]string)
		}
		if agentMeta.AppID != "" {
			engineTask.Metadata["app_id"] = agentMeta.AppID
		}
		if agentMeta.ServiceName != "" {
			engineTask.Metadata["service_name"] = agentMeta.ServiceName
		}
		if agentMeta.AgentID != "" {
			engineTask.Metadata["agent_id"] = agentMeta.AgentID
		}
	}

	// Step 5: Submit to engine
	if err := s.engine.Submit(ctx, engineTask); err != nil {
		return fmt.Errorf("engine submit: %w", err)
	}

	s.logger.Debug("Task submitted via engine",
		zap.String("task_id", task.ID),
		zap.String("task_type", task.TypeName),
		zap.String("agent_id", agentID),
		zap.String("routing", string(engineTask.Routing.Strategy)),
	)
	return nil
}

// ═══════════════════════════════════════════════════
// Consumer API
// ═══════════════════════════════════════════════════

// FetchTask fetches the next task for an agent with blocking semantics.
// The engine's Claim() is non-blocking, so we implement a polling loop
// with exponential backoff to simulate the legacy blocking behavior.
func (s *TaskServiceEngine) FetchTask(ctx context.Context, agentID string, timeout time.Duration) (*model.Task, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required for FetchTask")
	}

	consumer := agentToConsumerDescriptor(agentID)
	deadline := time.Now().Add(timeout)

	// Polling parameters
	const (
		initialInterval = 100 * time.Millisecond
		maxInterval     = time.Second
	)
	pollInterval := initialInterval

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Try to claim a task
		engineTask, err := s.engine.Claim(ctx, consumer)
		if err != nil {
			return nil, fmt.Errorf("engine claim: %w", err)
		}
		if engineTask != nil {
			return engineTaskToControlplane(engineTask), nil
		}

		// No task available — sleep with backoff
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		sleepDuration := pollInterval
		if sleepDuration > remaining {
			sleepDuration = remaining
		}

		timer := time.NewTimer(sleepDuration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}

		// Exponential backoff up to maxInterval
		pollInterval *= 2
		if pollInterval > maxInterval {
			pollInterval = maxInterval
		}
	}

	return nil, nil // Timeout — no task available
}

// ═══════════════════════════════════════════════════
// Query API
// ═══════════════════════════════════════════════════

// GetPendingTasks returns all pending tasks for an agent.
func (s *TaskServiceEngine) GetPendingTasks(ctx context.Context, agentID string) ([]*model.Task, error) {
	// Query engine for pending tasks that are either targeted at this agent or in global queue
	page, err := s.engine.ListTasks(ctx, taskengine.ListQuery{
		Status: taskengine.StatusPending,
		Limit:  1000, // reasonable upper bound
	})
	if err != nil {
		return nil, fmt.Errorf("engine list tasks: %w", err)
	}
	if page == nil {
		return nil, nil
	}

	// Filter: tasks routed to this agent OR broadcast tasks
	var tasks []*model.Task
	for _, t := range page.Tasks {
		if t.Routing.Strategy == taskengine.RoutingDirect && t.Routing.TargetNodeID == agentID {
			tasks = append(tasks, engineTaskToControlplane(t))
		} else if t.Routing.Strategy == taskengine.RoutingBroadcast {
			tasks = append(tasks, engineTaskToControlplane(t))
		}
	}
	return tasks, nil
}

// GetGlobalPendingTasks returns all pending tasks in the global queue.
func (s *TaskServiceEngine) GetGlobalPendingTasks(ctx context.Context) ([]*model.Task, error) {
	page, err := s.engine.ListTasks(ctx, taskengine.ListQuery{
		Status: taskengine.StatusPending,
		Limit:  1000,
	})
	if err != nil {
		return nil, fmt.Errorf("engine list tasks: %w", err)
	}
	if page == nil {
		return nil, nil
	}

	// Filter: only broadcast tasks
	var tasks []*model.Task
	for _, t := range page.Tasks {
		if t.Routing.Strategy == taskengine.RoutingBroadcast {
			tasks = append(tasks, engineTaskToControlplane(t))
		}
	}
	return tasks, nil
}

// GetAllTasks returns all tasks from the engine.
func (s *TaskServiceEngine) GetAllTasks(ctx context.Context) ([]*TaskInfo, error) {
	page, err := s.engine.ListTasks(ctx, taskengine.ListQuery{
		Limit: 10000, // get all
	})
	if err != nil {
		return nil, fmt.Errorf("engine list tasks: %w", err)
	}
	if page == nil {
		return nil, nil
	}

	result := make([]*TaskInfo, 0, len(page.Tasks))
	for _, t := range page.Tasks {
		result = append(result, engineTaskToTaskInfo(t))
	}
	return result, nil
}

// ListTasks returns a filtered and paged task list.
func (s *TaskServiceEngine) ListTasks(ctx context.Context, query ListTasksQuery) (ListTasksPage, error) {
	// Build engine query
	engineQuery := taskengine.ListQuery{
		Limit: query.Limit,
	}

	// Map task type filter
	if query.TaskType != "" {
		engineQuery.TaskType = controlplaneTypeToEngine(query.TaskType)
	}

	// Status filter — engine ListQuery only supports single status,
	// so we need to handle multi-status by filtering client-side.
	engineStatuses := controlplaneStatusesToEngine(query.Statuses)

	// If single status, use it directly; otherwise fetch all and filter
	if len(engineStatuses) == 1 {
		engineQuery.Status = engineStatuses[0]
	}

	// Cursor-based pagination: interpret cursor as offset
	if query.Cursor != "" {
		// Parse cursor as offset for the engine's offset-based pagination
		var offset int
		if _, err := fmt.Sscanf(query.Cursor, "%d", &offset); err == nil {
			engineQuery.Offset = offset
		}
	}

	page, err := s.engine.ListTasks(ctx, engineQuery)
	if err != nil {
		return ListTasksPage{}, fmt.Errorf("engine list tasks: %w", err)
	}
	if page == nil {
		return ListTasksPage{}, nil
	}

	// Convert and apply additional filters
	items := make([]*TaskInfo, 0, len(page.Tasks))
	for _, t := range page.Tasks {
		info := engineTaskToTaskInfo(t)

		// Apply filters that engine doesn't natively support
		if !matchesListFilter(info, query, engineStatuses) {
			continue
		}

		items = append(items, info)
	}

	// Compute pagination metadata
	nextOffset := page.Offset + len(page.Tasks)
	hasMore := page.Total > nextOffset
	nextCursor := ""
	if hasMore {
		nextCursor = fmt.Sprintf("%d", nextOffset)
	}

	return ListTasksPage{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

// ═══════════════════════════════════════════════════
// Cancellation API
// ═══════════════════════════════════════════════════

// CancelTask cancels a task by ID.
func (s *TaskServiceEngine) CancelTask(ctx context.Context, taskID string) error {
	if err := s.engine.Cancel(ctx, taskID); err != nil {
		return fmt.Errorf("engine cancel: %w", err)
	}
	s.logger.Info("Task cancelled via engine", zap.String("task_id", taskID))
	return nil
}

// IsTaskCancelled checks if a task has been cancelled.
func (s *TaskServiceEngine) IsTaskCancelled(ctx context.Context, taskID string) (bool, error) {
	task, err := s.engine.GetTask(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("engine get task: %w", err)
	}
	if task == nil {
		return false, nil
	}
	return task.Status == taskengine.StatusCancelled, nil
}

// ═══════════════════════════════════════════════════
// Result API
// ═══════════════════════════════════════════════════

// ReportTaskResult reports the result of a task execution.
func (s *TaskServiceEngine) ReportTaskResult(ctx context.Context, result *model.TaskResult) error {
	// Validate
	if err := s.helper.ValidateResult(result); err != nil {
		return err
	}

	// Handle RUNNING status specially — engine doesn't have a Report for Running
	if result.Status == model.TaskStatusRunning {
		return s.handleRunningReport(ctx, result)
	}

	// Convert to engine result
	engineResult := controlplaneResultToEngine(result)

	// Report to engine
	if err := s.engine.Report(ctx, engineResult); err != nil {
		return fmt.Errorf("engine report: %w", err)
	}

	s.logger.Debug("Task result reported via engine",
		zap.String("task_id", result.TaskID),
		zap.Int32("status", int32(result.Status)),
		zap.String("agent_id", result.AgentID),
	)
	return nil
}

// handleRunningReport handles the special case where an agent reports RUNNING status.
// In the legacy system, this was used as a heartbeat/claim signal.
// The engine handles this via Claim(), but for backward compatibility we treat
// a RUNNING report as a no-op if the task is already running, or as a claim signal.
func (s *TaskServiceEngine) handleRunningReport(ctx context.Context, result *model.TaskResult) error {
	task, err := s.engine.GetTask(ctx, result.TaskID)
	if err != nil {
		return fmt.Errorf("engine get task: %w", err)
	}
	if task == nil {
		s.logger.Warn("RUNNING report for unknown task", zap.String("task_id", result.TaskID))
		return nil
	}

	// If task is already running, this is a heartbeat — no-op
	if task.Status == taskengine.StatusRunning {
		return nil
	}

	// If task is pending, simulate a claim by the reporting agent
	if task.Status == taskengine.StatusPending {
		consumer := agentToConsumerDescriptor(result.AgentID)
		_, claimErr := s.engine.Claim(ctx, consumer)
		if claimErr != nil {
			s.logger.Warn("Failed to claim task on RUNNING report",
				zap.String("task_id", result.TaskID),
				zap.Error(claimErr),
			)
		}
	}

	return nil
}

// GetTaskResult retrieves the result of a task.
func (s *TaskServiceEngine) GetTaskResult(ctx context.Context, taskID string) (*model.TaskResult, bool, error) {
	engineResult, err := s.engine.GetResult(ctx, taskID)
	if err != nil {
		return nil, false, fmt.Errorf("engine get result: %w", err)
	}
	if engineResult == nil {
		return nil, false, nil
	}
	return engineResultToControlplane(engineResult), true, nil
}

// ═══════════════════════════════════════════════════
// Status API
// ═══════════════════════════════════════════════════

// GetTaskStatus retrieves the status of a task.
func (s *TaskServiceEngine) GetTaskStatus(ctx context.Context, taskID string) (*TaskInfo, error) {
	task, err := s.engine.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("engine get task: %w", err)
	}
	if task == nil {
		return nil, s.helper.ErrTaskNotFound(taskID)
	}
	return engineTaskToTaskInfo(task), nil
}

// SetTaskRunning marks a task as running by an agent.
// In the engine model, this is equivalent to a Claim operation.
func (s *TaskServiceEngine) SetTaskRunning(ctx context.Context, taskID string, agentID string) error {
	task, err := s.engine.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("engine get task: %w", err)
	}
	if task == nil {
		return s.helper.ErrTaskNotFound(taskID)
	}

	// If already running by this agent, no-op
	if task.Status == taskengine.StatusRunning && task.ClaimedBy == agentID {
		return nil
	}

	// If still pending, simulate claim
	if task.Status == taskengine.StatusPending {
		consumer := agentToConsumerDescriptor(agentID)
		_, err := s.engine.Claim(ctx, consumer)
		if err != nil {
			return fmt.Errorf("engine claim for set-running: %w", err)
		}
		return nil
	}

	// If in a terminal state, reject
	if taskengine.TaskStatus(task.Status).IsTerminal() {
		return fmt.Errorf("cannot set task %s to RUNNING: already in terminal state %s", taskID, task.Status)
	}

	return nil
}

// ═══════════════════════════════════════════════════
// Lifecycle
// ═══════════════════════════════════════════════════

// Start initializes the engine-backed task service.
func (s *TaskServiceEngine) Start(ctx context.Context) error {
	s.logger.Info("Starting engine-backed task service")
	if err := s.engine.Start(ctx); err != nil {
		return err
	}
	// Start the engine-backed reaper for timeout detection
	return s.reaper.Start(ctx)
}

// Close releases resources.
func (s *TaskServiceEngine) Close() error {
	// Stop reaper first to avoid accessing a stopped engine
	if s.reaper != nil {
		s.reaper.Stop()
	}
	return s.engine.Stop(context.Background())
}

// GetEngine returns the underlying taskengine.Engine instance.
// This is used by components that need direct engine access (e.g., longpoll handler)
// to perform optimized operations like Claim without going through the Facade.
func (s *TaskServiceEngine) GetEngine() taskengine.Engine {
	return s.engine
}

// ═══════════════════════════════════════════════════
// Internal Helpers
// ═══════════════════════════════════════════════════

// engineTaskToTaskInfo converts an engine task to the legacy TaskInfo format.
func engineTaskToTaskInfo(task *taskengine.Task) *TaskInfo {
	if task == nil {
		return nil
	}

	info := &TaskInfo{
		Task:            engineTaskToControlplane(task),
		Status:          engineStatusToControlplane(task.Status),
		AgentID:         task.ClaimedBy,
		CreatedAtMillis: task.CreatedAt,
	}

	// Extract metadata if available
	if task.Metadata != nil {
		if appID, ok := task.Metadata["app_id"]; ok {
			info.AppID = appID
		}
		if svcName, ok := task.Metadata["service_name"]; ok {
			info.ServiceName = svcName
		}
		if agentID, ok := task.Metadata["agent_id"]; ok && info.AgentID == "" {
			info.AgentID = agentID
		}
	}

	// If routing is direct, set agent ID from routing target
	if task.Routing.Strategy == taskengine.RoutingDirect && info.AgentID == "" {
		info.AgentID = task.Routing.TargetNodeID
	}

	return info
}

// matchesListFilter applies controlplane-specific filters that the engine
// doesn't natively support (multi-status, appID, serviceName, agentID).
func matchesListFilter(info *TaskInfo, query ListTasksQuery, engineStatuses []taskengine.TaskStatus) bool {
	// Multi-status filter (if > 1 status specified and not already filtered by engine)
	if len(engineStatuses) > 1 {
		matched := false
		for _, s := range query.Statuses {
			if info.Status == s {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// AppID filter
	if query.AppID != "" && info.AppID != query.AppID {
		return false
	}

	// ServiceName filter
	if query.ServiceName != "" && info.ServiceName != query.ServiceName {
		return false
	}

	// AgentID filter
	if query.AgentID != "" && info.AgentID != query.AgentID {
		return false
	}

	return true
}
