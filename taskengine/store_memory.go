// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// MemoryStore implements Store using in-memory data structures.
// Suitable for single-node deployments and testing.
//
// Thread-safe via sync.RWMutex.
type MemoryStore struct {
	mu      sync.RWMutex
	tasks   map[string]*Task       // taskID → Task
	results map[string]*TaskResult // taskID → TaskResult
	queues  map[string][]string    // queueID → []taskID (append left, pop right)
	groups  map[string][]string    // groupID → []taskID

	// Event subscribers for in-process fan-out.
	subscribers []*memorySubscriber
}

// memorySubscriber represents an event subscriber in MemoryStore.
type memorySubscriber struct {
	ch     chan TaskEvent
	active atomic.Bool
}

// NewMemoryStore creates an in-memory Store implementation.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tasks:   make(map[string]*Task),
		results: make(map[string]*TaskResult),
		queues:  make(map[string][]string),
		groups:  make(map[string][]string),
	}
}

// ─── Task CRUD ───

func (s *MemoryStore) SaveTask(_ context.Context, task *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[task.ID]; exists {
		return fmt.Errorf("task %s already exists", task.ID)
	}

	// Deep copy to avoid external mutation
	copied := *task
	s.tasks[task.ID] = &copied

	if task.GroupID != "" {
		s.groups[task.GroupID] = append(s.groups[task.GroupID], task.ID)
	}
	return nil
}

func (s *MemoryStore) GetTask(_ context.Context, taskID string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return nil, nil
	}
	copied := *task
	return &copied, nil
}

func (s *MemoryStore) GetTasks(_ context.Context, taskIDs []string) ([]*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tasks := make([]*Task, 0, len(taskIDs))
	for _, id := range taskIDs {
		if task, ok := s.tasks[id]; ok {
			copied := *task
			tasks = append(tasks, &copied)
		}
	}
	return tasks, nil
}

func (s *MemoryStore) UpdateTaskStatus(_ context.Context, taskID string, status TaskStatus, claimedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}

	if err := ValidateTransition(task.Status, status); err != nil {
		return err
	}

	task.Status = status
	if claimedBy != "" {
		task.ClaimedBy = claimedBy
	}
	return nil
}

func (s *MemoryStore) DeleteTask(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return nil
	}

	// Remove from group
	if task.GroupID != "" {
		s.removeFromSlice(task.GroupID, taskID, s.groups)
	}

	// Remove from all queues
	for queueID := range s.queues {
		s.removeTaskFromQueue(queueID, taskID)
	}

	delete(s.tasks, taskID)
	delete(s.results, taskID)
	return nil
}

func (s *MemoryStore) ListTasks(_ context.Context, query ListQuery) (*ListPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	var candidates []*Task

	if query.GroupID != "" {
		// Filter by group
		ids := s.groups[query.GroupID]
		for _, id := range ids {
			if task, ok := s.tasks[id]; ok {
				candidates = append(candidates, task)
			}
		}
	} else {
		for _, task := range s.tasks {
			candidates = append(candidates, task)
		}
	}

	// Apply filters
	var filtered []*Task
	for _, task := range candidates {
		if query.TaskType != "" && task.Type != query.TaskType {
			continue
		}
		if query.Status != "" && task.Status != query.Status {
			continue
		}
		filtered = append(filtered, task)
	}

	total := len(filtered)
	start := query.Offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	// Copy results
	page := make([]*Task, end-start)
	for i, task := range filtered[start:end] {
		copied := *task
		page[i] = &copied
	}

	return &ListPage{
		Tasks:  page,
		Total:  total,
		Offset: query.Offset,
		Limit:  limit,
	}, nil
}

// ─── Queue Operations ───

func (s *MemoryStore) Enqueue(_ context.Context, queueID string, taskID string, _ int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// LPUSH equivalent: prepend to slice
	s.queues[queueID] = append([]string{taskID}, s.queues[queueID]...)
	return nil
}

func (s *MemoryStore) Dequeue(_ context.Context, queueIDs []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, queueID := range queueIDs {
		queue := s.queues[queueID]
		if len(queue) == 0 {
			continue
		}
		// RPOP equivalent: pop from end
		taskID := queue[len(queue)-1]
		s.queues[queueID] = queue[:len(queue)-1]
		return taskID, nil
	}
	return "", nil
}

func (s *MemoryStore) RemoveFromQueue(_ context.Context, queueID string, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.removeTaskFromQueue(queueID, taskID)
	return nil
}

// ─── Result Storage ───

func (s *MemoryStore) SaveResult(_ context.Context, result *TaskResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	copied := *result
	s.results[result.TaskID] = &copied
	return nil
}

func (s *MemoryStore) GetResult(_ context.Context, taskID string) (*TaskResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result, ok := s.results[taskID]
	if !ok {
		return nil, nil
	}
	copied := *result
	return &copied, nil
}

// ─── Progress ───

