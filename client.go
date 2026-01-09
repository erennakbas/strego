package strego

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/erennakbas/strego/broker"
	"github.com/erennakbas/strego/types"
)

// ErrDuplicateTask is returned when a unique task already exists.
var ErrDuplicateTask = errors.New("duplicate task: unique key already exists")

// Client is used to enqueue tasks for processing.
type Client struct {
	broker broker.Broker
	store  Store
	logger Logger
}

// ClientOption configures the client.
type ClientOption func(*Client)

// WithStore sets the optional store for task persistence.
func WithStore(store Store) ClientOption {
	return func(c *Client) {
		c.store = store
	}
}

// WithClientLogger sets the logger for the client.
func WithClientLogger(logger Logger) ClientOption {
	return func(c *Client) {
		c.logger = logger
	}
}

// NewClient creates a new task queue client.
func NewClient(b broker.Broker, opts ...ClientOption) *Client {
	c := &Client{
		broker: b,
		logger: defaultLogger(),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Enqueue adds a task to the queue for processing.
// If the task has a ProcessAt option set, it will be scheduled for later.
func (c *Client) Enqueue(ctx context.Context, task *Task) (*types.TaskInfo, error) {
	if task == nil {
		return nil, errors.New("task cannot be nil")
	}

	proto := task.Proto()

	// Handle unique task deduplication
	if proto.Options != nil && proto.Options.UniqueKey != "" {
		ttl := time.Hour // default
		if proto.Options.UniqueTTL > 0 {
			ttl = proto.Options.UniqueTTL
		}

		ok, err := c.broker.SetUnique(ctx, proto.Options.UniqueKey, proto.ID, ttl)
		if err != nil {
			return nil, fmt.Errorf("failed to check uniqueness: %w", err)
		}
		if !ok {
			return nil, ErrDuplicateTask
		}
	}

	// Check if this is a scheduled task
	isScheduled := proto.Options != nil &&
		proto.Options.ProcessAt != nil &&
		proto.Options.ProcessAt.After(time.Now())

	queue := "default"
	if proto.Options != nil && proto.Options.Queue != "" {
		queue = proto.Options.Queue
	}

	if isScheduled {
		// Schedule for later
		proto.Metadata.State = types.TaskStateScheduled
		if err := c.broker.Schedule(ctx, proto, *proto.Options.ProcessAt); err != nil {
			return nil, fmt.Errorf("failed to schedule task: %w", err)
		}
	} else {
		// Enqueue immediately
		proto.Metadata.State = types.TaskStatePending
		if err := c.broker.Publish(ctx, queue, proto); err != nil {
			return nil, fmt.Errorf("failed to enqueue task: %w", err)
		}
	}

	// Save to store (sync, fail-safe)
	if c.store != nil {
		if err := c.store.CreateTask(ctx, proto); err != nil {
			c.logger.WithField("task_id", proto.ID).WithError(err).Warn("failed to save task to store")
			// Continue - Redis is primary
		}
	}

	info := &types.TaskInfo{
		ID:    proto.ID,
		Queue: queue,
		State: proto.Metadata.State,
	}

	if isScheduled {
		info.ProcessAt = proto.Options.ProcessAt
	}

	return info, nil
}

// EnqueueUnique adds a task with a unique key for deduplication.
// If a task with the same key already exists within the TTL, ErrDuplicateTask is returned.
func (c *Client) EnqueueUnique(ctx context.Context, task *Task, uniqueKey string, ttl time.Duration) (*types.TaskInfo, error) {
	if task == nil {
		return nil, errors.New("task cannot be nil")
	}

	// Set the unique key option
	proto := task.Proto()
	if proto.Options == nil {
		proto.Options = &types.TaskOptions{}
	}
	proto.Options.UniqueKey = uniqueKey

	return c.Enqueue(ctx, task)
}

// Schedule adds a task to be processed at a specific time.
func (c *Client) Schedule(ctx context.Context, task *Task, processAt time.Time) (*types.TaskInfo, error) {
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

	proto, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	return TaskFromProto(proto), nil
}

// GetQueueInfo returns statistics for a queue.
func (c *Client) GetQueueInfo(ctx context.Context, queue string) (*types.QueueInfo, error) {
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
