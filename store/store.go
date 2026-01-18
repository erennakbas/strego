// Package store defines the interface for task persistence.
package store

import (
	"context"
	"time"

	"github.com/erennakbas/strego/types"
)

// Store defines the interface for task persistence.
// This is optional and used for task history, search, and UI features.
type Store interface {
	// CreateTask saves a new task to the store.
	CreateTask(ctx context.Context, task *types.TaskProto) error

	// UpdateTask updates an existing task.
	UpdateTask(ctx context.Context, task *types.TaskProto) error

	// UpdateTaskState updates only the state and error of a task.
	UpdateTaskState(ctx context.Context, taskID, state, errMsg string) error

	// GetTask retrieves a task by ID.
	GetTask(ctx context.Context, taskID string) (*types.TaskProto, error)

	// ListTasks retrieves tasks matching the filter.
	ListTasks(ctx context.Context, filter TaskFilter) ([]*types.TaskProto, int64, error)

	// CountTasks returns the count of tasks matching the filter.
	CountTasks(ctx context.Context, filter TaskFilter) (int64, error)

	// DeleteTask removes a task from the store.
	DeleteTask(ctx context.Context, taskID string) error

	// DeleteOldTasks removes tasks older than the specified duration.
	DeleteOldTasks(ctx context.Context, olderThan time.Duration) (int64, error)

	// GetStats returns task statistics.
	GetStats(ctx context.Context) (*Stats, error)

	// GetQueueStats returns statistics for a specific queue.
	GetQueueStats(ctx context.Context, queue string) (*QueueStats, error)

	// Ping checks if the store is healthy.
	Ping(ctx context.Context) error

	// Close closes the store connection.
	Close() error
}

// TaskFilter specifies criteria for listing tasks.
type TaskFilter struct {
	Queue         string
	State         string   // Single state (deprecated, use States)
	States        []string // Multiple states filter
	Type          string
	Search        string // Search in task ID or type
	Labels        map[string]string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	Limit         int
	Offset        int
	SortBy        string // "created_at", "started_at", "completed_at", "scheduled_at"
	SortOrder     string // "asc" or "desc"
}

// Stats contains overall task statistics.
type Stats struct {
	TotalTasks     int64
	PendingTasks   int64
	ActiveTasks    int64
	CompletedTasks int64
	FailedTasks    int64
	DeadTasks      int64
	RetryTasks     int64
	ScheduledTasks int64
}

// QueueStats contains statistics for a specific queue.
type QueueStats struct {
	Queue          string
	TotalTasks     int64
	PendingTasks   int64
	ActiveTasks    int64
	CompletedTasks int64
	FailedTasks    int64
	DeadTasks      int64
	RetryTasks     int64
	ScheduledTasks int64
	AvgDuration    float64
}