// GetProgress computes task progress directly from in-memory structures.
// Mirrors the optimized RedisStore approach: iterates group members and
// aggregates status counts without copying full Task objects.
func (s *MemoryStore) GetProgress(_ context.Context, taskType TaskType, groupID string) (*Progress, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine candidate task IDs
	var candidates []string
	if groupID != "" {
		candidates = s.groups[groupID]
	} else {
		// No group filter — iterate all tasks
		candidates = make([]string, 0, len(s.tasks))
		for id := range s.tasks {
			candidates = append(candidates, id)
		}
	}

	p := &Progress{}
	for _, id := range candidates {
		task, ok := s.tasks[id]
		if !ok {
			continue
		}
		// Apply taskType filter
		if taskType != "" && task.Type != taskType {
			continue
		}

		p.Total++
		switch task.Status {
		case StatusPending:
			p.Pending++
		case StatusRunning:
			p.Running++
		case StatusSuccess, StatusSkipped:
			p.Completed++
		case StatusFailed:
			p.Failed++
		case StatusTimeout:
			p.Timeout++
		case StatusCancelled:
			p.Cancelled++
		}
	}
	return p, nil
}

// ─── Events ───

// PublishEvent fans out the event to all active in-process subscribers.
// Uses recover to guard against write to a closed channel (race with unsubscribe).
func (s *MemoryStore) PublishEvent(_ context.Context, event TaskEvent) error {
	s.mu.RLock()
	// Take a snapshot to avoid holding the lock during sends.
	subs := make([]*memorySubscriber, len(s.subscribers))
	copy(subs, s.subscribers)
	s.mu.RUnlock()

	for _, sub := range subs {
		func() {
			defer func() { recover() }() // safety net for closed channel
			if !sub.active.Load() {
				return
			}
			select {
			case sub.ch <- event:
			default:
				// Channel full, skip — subscriber will pick up via timeout poll.
			}
		}()
	}
	return nil
}

// SubscribeEvents creates an in-process event subscription.
// The returned channel receives events from PublishEvent calls on this store.
// The channel is closed when ctx is cancelled.
func (s *MemoryStore) SubscribeEvents(ctx context.Context) (<-chan TaskEvent, error) {
	ch := make(chan TaskEvent, 256)
	sub := &memorySubscriber{ch: ch}
	sub.active.Store(true)

	s.mu.Lock()
	s.subscribers = append(s.subscribers, sub)
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		// Mark inactive first to prevent new writes.
		sub.active.Store(false)
		// Remove from list so future PublishEvent skip it entirely.
		s.mu.Lock()
		for i, existing := range s.subscribers {
			if existing == sub {
				s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		close(ch)
	}()

	return ch, nil
}

// ─── Reaper Optimized Path ───

// GetOverdueRunningTasks returns IDs of running tasks whose deadline has passed.
// Iterates in-memory map and filters by createdAt + timeout < nowMillis.
func (s *MemoryStore) GetOverdueRunningTasks(_ context.Context, nowMillis int64) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var overdueIDs []string
	for _, task := range s.tasks {
		if task.Status != StatusRunning {
			continue
		}
		// Compute deadline: createdAt + timeout (in millis)
		timeoutMs := task.Timeout.Milliseconds()
		if timeoutMs == 0 {
			timeoutMs = 120000 // default 120s
		}
		deadline := task.CreatedAt + timeoutMs
		if deadline <= nowMillis {
			overdueIDs = append(overdueIDs, task.ID)
		}
		// Backpressure: max 500 per call
		if len(overdueIDs) >= 500 {
			break
		}
	}
	return overdueIDs, nil
}

// ─── Metadata-Only Access ───

// GetTaskMeta retrieves only the metadata of a task (no Payload).
func (s *MemoryStore) GetTaskMeta(_ context.Context, taskID string) (*TaskMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return nil, nil
	}
	return taskToMeta(task), nil
}

// GetTasksMeta retrieves metadata for multiple tasks in batch.
func (s *MemoryStore) GetTasksMeta(_ context.Context, taskIDs []string) ([]*TaskMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	metas := make([]*TaskMeta, 0, len(taskIDs))
	for _, id := range taskIDs {
		if task, ok := s.tasks[id]; ok {
			metas = append(metas, taskToMeta(task))
		}
	}
	return metas, nil
}

// ─── Lifecycle ───

func (s *MemoryStore) Start(_ context.Context) error { return nil }
func (s *MemoryStore) Close() error                  { return nil }

// ─── Internal helpers ───

func (s *MemoryStore) removeTaskFromQueue(queueID, taskID string) {
	queue := s.queues[queueID]
	for i, id := range queue {
		if id == taskID {
			s.queues[queueID] = append(queue[:i], queue[i+1:]...)
			return
		}
	}
}

func (s *MemoryStore) removeFromSlice(key, value string, m map[string][]string) {
	slice := m[key]
	for i, v := range slice {
		if v == value {
			m[key] = append(slice[:i], slice[i+1:]...)
			return
		}
	}
}
