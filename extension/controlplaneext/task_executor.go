// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"container/heap"
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// TaskHandler defines the interface for task handlers.
type TaskHandler interface {
	// Type returns the task type this handler supports.
	Type() string
	// Execute executes the task and returns the result.
	Execute(ctx context.Context, task *model.Task) (*model.TaskResult, error)
}

// TaskExecutor manages task execution.
type TaskExecutor struct {
	logger *zap.Logger
	config TaskExecutorConfig

	mu           sync.RWMutex
	handlers     map[string]TaskHandler
	taskQueue    *taskPriorityQueue
	results      map[string]*model.TaskResult
	pendingTasks map[string]*model.Task

	// Completed results buffer for status reporting
	completedMu      sync.Mutex
	completedResults []*model.TaskResult

	// Lifecycle
	workers  int
	stopChan chan struct{}
	wg       sync.WaitGroup
	started  bool
}

// newTaskExecutor creates a new task executor.
func newTaskExecutor(logger *zap.Logger, config TaskExecutorConfig) *TaskExecutor {
	workers := config.Workers
	if workers <= 0 {
		workers = 4
	}

	te := &TaskExecutor{
		logger:           logger,
		config:           config,
		handlers:         make(map[string]TaskHandler),
		taskQueue:        &taskPriorityQueue{},
		results:          make(map[string]*model.TaskResult),
		pendingTasks:     make(map[string]*model.Task),
		completedResults: make([]*model.TaskResult, 0),
		workers:          workers,
		stopChan:         make(chan struct{}),
	}

	heap.Init(te.taskQueue)

	// Register built-in handlers
	te.registerBuiltinHandlers()

	return te
}

// registerBuiltinHandlers registers the built-in task handlers.
func (e *TaskExecutor) registerBuiltinHandlers() {
	e.RegisterHandler(&heapDumpHandler{logger: e.logger})
	e.RegisterHandler(&threadDumpHandler{logger: e.logger})
	e.RegisterHandler(&configExportHandler{logger: e.logger})
}

// RegisterHandler registers a task handler.
func (e *TaskExecutor) RegisterHandler(handler TaskHandler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[handler.Type()] = handler
	e.logger.Debug("Registered task handler", zap.String("type", handler.Type()))
}

// Start starts the task executor workers.
func (e *TaskExecutor) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.started {
		return nil
	}

	e.logger.Info("Starting task executor", zap.Int("workers", e.workers))

	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker(i)
	}

	e.started = true
	return nil
}

// Shutdown stops the task executor.
func (e *TaskExecutor) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return nil
	}
	e.started = false
	e.mu.Unlock()

	close(e.stopChan)

	// Wait for workers with timeout
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		e.logger.Info("Task executor shutdown complete")
	case <-ctx.Done():
		e.logger.Warn("Task executor shutdown timed out")
	}

	return nil
}

// Submit submits a task for execution.
func (e *TaskExecutor) Submit(ctx context.Context, task *model.Task) error {
	if task == nil {
		return errors.New("task cannot be nil")
	}

	if task.ID == "" {
		return errors.New("task_id is required")
	}

	if task.TypeName == "" {
		return errors.New("task_type is required")
	}

	// Check if task is expired
	if task.ExpiresAtMillis > 0 && time.Now().UnixMilli() > task.ExpiresAtMillis {
		return errors.New("task has expired")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Check if handler exists
	if _, ok := e.handlers[task.TypeName]; !ok {
		return errors.New("no handler registered for task type: " + task.TypeName)
	}

	// Check for duplicate
	if _, exists := e.pendingTasks[task.ID]; exists {
		return errors.New("task already exists: " + task.ID)
	}

	// Add to queue
	e.pendingTasks[task.ID] = task
	heap.Push(e.taskQueue, &taskItem{task: task})

	e.logger.Debug("Task submitted",
		zap.String("task_id", task.ID),
		zap.String("task_type", task.TypeName),
		zap.Int32("priority", task.PriorityNum),
	)

	return nil
}

// GetResult returns the result of a task.
func (e *TaskExecutor) GetResult(taskID string) (*model.TaskResult, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result, ok := e.results[taskID]
	return result, ok
}

// GetPendingTasks returns all pending tasks.
func (e *TaskExecutor) GetPendingTasks() []*model.Task {
	e.mu.RLock()
	defer e.mu.RUnlock()

	tasks := make([]*model.Task, 0, len(e.pendingTasks))
	for _, task := range e.pendingTasks {
		tasks = append(tasks, task)
	}
	return tasks
}

// DrainCompletedResults returns and clears the completed results buffer.
func (e *TaskExecutor) DrainCompletedResults() []*model.TaskResult {
	e.completedMu.Lock()
	defer e.completedMu.Unlock()

	results := e.completedResults
	e.completedResults = make([]*model.TaskResult, 0)
	return results
}

// worker is the task execution worker goroutine.
func (e *TaskExecutor) worker(id int) {
	defer e.wg.Done()

	e.logger.Debug("Task worker started", zap.Int("worker_id", id))

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-e.stopChan:
			e.logger.Debug("Task worker stopping", zap.Int("worker_id", id))
			return
		case <-ticker.C:
			e.processNextTask(id)
		}
	}
}

