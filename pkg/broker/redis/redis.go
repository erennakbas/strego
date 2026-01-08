// Package redis provides a Redis Streams implementation of the Broker interface.
package redis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	pb "github.com/erennakbas/strego/internal/proto"
	"github.com/erennakbas/strego/pkg/broker"
)

const (
	// Key prefixes
	streamPrefix    = "strego:stream:"
	dlqPrefix       = "strego:dlq:"
	scheduledKey    = "strego:scheduled"
	retryKey        = "strego:retry"
	processedPrefix = "strego:processed:"
	uniquePrefix    = "strego:unique:"
	statsPrefix     = "strego:stats:"
	queuesKey       = "strego:queues"
)

// Broker implements the broker.Broker interface using Redis Streams.
type Broker struct {
	client *redis.Client
	config broker.ConsumerConfig
	mu     sync.RWMutex
	closed bool
}

// Option configures the broker
type Option func(*Broker)

// WithConsumerConfig sets the consumer configuration
func WithConsumerConfig(cfg broker.ConsumerConfig) Option {
	return func(b *Broker) {
		b.config = cfg
	}
}

// NewBroker creates a new Redis Streams broker
func NewBroker(client *redis.Client, opts ...Option) *Broker {
	b := &Broker{
		client: client,
		config: broker.DefaultConsumerConfig(),
	}

	// Set default consumer name if not provided
	if b.config.Consumer == "" {
		hostname, _ := os.Hostname()
		b.config.Consumer = fmt.Sprintf("worker-%s-%d", hostname, os.Getpid())
	}

	for _, opt := range opts {
		opt(b)
	}

	return b
}

// streamKey returns the stream key for a queue
func (b *Broker) streamKey(queue string) string {
	return streamPrefix + queue
}

// dlqKey returns the DLQ stream key for a queue
func (b *Broker) dlqKey(queue string) string {
	return dlqPrefix + queue
}

// Publish adds a task to the specified queue
func (b *Broker) Publish(ctx context.Context, queue string, task *pb.Task) error {
	data, err := proto.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	pipe := b.client.Pipeline()

	// Add to stream
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: b.streamKey(queue),
		Values: map[string]interface{}{
			"data": data,
		},
	})

	// Track queue in set
	pipe.SAdd(ctx, queuesKey, queue)

	// Increment enqueued counter
	pipe.HIncrBy(ctx, statsPrefix+queue, "enqueued", 1)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to publish task: %w", err)
	}

	return nil
}

// Subscribe starts consuming tasks from the specified queues
func (b *Broker) Subscribe(ctx context.Context, queues []string, handler broker.TaskHandler) error {
	// Create consumer groups for each queue
	for _, queue := range queues {
		streamKey := b.streamKey(queue)
		// Create group if not exists (MKSTREAM creates stream if not exists)
		err := b.client.XGroupCreateMkStream(ctx, streamKey, b.config.Group, "0").Err()
		if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
			return fmt.Errorf("failed to create consumer group: %w", err)
		}
	}

	// First, recover any pending messages from previous runs
	if err := b.recoverPending(ctx, queues, handler); err != nil {
		return fmt.Errorf("failed to recover pending: %w", err)
	}

	// Main consume loop
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Build streams list for XREADGROUP
		streams := make([]string, 0, len(queues)*2)
		for _, q := range queues {
			streams = append(streams, b.streamKey(q))
		}
		for range queues {
			streams = append(streams, ">") // ">" means only new messages
		}

		results, err := b.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    b.config.Group,
			Consumer: b.config.Consumer,
			Streams:  streams,
			Count:    b.config.BatchSize,
			Block:    b.config.BlockDuration,
		}).Result()

		if err != nil {
			if err == redis.Nil {
				continue // No messages, try again
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("failed to read from stream: %w", err)
		}

		for _, stream := range results {
			queue := stream.Stream[len(streamPrefix):]
			for _, msg := range stream.Messages {
				if err := b.processMessage(ctx, queue, msg, handler); err != nil {
					// Error is logged but we continue processing
					continue
				}
			}
		}
	}
}

