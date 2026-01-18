// Package redis provides a Redis Streams implementation of the Broker interface.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"

	"github.com/erennakbas/strego/broker"
	"github.com/erennakbas/strego/types"
)

const (
	// Key prefixes
	streamPrefix    = "strego:stream:"
	dlqPrefix       = "strego:dlq:"
	scheduledPrefix = "strego:scheduled:"
	retryPrefix     = "strego:retry:"
	processedPrefix = "strego:processed:"
	uniquePrefix    = "strego:unique:"
	statsPrefix     = "strego:stats:"
	queuesKey       = "strego:queues"
)

// Broker implements the broker.Broker interface using Redis Streams.
type Broker struct {
	client *redis.Client
	config broker.ConsumerConfig
	logger types.Logger
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

// WithLogger sets the logger for the broker
func WithLogger(logger types.Logger) Option {
	return func(b *Broker) {
		b.logger = logger
	}
}

// NewBroker creates a new Redis Streams broker
func NewBroker(client *redis.Client, opts ...Option) *Broker {
	b := &Broker{
		client: client,
		config: broker.DefaultConsumerConfig(),
		logger: logrus.StandardLogger(),
	}

	// Apply options first
	for _, opt := range opts {
		opt(b)
	}

	// Set default consumer name if not provided (after options)
	if b.config.Consumer == "" {
		hostname, _ := os.Hostname()
		b.config.Consumer = fmt.Sprintf("worker-%s-%d", hostname, os.Getpid())
	}

	// Ensure non-zero values for required config fields
	defaults := broker.DefaultConsumerConfig()
	if b.config.BatchSize <= 0 {
		b.config.BatchSize = defaults.BatchSize
	}
	if b.config.BlockDuration <= 0 {
		b.config.BlockDuration = defaults.BlockDuration
	}
	if b.config.ClaimStaleAfter <= 0 {
		b.config.ClaimStaleAfter = defaults.ClaimStaleAfter
	}
	if b.config.ClaimCheckInterval <= 0 {
		b.config.ClaimCheckInterval = defaults.ClaimCheckInterval
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

// scheduledKey returns the scheduled sorted set key for a queue
func (b *Broker) scheduledKey(queue string) string {
	return scheduledPrefix + queue
}

// retryKey returns the retry sorted set key for a queue
func (b *Broker) retryKey(queue string) string {
	return retryPrefix + queue
}

// Publish adds a task to the specified queue
func (b *Broker) Publish(ctx context.Context, queue string, task *types.TaskProto) error {
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	pipe := b.client.Pipeline()

	// Add to stream
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: b.streamKey(queue),
		Values: map[string]interface{}{
			"data": string(data),
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

	// Start background claimer goroutine first (before recovery)
	// This ensures the goroutine is scheduled early and ready to claim stale messages
	go b.backgroundClaimer(ctx, queues, handler)

	// Recover this consumer's own pending messages from previous runs
	// This is useful when using stable consumer names (e.g., Kubernetes StatefulSet)
	// For dynamic consumer names, this will return quickly with no messages
	if err := b.recoverOwnPending(ctx, queues, handler); err != nil {
		return fmt.Errorf("failed to recover own pending messages: %w", err)
	}

	// Pre-allocate streams array (reused in every iteration for efficiency)
	streams := make([]string, 0, len(queues)*2)
	for _, q := range queues {
		streams = append(streams, b.streamKey(q))
	}
	for range queues {
		streams = append(streams, ">") // ">" means only new messages
	}

	// Transient error tracking
	const maxRetries = 10
	retryCount := 0

	// Main consume loop
	for {
		// XReadGroup respects context and blocks, no need for select + default
		results, err := b.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    b.config.Group,
			Consumer: b.config.Consumer,
			Streams:  streams,
			Count:    b.config.BatchSize,
			Block:    b.config.BlockDuration,
		}).Result()

		if err != nil {
			// No messages - continue (not an error, just empty)
			if errors.Is(err, redis.Nil) {
				retryCount = 0 // Reset retry count on success
				continue
			}

			// Context cancelled - clean shutdown
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Transient errors (network issues, timeouts) - retry with exponential backoff
			if isTransientError(err) {
				retryCount++

				// Check if we've exceeded max retries
				if retryCount > maxRetries {
					b.logger.WithError(err).
						WithField("retry_count", retryCount).
						Error("max retries exceeded for transient error, giving up")
					return fmt.Errorf("max retries (%d) exceeded: %w", maxRetries, err)
				}

				// Calculate exponential backoff: 1s, 2s, 4s, 8s, 16s, 32s (max 30s)
				backoff := time.Duration(1<<uint(retryCount-1)) * time.Second
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}

				b.logger.WithError(err).
					WithField("retry_count", retryCount).
					WithField("backoff_seconds", backoff.Seconds()).
					Warn("transient error encountered, retrying with exponential backoff")

				time.Sleep(backoff)
				continue
			}

			// Fatal error - return and let caller handle
			b.logger.WithError(err).Error("fatal error reading from stream")
			return fmt.Errorf("failed to read from stream: %w", err)
		}

		// Success - reset retry count
		if retryCount > 0 {
			retryCount = 0
		}

		// Process messages
		for _, stream := range results {
			queue := stream.Stream[len(streamPrefix):]
			for _, msg := range stream.Messages {
				if err := b.processMessage(ctx, queue, msg, handler); err != nil {
					// processMessage handles retries/DLQ internally, just continue
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

	task := &types.TaskProto{}
	if err := json.Unmarshal([]byte(data), task); err != nil {
		// Invalid JSON, acknowledge and skip
		b.client.XAck(ctx, b.streamKey(queue), b.config.Group, msg.ID)
		return fmt.Errorf("failed to unmarshal task: %w", err)
	}

	// Store stream message ID for acknowledgment
	if task.Metadata == nil {
		task.Metadata = &types.TaskMetadata{}
	}
	task.Metadata.StreamMsgID = msg.ID

	// Call the handler
	if err := handler(ctx, task); err != nil {
		return err // Handler will decide retry/DLQ
	}

	// Success - acknowledge the message
	return b.Ack(ctx, queue, msg.ID)
}

// recoverOwnPending processes this consumer's own pending messages from previous runs.
// This only recovers messages that were previously claimed by THIS specific consumer.
// Stale messages from other consumers are handled by the backgroundClaimer goroutine.
func (b *Broker) recoverOwnPending(ctx context.Context, queues []string, handler broker.TaskHandler) error {
	for _, queue := range queues {
		streamKey := b.streamKey(queue)

		for {
			// Get pending messages for THIS consumer only
			// "0" means read pending messages (not new ones)
			pending, err := b.client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    b.config.Group,
				Consumer: b.config.Consumer,
				Streams:  []string{streamKey, "0"},
				Count:    b.config.BatchSize,
			}).Result()

			if err != nil {
				if errors.Is(err, redis.Nil) {
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

// backgroundClaimer runs in a separate goroutine to periodically claim stale messages
// This ensures that crashed workers' messages are recovered even when the system is running
func (b *Broker) backgroundClaimer(ctx context.Context, queues []string, handler broker.TaskHandler) {
	// Use a ticker to check for stale messages periodically
	ticker := time.NewTicker(b.config.ClaimCheckInterval)
	defer ticker.Stop()

	b.logger.WithField("interval", b.config.ClaimCheckInterval).
		WithField("queues", queues).
		Debug("background claimer started")

	for {
		select {
		case <-ctx.Done():
			b.logger.Debug("background claimer stopped")
			return
		case <-ticker.C:
			// Check each queue for stale messages
			for _, queue := range queues {
				// Run claim in a non-blocking way - if it fails, log and continue
				if err := b.claimStale(ctx, queue, handler); err != nil {
					// Only log if context is not cancelled
					if ctx.Err() == nil {
						b.logger.WithError(err).
							WithField("queue", queue).
							Warn("failed to claim stale messages, will retry on next tick")
					}
				}
			}
		}
	}
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
func (b *Broker) Schedule(ctx context.Context, task *types.TaskProto, processAt time.Time) error {
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	queue := "default"
	if task.Options != nil && task.Options.Queue != "" {
		queue = task.Options.Queue
	}

	pipe := b.client.Pipeline()

	// Add to queue-specific scheduled sorted set
	pipe.ZAdd(ctx, b.scheduledKey(queue), redis.Z{
		Score:  float64(processAt.Unix()),
		Member: string(data),
	})

	// Track queue in set (so it appears in GetQueues)
	pipe.SAdd(ctx, queuesKey, queue)

	// Increment scheduled counter
	pipe.HIncrBy(ctx, statsPrefix+queue, "scheduled", 1)

	_, err = pipe.Exec(ctx)
	return err
}

// Lua script for atomic scheduled task processing
// Iterates over all queues, gets tasks with score <= now from each queue's scheduled set,
// removes them and adds to their target queue streams
var enqueueScheduledScript = redis.NewScript(`
local queuesKey = KEYS[1]
local scheduledPrefix = ARGV[1]
local streamPrefix = ARGV[2]
local statsPrefix = ARGV[3]
local maxScore = ARGV[4]
local limit = tonumber(ARGV[5])

-- Get all known queues
local queues = redis.call('SMEMBERS', queuesKey)
if #queues == 0 then
    return 0
end

local totalCount = 0

for _, queue in ipairs(queues) do
    local scheduledKey = scheduledPrefix .. queue
    
    -- Get ready tasks for this queue
    local tasks = redis.call('ZRANGEBYSCORE', scheduledKey, '-inf', maxScore, 'LIMIT', 0, limit)
    
    for _, taskData in ipairs(tasks) do
        -- Parse task
        local task = cjson.decode(taskData)
        
        -- Update state to pending
        if task.metadata then
            task.metadata.state = 'pending'
        else
            task.metadata = {state = 'pending'}
        end
        local updatedData = cjson.encode(task)
        
        -- Atomically: remove from scheduled set and add to stream
        redis.call('ZREM', scheduledKey, taskData)
        redis.call('XADD', streamPrefix .. queue, '*', 'data', updatedData)
        redis.call('HINCRBY', statsPrefix .. queue, 'enqueued', 1)
        
        totalCount = totalCount + 1
    end
end

return totalCount
`)

// EnqueueScheduled atomically moves ready scheduled tasks to their queues.
func (b *Broker) EnqueueScheduled(ctx context.Context, until time.Time, limit int64) (int64, error) {
	result, err := enqueueScheduledScript.Run(ctx, b.client,
		[]string{queuesKey},
		scheduledPrefix, streamPrefix, statsPrefix, until.Unix(), limit,
	).Int64()

	if err != nil {
		return 0, fmt.Errorf("failed to enqueue scheduled tasks: %w", err)
	}

	return result, nil
}

// Lua script for atomic retry task processing
// Iterates over all queues, gets tasks with score <= now from each queue's retry set,
// removes them and adds to their target queue streams
var enqueueRetryScript = redis.NewScript(`
local queuesKey = KEYS[1]
local retryPrefix = ARGV[1]
local streamPrefix = ARGV[2]
local statsPrefix = ARGV[3]
local maxScore = ARGV[4]
local limit = tonumber(ARGV[5])

-- Get all known queues
local queues = redis.call('SMEMBERS', queuesKey)
if #queues == 0 then
    return 0
end

local totalCount = 0

for _, queue in ipairs(queues) do
    local retryKey = retryPrefix .. queue
    
    -- Get ready retry tasks for this queue
    local tasks = redis.call('ZRANGEBYSCORE', retryKey, '-inf', maxScore, 'LIMIT', 0, limit)
    
    for _, taskData in ipairs(tasks) do
        -- Parse task
        local task = cjson.decode(taskData)
        
        -- Update state to pending (from retry)
        if task.metadata then
            task.metadata.state = 'pending'
        else
            task.metadata = {state = 'pending'}
        end
        local updatedData = cjson.encode(task)
        
        -- Atomically: remove from retry set and add to stream
        redis.call('ZREM', retryKey, taskData)
        redis.call('XADD', streamPrefix .. queue, '*', 'data', updatedData)
        redis.call('HINCRBY', statsPrefix .. queue, 'enqueued', 1)
        
        totalCount = totalCount + 1
    end
end

return totalCount
`)

// EnqueueRetry atomically moves ready retry tasks to their queues.
func (b *Broker) EnqueueRetry(ctx context.Context, until time.Time, limit int64) (int64, error) {
	result, err := enqueueRetryScript.Run(ctx, b.client,
		[]string{queuesKey},
		retryPrefix, streamPrefix, statsPrefix, until.Unix(), limit,
	).Int64()

	if err != nil {
		return 0, fmt.Errorf("failed to enqueue retry tasks: %w", err)
	}

	return result, nil
}

// Retry schedules a task for retry with backoff
func (b *Broker) Retry(ctx context.Context, queue string, task *types.TaskProto, delay time.Duration) error {
	if task.Metadata == nil {
		task.Metadata = &types.TaskMetadata{}
	}
	task.Metadata.State = types.TaskStateRetry

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	processAt := time.Now().Add(delay)

	pipe := b.client.Pipeline()

	// Remove from current stream if we have the message ID
	if task.Metadata.StreamMsgID != "" {
		pipe.XAck(ctx, b.streamKey(queue), b.config.Group, task.Metadata.StreamMsgID)
		pipe.XDel(ctx, b.streamKey(queue), task.Metadata.StreamMsgID)
	}

	// Add to queue-specific retry sorted set
	pipe.ZAdd(ctx, b.retryKey(queue), redis.Z{
		Score:  float64(processAt.Unix()),
		Member: string(data),
	})

	_, err = pipe.Exec(ctx)
	return err
}

// MoveToDLQ moves a failed task to the dead letter queue
func (b *Broker) MoveToDLQ(ctx context.Context, queue string, task *types.TaskProto, taskErr error) error {
	if task.Metadata == nil {
		task.Metadata = &types.TaskMetadata{}
	}
	task.Metadata.State = types.TaskStateDead
	if taskErr != nil {
		task.Metadata.LastError = taskErr.Error()
	}

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	pipe := b.client.Pipeline()

	// Acknowledge and remove from original queue
	if task.Metadata.StreamMsgID != "" {
		pipe.XAck(ctx, b.streamKey(queue), b.config.Group, task.Metadata.StreamMsgID)
		pipe.XDel(ctx, b.streamKey(queue), task.Metadata.StreamMsgID)
	}

	// Add to DLQ stream
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: b.dlqKey(queue),
		Values: map[string]interface{}{
			"data": string(data),
		},
	})

	_, err = pipe.Exec(ctx)
	return err
}

// GetDLQ retrieves tasks from the dead letter queue
func (b *Broker) GetDLQ(ctx context.Context, queue string, limit int64) ([]*types.TaskProto, error) {
	messages, err := b.client.XRange(ctx, b.dlqKey(queue), "-", "+").Result()
	if err != nil {
		return nil, err
	}

	tasks := make([]*types.TaskProto, 0, len(messages))
	count := int64(0)

	for _, msg := range messages {
		if limit > 0 && count >= limit {
			break
		}

		data, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}
		task := &types.TaskProto{}
		if err := json.Unmarshal([]byte(data), task); err != nil {
			continue
		}
		tasks = append(tasks, task)
		count++
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
		task := &types.TaskProto{}
		if err := json.Unmarshal([]byte(data), task); err != nil {
			continue
		}

		if task.ID == taskID {
			// Reset task state
			task.Metadata.State = types.TaskStatePending
			task.Metadata.RetryCount = 0
			task.Metadata.LastError = ""

			newData, _ := json.Marshal(task)

			pipe := b.client.Pipeline()
			// Remove from DLQ
			pipe.XDel(ctx, b.dlqKey(queue), msg.ID)
			// Add back to main queue
			pipe.XAdd(ctx, &redis.XAddArgs{
				Stream: b.streamKey(queue),
				Values: map[string]interface{}{
					"data": string(newData),
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
func (b *Broker) GetQueueInfo(ctx context.Context, queue string) (*types.QueueInfo, error) {
	info := &types.QueueInfo{Name: queue}

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
			_, _ = fmt.Sscanf(v, "%d", &info.Completed)
		}
	}

	// For backwards compatibility
	info.Processed = info.Completed

	// Get scheduled count
	scheduledCount, err := b.client.ZCard(ctx, b.scheduledKey(queue)).Result()
	if err == nil {
		info.Scheduled = scheduledCount
	}

	// Get retry count
	retryCount, err := b.client.ZCard(ctx, b.retryKey(queue)).Result()
	if err == nil {
		info.Retry = retryCount
	}

	return info, nil
}

// GetQueues returns all known queue names
func (b *Broker) GetQueues(ctx context.Context) ([]string, error) {
	return b.client.SMembers(ctx, queuesKey).Result()
}

// GetConsumerGroup returns the consumer group name this broker uses
func (b *Broker) GetConsumerGroup() string {
	return b.config.Group
}

// GetScheduledCount returns the number of scheduled tasks for a queue
func (b *Broker) GetScheduledCount(ctx context.Context, queue string) (int64, error) {
	count, err := b.client.ZCard(ctx, b.scheduledKey(queue)).Result()
	if err != nil && err != redis.Nil {
		return 0, err
	}
	return count, nil
}

// GetRetryCount returns the number of retry tasks for a queue
func (b *Broker) GetRetryCount(ctx context.Context, queue string) (int64, error) {
	count, err := b.client.ZCard(ctx, b.retryKey(queue)).Result()
	if err != nil && err != redis.Nil {
		return 0, err
	}
	return count, nil
}

// GetConsumerGroups returns all consumer groups for a queue
func (b *Broker) GetConsumerGroups(ctx context.Context, queue string) ([]*types.ConsumerGroupInfo, error) {
	groups, err := b.client.XInfoGroups(ctx, b.streamKey(queue)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return []*types.ConsumerGroupInfo{}, nil
		}
		return nil, fmt.Errorf("failed to get consumer groups: %w", err)
	}

	result := make([]*types.ConsumerGroupInfo, 0, len(groups))
	for _, g := range groups {
		result = append(result, &types.ConsumerGroupInfo{
			Name:            g.Name,
			Consumers:       g.Consumers,
			Pending:         g.Pending,
			LastDeliveredID: g.LastDeliveredID,
		})
	}

	return result, nil
}

// GetGroupConsumers returns all consumers in a consumer group
func (b *Broker) GetGroupConsumers(ctx context.Context, queue, group string) ([]*types.ConsumerInfo, error) {
	consumers, err := b.client.XInfoConsumers(ctx, b.streamKey(queue), group).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return []*types.ConsumerInfo{}, nil
		}
		return nil, fmt.Errorf("failed to get group consumers: %w", err)
	}

	result := make([]*types.ConsumerInfo, 0, len(consumers))
	for _, c := range consumers {
		result = append(result, &types.ConsumerInfo{
			Name:     c.Name,
			Pending:  c.Pending,
			Idle:     c.Idle.Milliseconds(),
			Inactive: c.Inactive.Milliseconds(),
		})
	}

	return result, nil
}

// RemoveConsumer removes a consumer from a consumer group.
// This is safe to call when the consumer has no pending tasks.
func (b *Broker) RemoveConsumer(ctx context.Context, queue, group, consumer string) error {
	streamKey := b.streamKey(queue)
	return b.client.XGroupDelConsumer(ctx, streamKey, group, consumer).Err()
}

// PurgeQueue removes all tasks from a queue
func (b *Broker) PurgeQueue(ctx context.Context, queue string) error {
	// Delete the stream
	if err := b.client.Del(ctx, b.streamKey(queue)).Err(); err != nil {
		return err
	}
	// Reset stats
	return b.client.Del(ctx, statsPrefix+queue).Err()
}

// PurgeDLQ removes all tasks from the dead letter queue
func (b *Broker) PurgeDLQ(ctx context.Context, queue string) error {
	return b.client.Del(ctx, b.dlqKey(queue)).Err()
}

// Ping checks if the broker is healthy
func (b *Broker) Ping(ctx context.Context) error {
	return b.client.Ping(ctx).Err()
}

// isTransientError checks if an error is transient and should be retried
func isTransientError(err error) bool {
	if err == nil {
		return false
	}

	// Check for context deadline exceeded
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Check for common transient network/Redis errors
	errStr := err.Error()
	transientPatterns := []string{
		"connection refused",
		"connection reset",
		"broken pipe",
		"i/o timeout",
		"timeout",
		"EOF",
		"network is unreachable",
		"no route to host",
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// Close closes the broker connection
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return errors.New("broker already closed")
	}

	b.closed = true
	return nil
}
