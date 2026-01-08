// Package postgres provides a PostgreSQL implementation of the Store interface.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/erennakbas/strego/internal/proto"
	"github.com/erennakbas/strego/pkg/store"
)

// ErrTaskNotFound is returned when a task is not found.
var ErrTaskNotFound = errors.New("task not found")

// Store implements store.Store using PostgreSQL.
type Store struct {
	db *sql.DB
}

// Config configures the PostgreSQL store.
type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// New creates a new PostgreSQL store.
func New(cfg Config) (*Store, error) {
	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	s := &Store{db: db}

	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return s, nil
}

// NewWithDB creates a new PostgreSQL store with an existing database connection.
func NewWithDB(db *sql.DB) (*Store, error) {
	s := &Store{db: db}

	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return s, nil
}

// migrate creates the necessary tables if they don't exist.
func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS strego_tasks (
		id UUID PRIMARY KEY,
		type VARCHAR(255) NOT NULL,
		queue VARCHAR(255) NOT NULL DEFAULT 'default',
		state VARCHAR(50) NOT NULL DEFAULT 'pending',
		payload BYTEA NOT NULL,
		error TEXT,
		retry_count INT DEFAULT 0,
		max_retry INT DEFAULT 3,
		priority INT DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		scheduled_at TIMESTAMPTZ,
		started_at TIMESTAMPTZ,
		completed_at TIMESTAMPTZ,
		worker_id VARCHAR(255),
		trace_id VARCHAR(64),
		unique_key VARCHAR(255),
		labels JSONB DEFAULT '{}',
		
		CONSTRAINT valid_state CHECK (state IN (
			'pending', 'scheduled', 'active', 
			'completed', 'failed', 'retry', 'dead', 'cancelled'
		))
	);

	CREATE INDEX IF NOT EXISTS idx_strego_tasks_queue_state ON strego_tasks(queue, state);
	CREATE INDEX IF NOT EXISTS idx_strego_tasks_state ON strego_tasks(state) WHERE state IN ('pending', 'active', 'retry');
	CREATE INDEX IF NOT EXISTS idx_strego_tasks_type ON strego_tasks(type);
	CREATE INDEX IF NOT EXISTS idx_strego_tasks_created ON strego_tasks(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_strego_tasks_scheduled ON strego_tasks(scheduled_at) WHERE state = 'scheduled';
	CREATE UNIQUE INDEX IF NOT EXISTS idx_strego_tasks_unique ON strego_tasks(unique_key) WHERE unique_key IS NOT NULL;
	CREATE INDEX IF NOT EXISTS idx_strego_tasks_completed ON strego_tasks(completed_at) WHERE state IN ('completed', 'dead');
	`

	_, err := s.db.Exec(schema)
	return err
}

// stateToString converts a protobuf TaskState to a string.
func stateToString(state pb.TaskState) string {
	switch state {
	case pb.TaskState_TASK_STATE_PENDING:
		return "pending"
	case pb.TaskState_TASK_STATE_SCHEDULED:
		return "scheduled"
	case pb.TaskState_TASK_STATE_ACTIVE:
		return "active"
	case pb.TaskState_TASK_STATE_COMPLETED:
		return "completed"
	case pb.TaskState_TASK_STATE_FAILED:
		return "failed"
	case pb.TaskState_TASK_STATE_RETRY:
		return "retry"
	case pb.TaskState_TASK_STATE_DEAD:
		return "dead"
	case pb.TaskState_TASK_STATE_CANCELLED:
		return "cancelled"
	default:
		return "pending"
	}
}

// stringToState converts a string to a protobuf TaskState.
func stringToState(state string) pb.TaskState {
	switch state {
	case "pending":
		return pb.TaskState_TASK_STATE_PENDING
	case "scheduled":
		return pb.TaskState_TASK_STATE_SCHEDULED
	case "active":
		return pb.TaskState_TASK_STATE_ACTIVE
	case "completed":
		return pb.TaskState_TASK_STATE_COMPLETED
	case "failed":
		return pb.TaskState_TASK_STATE_FAILED
	case "retry":
		return pb.TaskState_TASK_STATE_RETRY
	case "dead":
		return pb.TaskState_TASK_STATE_DEAD
	case "cancelled":
		return pb.TaskState_TASK_STATE_CANCELLED
	default:
		return pb.TaskState_TASK_STATE_UNSPECIFIED
	}
}

// CreateTask saves a new task to the store.
func (s *Store) CreateTask(ctx context.Context, task *pb.Task) error {
	payload, err := proto.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to marshal task: %w", err)
	}

	state := "pending"
	if task.Metadata != nil {
		state = stateToString(task.Metadata.State)
	}

	queue := "default"
	maxRetry := 3
	priority := 0
	var uniqueKey *string
	var scheduledAt *time.Time
	labels := "{}"

	if task.Options != nil {
		if task.Options.Queue != "" {
			queue = task.Options.Queue
		}
		if task.Options.MaxRetry > 0 {
			maxRetry = int(task.Options.MaxRetry)
		}
		priority = int(task.Options.Priority)
		if task.Options.UniqueKey != "" {
			uniqueKey = &task.Options.UniqueKey
		}
		if task.Options.ProcessAt != nil {
			t := task.Options.ProcessAt.AsTime()
			scheduledAt = &t
		}
		if task.Options.Labels != nil {
			labelsJSON, _ := json.Marshal(task.Options.Labels)
			labels = string(labelsJSON)
		}
	}

	var createdAt time.Time
	if task.Metadata != nil && task.Metadata.CreatedAt != nil {
		createdAt = task.Metadata.CreatedAt.AsTime()
	} else {
		createdAt = time.Now()
	}

	query := `
		INSERT INTO strego_tasks (
			id, type, queue, state, payload, 
			max_retry, priority, created_at, scheduled_at, 
			unique_key, labels
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO NOTHING
	`

	_, err = s.db.ExecContext(ctx, query,
		task.Id,
		task.Type,
		queue,
		state,
		payload,
		maxRetry,
		priority,
		createdAt,
		scheduledAt,
		uniqueKey,
		labels,
	)

	if err != nil {
		return fmt.Errorf("failed to insert task: %w", err)
	}

	return nil
}

// UpdateTask updates an existing task.
func (s *Store) UpdateTask(ctx context.Context, task *pb.Task) error {
	state := "pending"
	retryCount := 0
	var startedAt, completedAt *time.Time
	var lastError *string
	var workerID *string

	if task.Metadata != nil {
		state = stateToString(task.Metadata.State)
		retryCount = int(task.Metadata.RetryCount)
		if task.Metadata.StartedAt != nil {
			t := task.Metadata.StartedAt.AsTime()
			startedAt = &t
		}
		if task.Metadata.CompletedAt != nil {
			t := task.Metadata.CompletedAt.AsTime()
			completedAt = &t
		}
		if task.Metadata.LastError != "" {
			lastError = &task.Metadata.LastError
		}
		if task.Metadata.WorkerId != "" {
			workerID = &task.Metadata.WorkerId
		}
	}

	query := `
		UPDATE strego_tasks SET
			state = $2,
			retry_count = $3,
			started_at = $4,
			completed_at = $5,
			error = $6,
			worker_id = $7
		WHERE id = $1
	`

	result, err := s.db.ExecContext(ctx, query,
		task.Id,
		state,
		retryCount,
		startedAt,
		completedAt,
		lastError,
		workerID,
	)

	if err != nil {
		return fmt.Errorf("failed to update task: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		// Task doesn't exist, create it
		return s.CreateTask(ctx, task)
	}

	return nil
}

// UpdateTaskState updates only the state and error of a task.
func (s *Store) UpdateTaskState(ctx context.Context, taskID, state, errMsg string) error {
	var completedAt *time.Time
	if state == "completed" || state == "dead" || state == "cancelled" {
		now := time.Now()
		completedAt = &now
	}

	query := `
		UPDATE strego_tasks SET
			state = $2,
			error = CASE WHEN $3 = '' THEN error ELSE $3 END,
			completed_at = COALESCE($4, completed_at)
		WHERE id = $1
	`

	_, err := s.db.ExecContext(ctx, query, taskID, state, errMsg, completedAt)
	if err != nil {
		return fmt.Errorf("failed to update task state: %w", err)
	}

	return nil
}

// GetTask retrieves a task by ID.
func (s *Store) GetTask(ctx context.Context, taskID string) (*pb.Task, error) {
	query := `
		SELECT id, type, queue, state, payload, error, retry_count, max_retry,
		       priority, created_at, scheduled_at, started_at, completed_at,
		       worker_id, trace_id, unique_key, labels
		FROM strego_tasks WHERE id = $1
	`

	row := s.db.QueryRowContext(ctx, query, taskID)

	var (
		id, taskType, queue, state          string
		payload                             []byte
		errMsg, workerID, traceID           sql.NullString
		uniqueKey                           sql.NullString
		retryCount, maxRetry, priority      int
		createdAt                           time.Time
		scheduledAt, startedAt, completedAt sql.NullTime
		labelsJSON                          string
	)

	err := row.Scan(
		&id, &taskType, &queue, &state, &payload, &errMsg,
		&retryCount, &maxRetry, &priority, &createdAt,
		&scheduledAt, &startedAt, &completedAt,
		&workerID, &traceID, &uniqueKey, &labelsJSON,
	)

	if err == sql.ErrNoRows {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

	task := &pb.Task{
		Id:      id,
		Type:    taskType,
		Payload: payload,
		Options: &pb.TaskOptions{
			Queue:    queue,
			MaxRetry: int32(maxRetry),
			Priority: int32(priority),
		},
		Metadata: &pb.TaskMetadata{
			State:      stringToState(state),
			RetryCount: int32(retryCount),
			CreatedAt:  timestamppb.New(createdAt),
		},
	}

	if errMsg.Valid {
		task.Metadata.LastError = errMsg.String
	}
	if workerID.Valid {
		task.Metadata.WorkerId = workerID.String
	}
	if traceID.Valid {
		task.Metadata.TraceId = traceID.String
	}
	if uniqueKey.Valid {
		task.Options.UniqueKey = uniqueKey.String
	}
	if scheduledAt.Valid {
		task.Options.ProcessAt = timestamppb.New(scheduledAt.Time)
	}
	if startedAt.Valid {
		task.Metadata.StartedAt = timestamppb.New(startedAt.Time)
	}
	if completedAt.Valid {
		task.Metadata.CompletedAt = timestamppb.New(completedAt.Time)
	}

	if labelsJSON != "" && labelsJSON != "{}" {
		var labels map[string]string
		if err := json.Unmarshal([]byte(labelsJSON), &labels); err == nil {
			task.Options.Labels = labels
		}
	}

	return task, nil
}

// ListTasks retrieves tasks matching the filter.
func (s *Store) ListTasks(ctx context.Context, filter store.TaskFilter) ([]*pb.Task, int64, error) {
	var conditions []string
	var args []interface{}
	argIdx := 1

	if filter.Queue != "" {
		conditions = append(conditions, fmt.Sprintf("queue = $%d", argIdx))
		args = append(args, filter.Queue)
		argIdx++
	}
	// Support both single state and multiple states
	if len(filter.States) > 0 {
		placeholders := make([]string, len(filter.States))
		for i, state := range filter.States {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, state)
			argIdx++
		}
		conditions = append(conditions, fmt.Sprintf("state IN (%s)", strings.Join(placeholders, ", ")))
	} else if filter.State != "" {
		conditions = append(conditions, fmt.Sprintf("state = $%d", argIdx))
		args = append(args, filter.State)
		argIdx++
	}
	if filter.Type != "" {
		conditions = append(conditions, fmt.Sprintf("type = $%d", argIdx))
		args = append(args, filter.Type)
		argIdx++
	}
	if filter.Search != "" {
		conditions = append(conditions, fmt.Sprintf("(id::text ILIKE $%d OR type ILIKE $%d)", argIdx, argIdx+1))
		search := "%" + filter.Search + "%"
		args = append(args, search, search)
		argIdx += 2
	}
	if !filter.CreatedAfter.IsZero() {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, filter.CreatedAfter)
		argIdx++
	}
	if !filter.CreatedBefore.IsZero() {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, filter.CreatedBefore)
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Get total count
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM strego_tasks %s", whereClause)
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count tasks: %w", err)
	}

	// Build order clause
	orderBy := "created_at"
	if filter.SortBy != "" {
		switch filter.SortBy {
		case "created_at", "started_at", "completed_at", "type", "queue", "state":
			orderBy = filter.SortBy
		}
	}
	orderDir := "DESC"
	if filter.SortOrder == "asc" {
		orderDir = "ASC"
	}

	// Apply pagination
	limit := 50
	if filter.Limit > 0 && filter.Limit <= 1000 {
		limit = filter.Limit
	}

	query := fmt.Sprintf(`
		SELECT id, type, queue, state, payload, error, retry_count, max_retry,
		       priority, created_at, scheduled_at, started_at, completed_at,
		       worker_id, trace_id, unique_key, labels
		FROM strego_tasks %s
		ORDER BY %s %s
		LIMIT %d OFFSET %d
	`, whereClause, orderBy, orderDir, limit, filter.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*pb.Task
	for rows.Next() {
		var (
			id, taskType, queue, state          string
			payload                             []byte
			errMsg, workerID, traceID           sql.NullString
			uniqueKey                           sql.NullString
			retryCount, maxRetry, priority      int
			createdAt                           time.Time
			scheduledAt, startedAt, completedAt sql.NullTime
			labelsJSON                          string
		)

		err := rows.Scan(
			&id, &taskType, &queue, &state, &payload, &errMsg,
			&retryCount, &maxRetry, &priority, &createdAt,
			&scheduledAt, &startedAt, &completedAt,
			&workerID, &traceID, &uniqueKey, &labelsJSON,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan task: %w", err)
		}

		task := &pb.Task{
			Id:      id,
			Type:    taskType,
			Payload: payload,
			Options: &pb.TaskOptions{
				Queue:    queue,
				MaxRetry: int32(maxRetry),
				Priority: int32(priority),
			},
			Metadata: &pb.TaskMetadata{
				State:      stringToState(state),
				RetryCount: int32(retryCount),
				CreatedAt:  timestamppb.New(createdAt),
			},
		}

		if errMsg.Valid {
			task.Metadata.LastError = errMsg.String
		}
		if workerID.Valid {
			task.Metadata.WorkerId = workerID.String
		}
		if startedAt.Valid {
			task.Metadata.StartedAt = timestamppb.New(startedAt.Time)
		}
		if completedAt.Valid {
			task.Metadata.CompletedAt = timestamppb.New(completedAt.Time)
		}

		tasks = append(tasks, task)
	}

	return tasks, total, nil
}

// CountTasks returns the count of tasks matching the filter.
func (s *Store) CountTasks(ctx context.Context, filter store.TaskFilter) (int64, error) {
	var conditions []string
	var args []interface{}
	argIdx := 1

	if filter.Queue != "" {
		conditions = append(conditions, fmt.Sprintf("queue = $%d", argIdx))
		args = append(args, filter.Queue)
		argIdx++
	}
	if filter.State != "" {
		conditions = append(conditions, fmt.Sprintf("state = $%d", argIdx))
		args = append(args, filter.State)
		argIdx++
	}
	if filter.Type != "" {
		conditions = append(conditions, fmt.Sprintf("type = $%d", argIdx))
		args = append(args, filter.Type)
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf("SELECT COUNT(*) FROM strego_tasks %s", whereClause)

	var count int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count tasks: %w", err)
	}

	return count, nil
}

// DeleteTask removes a task from the store.
func (s *Store) DeleteTask(ctx context.Context, taskID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM strego_tasks WHERE id = $1", taskID)
	if err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}
	return nil
}

// DeleteOldTasks removes tasks older than the specified duration.
func (s *Store) DeleteOldTasks(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	result, err := s.db.ExecContext(ctx, `
		DELETE FROM strego_tasks 
		WHERE state IN ('completed', 'dead', 'cancelled') 
		AND completed_at < $1
	`, cutoff)

	if err != nil {
		return 0, fmt.Errorf("failed to delete old tasks: %w", err)
	}

	rows, _ := result.RowsAffected()
	return rows, nil
}

// GetStats returns task statistics.
func (s *Store) GetStats(ctx context.Context) (*store.Stats, error) {
	query := `
		SELECT 
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE state = 'pending') as pending,
			COUNT(*) FILTER (WHERE state = 'active') as active,
			COUNT(*) FILTER (WHERE state = 'completed') as completed,
			COUNT(*) FILTER (WHERE state = 'failed') as failed,
			COUNT(*) FILTER (WHERE state = 'dead') as dead,
			COUNT(*) FILTER (WHERE state = 'retry') as retry,
			COUNT(*) FILTER (WHERE state = 'scheduled') as scheduled
		FROM strego_tasks
	`

	stats := &store.Stats{}
	err := s.db.QueryRowContext(ctx, query).Scan(
		&stats.TotalTasks,
		&stats.PendingTasks,
		&stats.ActiveTasks,
		&stats.CompletedTasks,
		&stats.FailedTasks,
		&stats.DeadTasks,
		&stats.RetryTasks,
		&stats.ScheduledTasks,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}

	return stats, nil
}

// GetQueueStats returns statistics for a specific queue.
func (s *Store) GetQueueStats(ctx context.Context, queue string) (*store.QueueStats, error) {
	query := `
		SELECT 
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE state = 'pending') as pending,
			COUNT(*) FILTER (WHERE state = 'active') as active,
			COUNT(*) FILTER (WHERE state = 'completed') as completed,
			COUNT(*) FILTER (WHERE state = 'failed') as failed,
			COUNT(*) FILTER (WHERE state = 'dead') as dead,
			COUNT(*) FILTER (WHERE state = 'retry') as retry,
			COUNT(*) FILTER (WHERE state = 'scheduled') as scheduled,
			COALESCE(AVG(EXTRACT(EPOCH FROM (completed_at - started_at))) FILTER (WHERE completed_at IS NOT NULL AND started_at IS NOT NULL), 0) as avg_duration
		FROM strego_tasks
		WHERE queue = $1
	`

	stats := &store.QueueStats{Queue: queue}
	err := s.db.QueryRowContext(ctx, query, queue).Scan(
		&stats.TotalTasks,
		&stats.PendingTasks,
		&stats.ActiveTasks,
		&stats.CompletedTasks,
		&stats.FailedTasks,
		&stats.DeadTasks,
		&stats.RetryTasks,
		&stats.ScheduledTasks,
		&stats.AvgDuration,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get queue stats: %w", err)
	}

	return stats, nil
}

// Ping checks if the store is healthy.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Close closes the store connection.
func (s *Store) Close() error {
	return s.db.Close()
}