// processMessage handles a single message from the stream
func (b *Broker) processMessage(ctx context.Context, queue string, msg redis.XMessage, handler broker.TaskHandler) error {
	data, ok := msg.Values["data"].(string)
	if !ok {
		// Invalid message, acknowledge and skip
		b.client.XAck(ctx, b.streamKey(queue), b.config.Group, msg.ID)
		return fmt.Errorf("invalid message format")
	}

	task := &pb.Task{}
	if err := proto.Unmarshal([]byte(data), task); err != nil {
		// Invalid protobuf, acknowledge and skip
		b.client.XAck(ctx, b.streamKey(queue), b.config.Group, msg.ID)
		return fmt.Errorf("failed to unmarshal task: %w", err)
	}

	// Store stream message ID for acknowledgment
	if task.Metadata == nil {
		task.Metadata = &pb.TaskMetadata{}
	}
	task.Metadata.StreamMsgId = msg.ID

	// Call the handler
	if err := handler(ctx, task); err != nil {
		return err // Handler will decide retry/DLQ
	}

	// Success - acknowledge the message
	return b.Ack(ctx, queue, msg.ID)
}

// recoverPending processes any pending messages from previous runs
func (b *Broker) recoverPending(ctx context.Context, queues []string, handler broker.TaskHandler) error {
	for _, queue := range queues {
		streamKey := b.streamKey(queue)

		for {
			// Get pending messages for this consumer
			pending, err := b.client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    b.config.Group,
				Consumer: b.config.Consumer,
				Streams:  []string{streamKey, "0"}, // "0" means pending messages
				Count:    b.config.BatchSize,
			}).Result()

			if err != nil {
				if err == redis.Nil {
					break
				}
				return err
			}

			if len(pending) == 0 || len(pending[0].Messages) == 0 {
				break
			}

			for _, msg := range pending[0].Messages {
				if err := b.processMessage(ctx, queue, msg, handler); err != nil {
					continue
				}
			}
		}

		// Claim stale messages from other consumers
		if err := b.claimStale(ctx, queue, handler); err != nil {
			return err
		}
	}
	return nil
}

// claimStale claims messages that have been pending too long from other consumers
func (b *Broker) claimStale(ctx context.Context, queue string, handler broker.TaskHandler) error {
	streamKey := b.streamKey(queue)

	for {
		messages, _, err := b.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   streamKey,
			Group:    b.config.Group,
			Consumer: b.config.Consumer,
			MinIdle:  b.config.ClaimStaleAfter,
			Start:    "0",
			Count:    b.config.BatchSize,
		}).Result()

		if err != nil {
			if errors.Is(err, redis.Nil) {
				break
			}
			return err
		}

		if len(messages) == 0 {
			break
		}

		for _, msg := range messages {
			if err := b.processMessage(ctx, queue, msg, handler); err != nil {
				continue
			}
		}
	}
	return nil
}

// Ack acknowledges a task as successfully processed
func (b *Broker) Ack(ctx context.Context, queue string, msgID string) error {
	pipe := b.client.Pipeline()
	pipe.XAck(ctx, b.streamKey(queue), b.config.Group, msgID)
	pipe.XDel(ctx, b.streamKey(queue), msgID) // Remove from stream after ack
	pipe.HIncrBy(ctx, statsPrefix+queue, "completed", 1)
	_, err := pipe.Exec(ctx)

	return err
}

// Schedule adds a task to be processed at a specific time
func (b *Broker) Schedule(ctx context.Context, task *pb.Task, processAt time.Time) error {
	data, err := proto.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	return b.client.ZAdd(ctx, scheduledKey, redis.Z{
		Score:  float64(processAt.Unix()),
		Member: data,
	}).Err()
}

// GetScheduled retrieves and REMOVES tasks that are ready to be processed
// Uses ZPOPMIN for atomic get-and-delete
func (b *Broker) GetScheduled(ctx context.Context, until time.Time, limit int64) ([]*pb.Task, error) {
	// First, get the count of ready tasks
	members, err := b.client.ZRangeByScore(ctx, scheduledKey, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   fmt.Sprintf("%d", until.Unix()),
		Count: limit,
	}).Result()

	if err != nil || len(members) == 0 {
		return nil, err
	}

	// Pop them atomically
	results, err := b.client.ZPopMin(ctx, scheduledKey, int64(len(members))).Result()
	if err != nil {
		return nil, err
	}

	tasks := make([]*pb.Task, 0, len(results))
	now := float64(until.Unix())

	for _, z := range results {
		// Only process if score <= until (in case new items were added)
		if z.Score > now {
			// Put it back - it's not ready yet
			b.client.ZAdd(ctx, scheduledKey, z)
			continue
		}

		// Handle both string and []byte member types
		var data []byte
		switch v := z.Member.(type) {
		case string:
			data = []byte(v)
		case []byte:
			data = v
		default:
			continue
		}

		task := &pb.Task{}
		if err := proto.Unmarshal(data, task); err != nil {
			continue
		}
		tasks = append(tasks, task)
	}

	return tasks, nil
}

