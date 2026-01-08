// Package strego provides a distributed task queue for Go applications.
package strego

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/erennakbas/strego/pkg/types"
)

// Re-export types for convenience
type (
	TaskState    = types.TaskState
	TaskProto    = types.TaskProto
	TaskOptions  = types.TaskOptions
	TaskMetadata = types.TaskMetadata
	TaskInfo     = types.TaskInfo
	QueueInfo    = types.QueueInfo
)

// Task state constants
const (
	TaskStateUnspecified = types.TaskStateUnspecified
	TaskStatePending     = types.TaskStatePending
	TaskStateScheduled   = types.TaskStateScheduled
	TaskStateActive      = types.TaskStateActive
	TaskStateCompleted   = types.TaskStateCompleted
	TaskStateFailed      = types.TaskStateFailed
	TaskStateRetry       = types.TaskStateRetry
	TaskStateDead        = types.TaskStateDead
	TaskStateCancelled   = types.TaskStateCancelled
)

// Task represents a unit of work to be processed.
type Task struct {
	proto *types.TaskProto
}

// NewTask creates a new task with a typed payload.
// The payload will be JSON-encoded.
func NewTask[T any](taskType string, payload T, opts ...TaskOption) (*Task, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	t := &Task{
		proto: &types.TaskProto{
			ID:      uuid.NewString(),
			Type:    taskType,
			Payload: data,
			Options: &types.TaskOptions{
				Queue:    "default",
				MaxRetry: 3,
			},
			Metadata: &types.TaskMetadata{
				State:     types.TaskStatePending,
				CreatedAt: &now,
			},
		},
	}

	for _, opt := range opts {
		opt(t)
	}

	return t, nil
}

// NewTaskFromBytes creates a new task with raw JSON bytes payload.
func NewTaskFromBytes(taskType string, payload []byte, opts ...TaskOption) *Task {
	now := time.Now()
	t := &Task{
		proto: &types.TaskProto{
			ID:      uuid.NewString(),
			Type:    taskType,
			Payload: payload,
			Options: &types.TaskOptions{
				Queue:    "default",
				MaxRetry: 3,
			},
			Metadata: &types.TaskMetadata{
				State:     types.TaskStatePending,
				CreatedAt: &now,
			},
		},
	}

	for _, opt := range opts {
		opt(t)
	}

	return t
}

// ID returns the task ID.
func (t *Task) ID() string {
	return t.proto.ID
}

// Type returns the task type.
func (t *Task) Type() string {
	return t.proto.Type
}

// Payload returns the raw payload bytes.
func (t *Task) Payload() []byte {
	return t.proto.Payload
}

// Queue returns the queue name.
func (t *Task) Queue() string {
	if t.proto.Options != nil && t.proto.Options.Queue != "" {
		return t.proto.Options.Queue
	}
	return "default"
}

// State returns the current task state.
func (t *Task) State() types.TaskState {
	if t.proto.Metadata != nil {
		return t.proto.Metadata.State
	}
	return types.TaskStatePending
}

// RetryCount returns the number of retry attempts.
func (t *Task) RetryCount() int {
	if t.proto.Metadata != nil {
		return int(t.proto.Metadata.RetryCount)
	}
	return 0
}

// MaxRetry returns the maximum retry attempts.
func (t *Task) MaxRetry() int {
	if t.proto.Options != nil {
		return int(t.proto.Options.MaxRetry)
	}
	return 3
}

// LastError returns the last error message.
func (t *Task) LastError() string {
	if t.proto.Metadata != nil {
		return t.proto.Metadata.LastError
	}
	return ""
}

// Labels returns the task labels.
func (t *Task) Labels() map[string]string {
	if t.proto.Options != nil {
		return t.proto.Options.Labels
	}
	return nil
}

// Proto returns the underlying task proto structure.
func (t *Task) Proto() *types.TaskProto {
	return t.proto
}

// Unmarshal extracts the payload into a typed struct.
func Unmarshal[T any](t *Task, v *T) error {
	return json.Unmarshal(t.proto.Payload, v)
}

// TaskFromProto creates a Task from a TaskProto.
func TaskFromProto(proto *types.TaskProto) *Task {
	return &Task{proto: proto}
}

// TaskOption configures a task.
type TaskOption func(*Task)

// WithQueue sets the queue name.
func WithQueue(queue string) TaskOption {
	return func(t *Task) {
		if t.proto.Options == nil {
			t.proto.Options = &types.TaskOptions{}
		}
		t.proto.Options.Queue = queue
	}
}

// WithMaxRetry sets the maximum retry attempts.
func WithMaxRetry(n int) TaskOption {
	return func(t *Task) {
		if t.proto.Options == nil {
			t.proto.Options = &types.TaskOptions{}
		}
		t.proto.Options.MaxRetry = int32(n)
	}
}

// WithTimeout sets the task processing timeout.
func WithTimeout(d time.Duration) TaskOption {
	return func(t *Task) {
		if t.proto.Options == nil {
			t.proto.Options = &types.TaskOptions{}
		}
		t.proto.Options.Timeout = d
	}
}

// WithProcessAt sets when the task should be processed.
func WithProcessAt(at time.Time) TaskOption {
	return func(t *Task) {
		if t.proto.Options == nil {
			t.proto.Options = &types.TaskOptions{}
		}
		t.proto.Options.ProcessAt = &at
		if t.proto.Metadata == nil {
			t.proto.Metadata = &types.TaskMetadata{}
		}
		t.proto.Metadata.State = types.TaskStateScheduled
	}
}

// WithProcessIn sets the delay before processing.
func WithProcessIn(d time.Duration) TaskOption {
	return WithProcessAt(time.Now().Add(d))
}

// WithPriority sets the task priority (0-10, higher = more important).
func WithPriority(priority int) TaskOption {
	return func(t *Task) {
		if t.proto.Options == nil {
			t.proto.Options = &types.TaskOptions{}
		}
		t.proto.Options.Priority = int32(priority)
	}
}

// WithUniqueKey sets a unique key for deduplication.
func WithUniqueKey(key string, ttl time.Duration) TaskOption {
	return func(t *Task) {
		if t.proto.Options == nil {
			t.proto.Options = &types.TaskOptions{}
		}
		t.proto.Options.UniqueKey = key
		t.proto.Options.UniqueTTL = ttl
	}
}

// WithLabels sets custom labels on the task.
func WithLabels(labels map[string]string) TaskOption {
	return func(t *Task) {
		if t.proto.Options == nil {
			t.proto.Options = &types.TaskOptions{}
		}
		t.proto.Options.Labels = labels
	}
}

// WithLabel adds a single label to the task.
func WithLabel(key, value string) TaskOption {
	return func(t *Task) {
		if t.proto.Options == nil {
			t.proto.Options = &types.TaskOptions{}
		}
		if t.proto.Options.Labels == nil {
			t.proto.Options.Labels = make(map[string]string)
		}
		t.proto.Options.Labels[key] = value
	}
}
