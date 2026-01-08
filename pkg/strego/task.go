// Package strego provides a distributed task queue for Go applications.
package strego

import (
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/erennakbas/strego/internal/proto"
)

// Task represents a unit of work to be processed.
type Task struct {
	pb *pb.Task
}

// NewTask creates a new task with the given type and protobuf payload.
func NewTask(taskType string, payload proto.Message, opts ...TaskOption) (*Task, error) {
	data, err := proto.Marshal(payload)
	if err != nil {
		return nil, err
	}

	t := &Task{
		pb: &pb.Task{
			Id:      uuid.NewString(),
			Type:    taskType,
			Payload: data,
			Options: &pb.TaskOptions{
				Queue:    "default",
				MaxRetry: 3,
			},
			Metadata: &pb.TaskMetadata{
				State:     pb.TaskState_TASK_STATE_PENDING,
				CreatedAt: timestamppb.Now(),
			},
		},
	}

	for _, opt := range opts {
		opt(t)
	}

	return t, nil
}

// NewTaskFromBytes creates a new task with raw bytes payload.
func NewTaskFromBytes(taskType string, payload []byte, opts ...TaskOption) *Task {
	t := &Task{
		pb: &pb.Task{
			Id:      uuid.NewString(),
			Type:    taskType,
			Payload: payload,
			Options: &pb.TaskOptions{
				Queue:    "default",
				MaxRetry: 3,
			},
			Metadata: &pb.TaskMetadata{
				State:     pb.TaskState_TASK_STATE_PENDING,
				CreatedAt: timestamppb.Now(),
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
	return t.pb.Id
}

// Type returns the task type.
func (t *Task) Type() string {
	return t.pb.Type
}

// Payload returns the raw payload bytes.
func (t *Task) Payload() []byte {
	return t.pb.Payload
}

// Queue returns the queue name.
func (t *Task) Queue() string {
	if t.pb.Options != nil {
		return t.pb.Options.Queue
	}
	return "default"
}

// State returns the current task state.
func (t *Task) State() TaskState {
	if t.pb.Metadata != nil {
		return TaskState(t.pb.Metadata.State)
	}
	return TaskStatePending
}

// RetryCount returns the number of retry attempts.
func (t *Task) RetryCount() int {
	if t.pb.Metadata != nil {
		return int(t.pb.Metadata.RetryCount)
	}
	return 0
}

// MaxRetry returns the maximum retry attempts.
func (t *Task) MaxRetry() int {
	if t.pb.Options != nil {
		return int(t.pb.Options.MaxRetry)
	}
	return 3
}

// LastError returns the last error message.
func (t *Task) LastError() string {
	if t.pb.Metadata != nil {
		return t.pb.Metadata.LastError
	}
	return ""
}

// Labels returns the task labels.
func (t *Task) Labels() map[string]string {
	if t.pb.Options != nil {
		return t.pb.Options.Labels
	}
	return nil
}

// Proto returns the underlying protobuf message.
func (t *Task) Proto() *pb.Task {
	return t.pb
}

// Unmarshal extracts the payload into a protobuf message.
func Unmarshal[T proto.Message](t *Task, msg T) error {
	return proto.Unmarshal(t.pb.Payload, msg)
}

// TaskFromProto creates a Task from a protobuf message.
func TaskFromProto(pbTask *pb.Task) *Task {
	return &Task{pb: pbTask}
}

// TaskState represents the state of a task.
type TaskState int32

const (
	TaskStateUnspecified TaskState = 0
	TaskStatePending     TaskState = 1
	TaskStateScheduled   TaskState = 2
	TaskStateActive      TaskState = 3
	TaskStateCompleted   TaskState = 4
	TaskStateFailed      TaskState = 5
	TaskStateRetry       TaskState = 6
	TaskStateDead        TaskState = 7
	TaskStateCancelled   TaskState = 8
)

func (s TaskState) String() string {
	switch s {
	case TaskStatePending:
		return "pending"
	case TaskStateScheduled:
		return "scheduled"
	case TaskStateActive:
		return "active"
	case TaskStateCompleted:
		return "completed"
	case TaskStateFailed:
		return "failed"
	case TaskStateRetry:
		return "retry"
	case TaskStateDead:
		return "dead"
	case TaskStateCancelled:
		return "cancelled"
	default:
		return "unspecified"
	}
}

// TaskInfo contains information about an enqueued task.
type TaskInfo struct {
	ID        string
	Queue     string
	State     TaskState
	ProcessAt time.Time
}

// TaskOption configures a task.
type TaskOption func(*Task)

// WithQueue sets the queue name.
func WithQueue(queue string) TaskOption {
	return func(t *Task) {
		if t.pb.Options == nil {
			t.pb.Options = &pb.TaskOptions{}
		}
		t.pb.Options.Queue = queue
	}
}

// WithMaxRetry sets the maximum retry attempts.
func WithMaxRetry(n int) TaskOption {
	return func(t *Task) {
		if t.pb.Options == nil {
			t.pb.Options = &pb.TaskOptions{}
		}
		t.pb.Options.MaxRetry = int32(n)
	}
}

// WithTimeout sets the task processing timeout.
func WithTimeout(d time.Duration) TaskOption {
	return func(t *Task) {
		if t.pb.Options == nil {
			t.pb.Options = &pb.TaskOptions{}
		}
		t.pb.Options.Timeout = durationpb.New(d)
	}
}

// WithProcessAt sets when the task should be processed.
func WithProcessAt(at time.Time) TaskOption {
	return func(t *Task) {
		if t.pb.Options == nil {
			t.pb.Options = &pb.TaskOptions{}
		}
		t.pb.Options.ProcessAt = timestamppb.New(at)
		if t.pb.Metadata == nil {
			t.pb.Metadata = &pb.TaskMetadata{}
		}
		t.pb.Metadata.State = pb.TaskState_TASK_STATE_SCHEDULED
	}
}

// WithProcessIn sets the delay before processing.
func WithProcessIn(d time.Duration) TaskOption {
	return WithProcessAt(time.Now().Add(d))
}

// WithPriority sets the task priority (0-10, higher = more important).
func WithPriority(priority int) TaskOption {
	return func(t *Task) {
		if t.pb.Options == nil {
			t.pb.Options = &pb.TaskOptions{}
		}
		t.pb.Options.Priority = int32(priority)
	}
}

// WithUniqueKey sets a unique key for deduplication.
func WithUniqueKey(key string, ttl time.Duration) TaskOption {
	return func(t *Task) {
		if t.pb.Options == nil {
			t.pb.Options = &pb.TaskOptions{}
		}
		t.pb.Options.UniqueKey = key
		t.pb.Options.UniqueTtl = durationpb.New(ttl)
	}
}

// WithLabels sets custom labels on the task.
func WithLabels(labels map[string]string) TaskOption {
	return func(t *Task) {
		if t.pb.Options == nil {
			t.pb.Options = &pb.TaskOptions{}
		}
		t.pb.Options.Labels = labels
	}
}

// WithLabel adds a single label to the task.
func WithLabel(key, value string) TaskOption {
	return func(t *Task) {
		if t.pb.Options == nil {
			t.pb.Options = &pb.TaskOptions{}
		}
		if t.pb.Options.Labels == nil {
			t.pb.Options.Labels = make(map[string]string)
		}
		t.pb.Options.Labels[key] = value
	}
}