// MoveToQueue moves a task directly to its target queue (task already removed from sorted set)
func (b *Broker) MoveToQueue(ctx context.Context, task *pb.Task) error {
	queue := task.Options.GetQueue()
	if queue == "" {
		queue = "default"
	}

	// Update state
	task.Metadata.State = pb.TaskState_TASK_STATE_PENDING
	data, err := proto.Marshal(task)
	if err != nil {
		return err
	}

	return b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: b.streamKey(queue),
		Values: map[string]interface{}{
			"data": data,
		},
	}).Err()
}

// Retry schedules a task for retry with backoff
func (b *Broker) Retry(ctx context.Context, queue string, task *pb.Task, delay time.Duration) error {
	if task.Metadata == nil {
		task.Metadata = &pb.TaskMetadata{}
	}
	task.Metadata.State = pb.TaskState_TASK_STATE_RETRY

	data, err := proto.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	processAt := time.Now().Add(delay)

	pipe := b.client.Pipeline()

	// Remove from current stream if we have the message ID
	if task.Metadata.StreamMsgId != "" {
		pipe.XAck(ctx, b.streamKey(queue), b.config.Group, task.Metadata.StreamMsgId)
		pipe.XDel(ctx, b.streamKey(queue), task.Metadata.StreamMsgId)
	}

	// Add to retry sorted set
	pipe.ZAdd(ctx, retryKey, redis.Z{
		Score:  float64(processAt.Unix()),
		Member: data,
	})

	_, err = pipe.Exec(ctx)
	return err
}

// GetRetry retrieves and REMOVES tasks that are ready to be retried
// Uses ZPOPMIN for atomic get-and-delete
func (b *Broker) GetRetry(ctx context.Context, until time.Time, limit int64) ([]*pb.Task, error) {
	// First, get the count of ready tasks
	members, err := b.client.ZRangeByScore(ctx, retryKey, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   fmt.Sprintf("%d", until.Unix()),
		Count: limit,
	}).Result()

	if err != nil || len(members) == 0 {
		return nil, err
	}

	// Pop them atomically
	results, err := b.client.ZPopMin(ctx, retryKey, int64(len(members))).Result()
	if err != nil {
		return nil, err
	}

	tasks := make([]*pb.Task, 0, len(results))
	now := float64(until.Unix())

	for _, z := range results {
		// Only process if score <= until (in case new items were added)
		if z.Score > now {
			// Put it back - it's not ready yet
			b.client.ZAdd(ctx, retryKey, z)
			continue
		}

		// Handle both string and []byte member types
		var data []byte
		switch v := z.Member.(type) {
		case string:
			data = []byte(v)
		case []byte:
			data = v
		default:
			continue
		}

		task := &pb.Task{}
		if err := proto.Unmarshal(data, task); err != nil {
			continue
		}
		tasks = append(tasks, task)
	}

	return tasks, nil
}

// MoveToDLQ moves a failed task to the dead letter queue
func (b *Broker) MoveToDLQ(ctx context.Context, queue string, task *pb.Task, taskErr error) error {
	if task.Metadata == nil {
		task.Metadata = &pb.TaskMetadata{}
	}
	task.Metadata.State = pb.TaskState_TASK_STATE_DEAD
	if taskErr != nil {
		task.Metadata.LastError = taskErr.Error()
	}

	data, err := proto.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	pipe := b.client.Pipeline()

	// Acknowledge and remove from original queue
	if task.Metadata.StreamMsgId != "" {
		pipe.XAck(ctx, b.streamKey(queue), b.config.Group, task.Metadata.StreamMsgId)
		pipe.XDel(ctx, b.streamKey(queue), task.Metadata.StreamMsgId)
	}

	// Add to DLQ stream
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: b.dlqKey(queue),
		Values: map[string]interface{}{
			"data": data,
		},
	})

	_, err = pipe.Exec(ctx)
	return err
}

