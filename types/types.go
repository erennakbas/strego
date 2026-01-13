// Package types provides core types for the strego task queue system.
package types

import (
	"encoding/json"
	"time"

	"github.com/sirupsen/logrus"
)

// Logger is the interface for logging in strego.
// It uses logrus.FieldLogger which is implemented by both *logrus.Logger and *logrus.Entry.
type Logger = logrus.FieldLogger

// DefaultLogger returns the default logrus logger.
func DefaultLogger() Logger {
	return logrus.StandardLogger()
}

// TaskState represents the current state of a task.
type TaskState string

const (
	TaskStateUnspecified TaskState = ""
	TaskStatePending     TaskState = "pending"
	TaskStateScheduled   TaskState = "scheduled"
	TaskStateActive      TaskState = "active"
	TaskStateCompleted   TaskState = "completed"
	TaskStateFailed      TaskState = "failed"
	TaskStateRetry       TaskState = "retry"
	TaskStateDead        TaskState = "dead"
	TaskStateCancelled   TaskState = "cancelled"
)

// String returns the string representation of TaskState.
func (s TaskState) String() string {
	return string(s)
}

// ConsumerGroupInfo represents information about a Redis consumer group.
type ConsumerGroupInfo struct {
	Name            string
	Consumers       int64
	Pending         int64
	LastDeliveredID string
}

// ConsumerInfo represents information about a consumer in a group.
type ConsumerInfo struct {
	Name     string
	Pending  int64
	Idle     int64 // milliseconds
	Inactive int64 // milliseconds
}

// TaskProto is the serializable task structure stored in Redis.
type TaskProto struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Options  *TaskOptions    `json:"options,omitempty"`
	Metadata *TaskMetadata   `json:"metadata,omitempty"`
}

// TaskOptions configures how a task should be processed.
type TaskOptions struct {
	Queue     string            `json:"queue,omitempty"`
	MaxRetry  int32             `json:"max_retry,omitempty"`
	Timeout   time.Duration     `json:"timeout,omitempty"`
	ProcessAt *time.Time        `json:"process_at,omitempty"`
	Priority  int32             `json:"priority,omitempty"`
	UniqueKey string            `json:"unique_key,omitempty"`
	UniqueTTL time.Duration     `json:"unique_ttl,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// TaskMetadata contains runtime information about a task.
type TaskMetadata struct {
	State       TaskState  `json:"state,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	RetryCount  int32      `json:"retry_count,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	WorkerID    string     `json:"worker_id,omitempty"`
	TraceID     string     `json:"trace_id,omitempty"`
	StreamMsgID string     `json:"stream_msg_id,omitempty"`
}

// TaskResult stores the outcome of task processing.
type TaskResult struct {
	TaskID      string          `json:"task_id"`
	Success     bool            `json:"success"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	Duration    time.Duration   `json:"duration,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

// TaskInfo is returned after enqueuing a task.
type TaskInfo struct {
	ID        string     `json:"id"`
	Queue     string     `json:"queue"`
	State     TaskState  `json:"state"`
	ProcessAt *time.Time `json:"process_at,omitempty"`
}

// QueueInfo contains statistics about a queue.
type QueueInfo struct {
	Name      string `json:"name"`
	Pending   int64  `json:"pending"`
	Active    int64  `json:"active"`
	Scheduled int64  `json:"scheduled"`
	Retry     int64  `json:"retry"`
	Dead      int64  `json:"dead"`
	Completed int64  `json:"completed"`
	Processed int64  `json:"processed"`
	Failed    int64  `json:"failed"`
}

// WorkerInfo contains information about a worker.
type WorkerInfo struct {
	ID          string    `json:"id"`
	Hostname    string    `json:"hostname"`
	PID         int32     `json:"pid"`
	Queues      []string  `json:"queues"`
	Concurrency int32     `json:"concurrency"`
	StartedAt   time.Time `json:"started_at"`
	Status      TaskState `json:"status"`
}