// processNextTask processes the next task in the queue.
func (e *TaskExecutor) processNextTask(workerID int) {
	e.mu.Lock()
	if e.taskQueue.Len() == 0 {
		e.mu.Unlock()
		return
	}

	item := heap.Pop(e.taskQueue).(*taskItem)
	task := item.task
	delete(e.pendingTasks, task.ID)

	handler, ok := e.handlers[task.TypeName]
	e.mu.Unlock()

	if !ok {
		e.storeResult(&model.TaskResult{
			TaskID:            task.ID,
			Status:            model.TaskStatusFailed,
			ErrorMessage:      "no handler for task type: " + task.TypeName,
			CompletedAtMillis: time.Now().UnixMilli(),
		})
		return
	}

	// Check expiration
	if task.ExpiresAtMillis > 0 && time.Now().UnixMilli() > task.ExpiresAtMillis {
		e.storeResult(&model.TaskResult{
			TaskID:            task.ID,
			Status:            model.TaskStatusTimeout,
			ErrorMessage:      "task expired before execution",
			CompletedAtMillis: time.Now().UnixMilli(),
		})
		return
	}

	// Execute with timeout
	timeout := time.Duration(task.TimeoutMillis) * time.Millisecond
	if timeout <= 0 {
		timeout = e.config.DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	e.logger.Debug("Executing task",
		zap.Int("worker_id", workerID),
		zap.String("task_id", task.ID),
		zap.String("task_type", task.TypeName),
	)

	result, err := handler.Execute(ctx, task)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result = &model.TaskResult{
				TaskID:            task.ID,
				Status:            model.TaskStatusTimeout,
				ErrorMessage:      "task execution timed out",
				CompletedAtMillis: time.Now().UnixMilli(),
			}
		} else {
			result = &model.TaskResult{
				TaskID:            task.ID,
				Status:            model.TaskStatusFailed,
				ErrorMessage:      err.Error(),
				CompletedAtMillis: time.Now().UnixMilli(),
			}
		}
	}

	e.storeResult(result)

	e.logger.Debug("Task completed",
		zap.String("task_id", task.ID),
		zap.String("status", string(result.Status)),
	)
}

// storeResult stores a task result.
func (e *TaskExecutor) storeResult(result *model.TaskResult) {
	e.mu.Lock()
	e.results[result.TaskID] = result
	e.mu.Unlock()

	e.completedMu.Lock()
	e.completedResults = append(e.completedResults, result)
	e.completedMu.Unlock()
}

// taskItem wraps a task for the priority queue.
type taskItem struct {
	task  *model.Task
	index int
}

// taskPriorityQueue implements heap.Interface for task priority.
type taskPriorityQueue []*taskItem

func (pq taskPriorityQueue) Len() int { return len(pq) }

func (pq taskPriorityQueue) Less(i, j int) bool {
	// Higher priority first
	if pq[i].task.PriorityNum != pq[j].task.PriorityNum {
		return pq[i].task.PriorityNum > pq[j].task.PriorityNum
	}
	// Earlier creation time first (FIFO for same priority)
	return pq[i].task.CreatedAtMillis < pq[j].task.CreatedAtMillis
}

func (pq taskPriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *taskPriorityQueue) Push(x any) {
	n := len(*pq)
	item := x.(*taskItem)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *taskPriorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*pq = old[0 : n-1]
	return item
}
