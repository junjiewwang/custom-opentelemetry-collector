// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"context"
	"fmt"
	"sync"
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

func (s *MemoryStore) GetProgress(ctx context.Context, taskType TaskType, groupID string) (*Progress, error) {
	page, err := s.ListTasks(ctx, ListQuery{
		TaskType: taskType,
		GroupID:  groupID,
		Limit:    100000,
	})
	if err != nil {
		return nil, err
	}

	p := &Progress{Total: page.Total}
	for _, task := range page.Tasks {
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

// PublishEvent is a no-op for MemoryStore (no external subscribers).
func (s *MemoryStore) PublishEvent(_ context.Context, _ TaskEvent) error {
	return nil
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
