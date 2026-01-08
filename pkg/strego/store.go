package strego

import (
	"context"

	"github.com/erennakbas/strego/pkg/types"
)

// Store defines the interface for task persistence.
// This is optional and used for task history, search, and UI features.
// See pkg/store for the full interface and pkg/store/postgres for the PostgreSQL implementation.
type Store interface {
	// CreateTask saves a new task to the store.
	CreateTask(ctx context.Context, task *types.TaskProto) error

	// UpdateTask updates an existing task.
	UpdateTask(ctx context.Context, task *types.TaskProto) error

	// UpdateTaskState updates only the state and error of a task.
	UpdateTaskState(ctx context.Context, taskID, state, errMsg string) error

	// GetTask retrieves a task by ID.
	GetTask(ctx context.Context, taskID string) (*types.TaskProto, error)

	// Close closes the store connection.
	Close() error
}
