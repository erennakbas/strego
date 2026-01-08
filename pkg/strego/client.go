package strego

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/erennakbas/strego/internal/proto"
	"github.com/erennakbas/strego/pkg/broker"
)

// ErrDuplicateTask is returned when a unique task already exists.
var ErrDuplicateTask = errors.New("duplicate task: unique key already exists")

// Client is used to enqueue tasks for processing.
type Client struct {
	broker broker.Broker
	store  Store
	logger *slog.Logger
}

// ClientOption configures the client.
type ClientOption func(*Client)

// WithStore sets the optional store for task persistence.
func WithStore(store Store) ClientOption {
	return func(c *Client) {
		c.store = store
	}
}

// WithLogger sets the logger.
func WithLogger(logger *slog.Logger) ClientOption {
	return func(c *Client) {
		c.logger = logger
	}
}

// NewClient creates a new task queue client.
func NewClient(b broker.Broker, opts ...ClientOption) *Client {
	c := &Client{
		broker: b,
		logger: slog.Default(),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Enqueue adds a task to the queue for processing.
// If the task has a ProcessAt option set, it will be scheduled for later.
func (c *Client) Enqueue(ctx context.Context, task *Task) (*TaskInfo, error) {
	if task == nil {
		return nil, errors.New("task cannot be nil")
	}

	pbTask := task.Proto()

	// Handle unique task deduplication
	if pbTask.Options != nil && pbTask.Options.UniqueKey != "" {
		ttl := time.Hour // default
		if pbTask.Options.UniqueTtl != nil {
			ttl = pbTask.Options.UniqueTtl.AsDuration()
		}

		ok, err := c.broker.SetUnique(ctx, pbTask.Options.UniqueKey, pbTask.Id, ttl)
		if err != nil {
			return nil, fmt.Errorf("failed to check uniqueness: %w", err)
		}
		if !ok {
			return nil, ErrDuplicateTask
		}
	}

	// Check if this is a scheduled task
	isScheduled := pbTask.Options != nil &&
		pbTask.Options.ProcessAt != nil &&
		pbTask.Options.ProcessAt.AsTime().After(time.Now())

	queue := "default"
	if pbTask.Options != nil && pbTask.Options.Queue != "" {
		queue = pbTask.Options.Queue
	}

	if isScheduled {
		// Schedule for later
		pbTask.Metadata.State = pb.TaskState_TASK_STATE_SCHEDULED
		if err := c.broker.Schedule(ctx, pbTask, pbTask.Options.ProcessAt.AsTime()); err != nil {
			return nil, fmt.Errorf("failed to schedule task: %w", err)
		}
	} else {
		// Enqueue immediately
		pbTask.Metadata.State = pb.TaskState_TASK_STATE_PENDING
		if err := c.broker.Publish(ctx, queue, pbTask); err != nil {
			return nil, fmt.Errorf("failed to enqueue task: %w", err)
		}
	}

	// Save to store (sync, fail-safe)
	if c.store != nil {
		if err := c.store.CreateTask(ctx, pbTask); err != nil {
			c.logger.Warn("failed to save task to store",
				"task_id", pbTask.Id,
				"error", err)
			// Continue - Redis is primary
		}
	}

	info := &TaskInfo{
		ID:    pbTask.Id,
		Queue: queue,
		State: TaskState(pbTask.Metadata.State),
	}

	if isScheduled {
		info.ProcessAt = pbTask.Options.ProcessAt.AsTime()
	}

	return info, nil
}

// EnqueueUnique adds a task with a unique key for deduplication.
// If a task with the same key already exists within the TTL, ErrDuplicateTask is returned.
func (c *Client) EnqueueUnique(ctx context.Context, task *Task, uniqueKey string, ttl time.Duration) (*TaskInfo, error) {
	if task == nil {
		return nil, errors.New("task cannot be nil")
	}

	// Set the unique key option
	pbTask := task.Proto()
	if pbTask.Options == nil {
		pbTask.Options = &pb.TaskOptions{}
	}
	pbTask.Options.UniqueKey = uniqueKey

	return c.Enqueue(ctx, task)
}

// Schedule adds a task to be processed at a specific time.
func (c *Client) Schedule(ctx context.Context, task *Task, processAt time.Time) (*TaskInfo, error) {
	if task == nil {
		return nil, errors.New("task cannot be nil")
	}

	// Apply the schedule option
	WithProcessAt(processAt)(task)

	return c.Enqueue(ctx, task)
}

// Cancel cancels a pending or scheduled task.
func (c *Client) Cancel(ctx context.Context, taskID string) error {
	// For now, we can only update the store state
	// Cancelling a task in Redis Streams requires tracking it differently
	if c.store != nil {
		return c.store.UpdateTaskState(ctx, taskID, "cancelled", "")
	}
	return errors.New("cancel requires a store to be configured")
}

// GetTask retrieves information about a task.
// Requires a store to be configured.
func (c *Client) GetTask(ctx context.Context, taskID string) (*Task, error) {
	if c.store == nil {
		return nil, errors.New("GetTask requires a store to be configured")
	}

	pbTask, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	return TaskFromProto(pbTask), nil
}

// GetQueueInfo returns statistics for a queue.
func (c *Client) GetQueueInfo(ctx context.Context, queue string) (*pb.QueueInfo, error) {
	return c.broker.GetQueueInfo(ctx, queue)
}

// GetQueues returns all known queue names.
func (c *Client) GetQueues(ctx context.Context) ([]string, error) {
	return c.broker.GetQueues(ctx)
}

// Ping checks if the broker is healthy.
func (c *Client) Ping(ctx context.Context) error {
	return c.broker.Ping(ctx)
}

// Close closes the client and releases resources.
func (c *Client) Close() error {
	return c.broker.Close()
}
