// Package broker defines the interface for task queue backends.
package broker

import (
	"context"
	"time"

	"github.com/erennakbas/strego/types"
)

// Broker defines the interface for task queue operations.
// The primary implementation uses Redis Streams.
type Broker interface {
	// Publish adds a task to the specified queue
	Publish(ctx context.Context, queue string, task *types.TaskProto) error

	// Subscribe starts consuming tasks from the specified queues
	// The handler is called for each task received
	Subscribe(ctx context.Context, queues []string, handler TaskHandler) error

	// Ack acknowledges a task as successfully processed
	Ack(ctx context.Context, queue string, msgID string) error

	// Schedule adds a task to be processed at a specific time
	Schedule(ctx context.Context, task *types.TaskProto, processAt time.Time) error

	// EnqueueScheduled atomically moves ready scheduled tasks to their queues.
	// Returns the number of tasks moved. Uses Lua script for atomicity.
	EnqueueScheduled(ctx context.Context, until time.Time, limit int64) (int64, error)

	// Retry schedules a task for retry with backoff
	Retry(ctx context.Context, queue string, task *types.TaskProto, delay time.Duration) error

	// EnqueueRetry atomically moves ready retry tasks to their queues.
	// Returns the number of tasks moved. Uses Lua script for atomicity.
	EnqueueRetry(ctx context.Context, until time.Time, limit int64) (int64, error)

	// MoveToDLQ moves a failed task to the dead letter queue
	MoveToDLQ(ctx context.Context, queue string, task *types.TaskProto, err error) error

	// GetDLQ retrieves tasks from the dead letter queue
	GetDLQ(ctx context.Context, queue string, limit int64) ([]*types.TaskProto, error)

	// RetryFromDLQ moves a task from DLQ back to the main queue
	RetryFromDLQ(ctx context.Context, queue string, taskID string) error

	// SetProcessed marks a task as processed for idempotency
	SetProcessed(ctx context.Context, taskID string, ttl time.Duration) (bool, error)

	// SetUnique sets a unique key for deduplication
	SetUnique(ctx context.Context, uniqueKey string, taskID string, ttl time.Duration) (bool, error)

	// GetQueueInfo returns statistics for a queue
	GetQueueInfo(ctx context.Context, queue string) (*types.QueueInfo, error)

	// GetQueues returns all known queue names
	GetQueues(ctx context.Context) ([]string, error)

	// GetConsumerGroups returns all consumer groups for a queue
	GetConsumerGroups(ctx context.Context, queue string) ([]*types.ConsumerGroupInfo, error)

	// GetGroupConsumers returns all consumers in a consumer group
	GetGroupConsumers(ctx context.Context, queue, group string) ([]*types.ConsumerInfo, error)

	// RemoveConsumer removes a consumer from a consumer group
	// This is safe to call when the consumer has no pending tasks
	RemoveConsumer(ctx context.Context, queue, group, consumer string) error

	// PurgeQueue removes all tasks from a queue
	PurgeQueue(ctx context.Context, queue string) error

	// PurgeDLQ removes all tasks from the dead letter queue
	PurgeDLQ(ctx context.Context, queue string) error

	// Ping checks if the broker is healthy
	Ping(ctx context.Context) error

	// Close closes the broker connection
	Close() error
}

// TaskHandler is called when a task is received from the queue
type TaskHandler func(ctx context.Context, task *types.TaskProto) error

// ConsumerConfig configures the consumer behavior
type ConsumerConfig struct {
	// Group is the consumer group name
	Group string

	// Consumer is the unique consumer name within the group
	Consumer string

	// BatchSize is the number of tasks to fetch at once
	BatchSize int64

	// BlockDuration is how long to wait for new tasks
	BlockDuration time.Duration

	// ClaimStaleAfter is the duration after which stale tasks are claimed
	ClaimStaleAfter time.Duration

	// ClaimCheckInterval is how often to check for stale tasks (background)
	ClaimCheckInterval time.Duration
}

// DefaultConsumerConfig returns a default consumer configuration
func DefaultConsumerConfig() ConsumerConfig {
	return ConsumerConfig{
		Group:              "strego-workers",
		Consumer:           "",
		BatchSize:          10,
		BlockDuration:      5 * time.Second,
		ClaimStaleAfter:    5 * time.Minute,
		ClaimCheckInterval: 30 * time.Second,
	}
}