// GetDLQ retrieves tasks from the dead letter queue
func (b *Broker) GetDLQ(ctx context.Context, queue string, limit int64) ([]*pb.Task, error) {
	messages, err := b.client.XRange(ctx, b.dlqKey(queue), "-", "+").Result()
	if err != nil {
		return nil, err
	}

	tasks := make([]*pb.Task, 0, len(messages))
	for _, msg := range messages {
		data, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}
		task := &pb.Task{}
		if err := proto.Unmarshal([]byte(data), task); err != nil {
			continue
		}
		task.Metadata.StreamMsgId = msg.ID
		tasks = append(tasks, task)

		if int64(len(tasks)) >= limit {
			break
		}
	}

	return tasks, nil
}

// RetryFromDLQ moves a task from DLQ back to the main queue
func (b *Broker) RetryFromDLQ(ctx context.Context, queue string, taskID string) error {
	// Find the task in DLQ
	messages, err := b.client.XRange(ctx, b.dlqKey(queue), "-", "+").Result()
	if err != nil {
		return err
	}

	for _, msg := range messages {
		data, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}
		task := &pb.Task{}
		if err := proto.Unmarshal([]byte(data), task); err != nil {
			continue
		}

		if task.Id == taskID {
			// Reset task state
			task.Metadata.State = pb.TaskState_TASK_STATE_PENDING
			task.Metadata.RetryCount = 0
			task.Metadata.LastError = ""

			newData, _ := proto.Marshal(task)

			pipe := b.client.Pipeline()
			// Remove from DLQ
			pipe.XDel(ctx, b.dlqKey(queue), msg.ID)
			// Add back to main queue
			pipe.XAdd(ctx, &redis.XAddArgs{
				Stream: b.streamKey(queue),
				Values: map[string]interface{}{
					"data": newData,
				},
			})
			// Clear processed key so task can be processed again
			pipe.Del(ctx, processedPrefix+taskID)
			_, err = pipe.Exec(ctx)
			return err
		}
	}

	return fmt.Errorf("task %s not found in DLQ", taskID)
}

// SetProcessed marks a task as processed for idempotency
func (b *Broker) SetProcessed(ctx context.Context, taskID string, ttl time.Duration) (bool, error) {
	key := processedPrefix + taskID
	ok, err := b.client.SetNX(ctx, key, "1", ttl).Result()
	return ok, err
}

// SetUnique sets a unique key for deduplication
func (b *Broker) SetUnique(ctx context.Context, uniqueKey string, taskID string, ttl time.Duration) (bool, error) {
	key := uniquePrefix + uniqueKey
	ok, err := b.client.SetNX(ctx, key, taskID, ttl).Result()
	return ok, err
}

// GetQueueInfo returns statistics for a queue
func (b *Broker) GetQueueInfo(ctx context.Context, queue string) (*pb.QueueInfo, error) {
	info := &pb.QueueInfo{Name: queue}

	// Get stream length (total unprocessed messages)
	streamLen, err := b.client.XLen(ctx, b.streamKey(queue)).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	// Get consumer group pending info (active = being processed)
	pending, err := b.client.XPending(ctx, b.streamKey(queue), b.config.Group).Result()
	if err == nil && pending != nil {
		info.Active = pending.Count
	}

	// Pending = stream length - active (waiting to be picked up)
	info.Pending = streamLen - info.Active
	if info.Pending < 0 {
		info.Pending = 0
	}

	// Get DLQ length
	dlqLen, err := b.client.XLen(ctx, b.dlqKey(queue)).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	info.Dead = dlqLen

	// Get completed count from stats hash
	stats, err := b.client.HGetAll(ctx, statsPrefix+queue).Result()
	if err == nil {
		if v, ok := stats["completed"]; ok {
			fmt.Sscanf(v, "%d", &info.Completed)
		}
	}

	// For backwards compatibility
	info.Processed = info.Completed

	return info, nil
}

// GetQueues returns all known queue names
func (b *Broker) GetQueues(ctx context.Context) ([]string, error) {
	return b.client.SMembers(ctx, queuesKey).Result()
}

// PurgeQueue removes all tasks from a queue
func (b *Broker) PurgeQueue(ctx context.Context, queue string) error {
	return b.client.Del(ctx, b.streamKey(queue)).Err()
}

// PurgeDLQ removes all tasks from the dead letter queue
func (b *Broker) PurgeDLQ(ctx context.Context, queue string) error {
	return b.client.Del(ctx, b.dlqKey(queue)).Err()
}

// Ping checks if the broker is healthy
func (b *Broker) Ping(ctx context.Context) error {
	return b.client.Ping(ctx).Err()
}

// Close closes the broker connection
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}
	b.closed = true

	return b.client.Close()
}
