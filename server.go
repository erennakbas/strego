package strego

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/erennakbas/strego/broker"
	"github.com/erennakbas/strego/types"
)

// Default configuration values
const (
	DefaultConcurrency       = 10
	DefaultShutdownTimeout   = 30 * time.Second
	DefaultProcessedTTL      = 24 * time.Hour
	DefaultSchedulerTick     = 1 * time.Second
	DefaultRetryBaseDuration = 1 * time.Second
	DefaultRetryMaxDuration  = 10 * time.Minute
)

// ErrTaskRetried is returned when a task has been scheduled for retry.
// This is not a real error - it signals that the task should not be ACKed.
var ErrTaskRetried = errors.New("task scheduled for retry")

// ErrTaskDead is returned when a task has been moved to the dead letter queue.
// This is not a real error - it signals that the task should not be ACKed.
var ErrTaskDead = errors.New("task moved to dead letter queue")

// Server processes tasks from queues.
type Server struct {
	broker   broker.Broker
	store    Store
	mux      *ServeMux
	logger   Logger
	workerID string

	// Configuration
	concurrency       int
	queues            []string
	shutdownTimeout   time.Duration
	processedTTL      time.Duration
	schedulerTick     time.Duration
	retryBaseDuration time.Duration
	retryMaxDuration  time.Duration

	// State
	mu       sync.Mutex
	running  bool
	shutdown chan struct{}
	wg       sync.WaitGroup
}

// ServerOption configures the server.
type ServerOption func(*Server)

// WithConcurrency sets the number of concurrent workers.
func WithConcurrency(n int) ServerOption {
	return func(s *Server) {
		if n > 0 {
			s.concurrency = n
		}
	}
}

// WithQueues sets the queues to process.
// If not set, only "default" queue is processed.
func WithQueues(queues ...string) ServerOption {
	return func(s *Server) {
		s.queues = queues
	}
}

// WithServerStore sets the optional store for task persistence.
func WithServerStore(store Store) ServerOption {
	return func(s *Server) {
		s.store = store
	}
}

// WithServerLogger sets the logger for the server.
func WithServerLogger(logger Logger) ServerOption {
	return func(s *Server) {
		s.logger = logger
	}
}

// WithShutdownTimeout sets the graceful shutdown timeout.
func WithShutdownTimeout(d time.Duration) ServerOption {
	return func(s *Server) {
		s.shutdownTimeout = d
	}
}

// WithProcessedTTL sets how long to remember processed tasks for idempotency.
func WithProcessedTTL(d time.Duration) ServerOption {
	return func(s *Server) {
		s.processedTTL = d
	}
}

// WithRetryConfig sets the retry backoff configuration.
func WithRetryConfig(base, max time.Duration) ServerOption {
	return func(s *Server) {
		s.retryBaseDuration = base
		s.retryMaxDuration = max
	}
}

// NewServer creates a new task processing server.
func NewServer(b broker.Broker, opts ...ServerOption) *Server {
	hostname, _ := os.Hostname()
	workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())

	s := &Server{
		broker:            b,
		mux:               NewServeMux(),
		logger:            defaultLogger(),
		workerID:          workerID,
		concurrency:       DefaultConcurrency,
		queues:            []string{"default"},
		shutdownTimeout:   DefaultShutdownTimeout,
		processedTTL:      DefaultProcessedTTL,
		schedulerTick:     DefaultSchedulerTick,
		retryBaseDuration: DefaultRetryBaseDuration,
		retryMaxDuration:  DefaultRetryMaxDuration,
		shutdown:          make(chan struct{}),
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Handle registers a handler for the given task type.
func (s *Server) Handle(taskType string, handler Handler) {
	s.mux.Handle(taskType, handler)
}

// HandleFunc registers a handler function for the given task type.
func (s *Server) HandleFunc(taskType string, fn func(ctx context.Context, task *Task) error) {
	s.mux.HandleFunc(taskType, fn)
}

// Start begins processing tasks.
// This method blocks until Shutdown is called or a termination signal is received.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("server is already running")
	}
	s.running = true
	s.shutdown = make(chan struct{})
	s.mu.Unlock()

	s.logger.Info("starting strego server",
		"worker_id", s.workerID,
		"concurrency", s.concurrency,
		"queues", s.queues)

	// Create a context that's cancelled on shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the scheduler for scheduled and retry tasks
	s.wg.Add(1)
	go s.runScheduler(ctx)

	// Setup signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Start processing
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.broker.Subscribe(ctx, s.queues, s.processTask)
	}()

	// Wait for shutdown signal or error
	select {
	case sig := <-sigCh:
		s.logger.Info("received signal, shutting down", "signal", sig)
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Error("broker subscription error", "error", err)
			return err
		}
	case <-s.shutdown:
		s.logger.Info("shutdown requested")
	}

	// Cancel context to stop all goroutines
	cancel()

	// Wait for graceful shutdown
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Info("graceful shutdown complete")
	case <-time.After(s.shutdownTimeout):
		s.logger.Warn("shutdown timeout exceeded, forcing exit")
	}

	s.mu.Lock()
	s.running = false
	s.mu.Unlock()

	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		close(s.shutdown)
	}
}

// processTask handles a single task from the broker.
func (s *Server) processTask(ctx context.Context, proto *types.TaskProto) error {
	task := TaskFromProto(proto)

	s.logger.Debug("processing task",
		"task_id", task.ID(),
		"task_type", task.Type(),
		"queue", task.Queue(),
		"retry_count", task.RetryCount())

	// Check for idempotency (exactly-once processing)
	// Only check on first attempt - retries should always be processed
	if task.RetryCount() == 0 {
		shouldProcess, err := s.broker.SetProcessed(ctx, task.ID(), s.processedTTL)
		if err != nil {
			s.logger.Error("idempotency check failed", "task_id", task.ID(), "error", err)
			// On error, still try to process (fail open)
		} else if !shouldProcess {
			s.logger.Debug("task already processed, skipping",
				"task_id", task.ID())
			return nil // Already processed
		}
	}

	// Update task state to active
	now := time.Now()
	proto.Metadata.State = types.TaskStateActive
	proto.Metadata.StartedAt = &now
	proto.Metadata.WorkerID = s.workerID

	if s.store != nil {
		if err := s.store.UpdateTask(ctx, proto); err != nil {
			s.logger.Warn("failed to update task state in store",
				"task_id", task.ID(), "error", err)
		}
	}

	// Find the handler
	handler := s.mux.Handler(task.Type())
	if handler == nil {
		s.logger.Error("no handler registered for task type",
			"task_type", task.Type())
		// Move to DLQ as we can't process it
		return s.handleFailure(ctx, task, fmt.Errorf("no handler for task type: %s", task.Type()))
	}

	// Create a timeout context if configured
	taskCtx := ctx
	var taskCancel context.CancelFunc
	if proto.Options != nil && proto.Options.Timeout > 0 {
		taskCtx, taskCancel = context.WithTimeout(ctx, proto.Options.Timeout)
		defer taskCancel()
	}

	// Execute the handler
	startTime := time.Now()
	err := handler.ProcessTask(taskCtx, task)
	duration := time.Since(startTime)

	if err != nil {
		s.logger.Error("task processing failed",
			"task_id", task.ID(),
			"task_type", task.Type(),
			"duration", duration,
			"error", err)
		return s.handleFailure(ctx, task, err)
	}

	// Success
	s.logger.Info("task processed successfully",
		"task_id", task.ID(),
		"task_type", task.Type(),
		"duration", duration)

	completedAt := time.Now()
	proto.Metadata.State = types.TaskStateCompleted
	proto.Metadata.CompletedAt = &completedAt

	if s.store != nil {
		if err := s.store.UpdateTask(ctx, proto); err != nil {
			s.logger.Warn("failed to update completed task in store",
				"task_id", task.ID(), "error", err)
		}
	}

	return nil
}

// handleFailure decides whether to retry or move to DLQ.
func (s *Server) handleFailure(ctx context.Context, task *Task, taskErr error) error {
	proto := task.Proto()

	// Increment retry count
	if proto.Metadata == nil {
		proto.Metadata = &types.TaskMetadata{}
	}
	proto.Metadata.RetryCount++
	proto.Metadata.LastError = taskErr.Error()

	maxRetry := int32(3)
	if proto.Options != nil && proto.Options.MaxRetry > 0 {
		maxRetry = proto.Options.MaxRetry
	}

	queue := task.Queue()

	if proto.Metadata.RetryCount >= maxRetry {
		// Max retries exceeded, move to DLQ
		s.logger.Warn("max retries exceeded, moving to DLQ",
			"task_id", task.ID(),
			"retry_count", proto.Metadata.RetryCount)

		if err := s.broker.MoveToDLQ(ctx, queue, proto, taskErr); err != nil {
			s.logger.Error("failed to move task to DLQ",
				"task_id", task.ID(), "error", err)
			return err
		}

		if s.store != nil {
			now := time.Now()
			proto.Metadata.State = types.TaskStateDead
			proto.Metadata.CompletedAt = &now
			if err := s.store.UpdateTask(ctx, proto); err != nil {
				s.logger.Warn("failed to update dead task in store",
					"task_id", task.ID(), "error", err)
			} else {
				s.logger.Debug("updated dead task in store", "task_id", task.ID())
			}
		}

		return ErrTaskDead
	}

	// Calculate exponential backoff
	delay := s.calculateBackoff(int(proto.Metadata.RetryCount))

	s.logger.Info("scheduling retry",
		"task_id", task.ID(),
		"retry_count", proto.Metadata.RetryCount,
		"delay", delay)

	if err := s.broker.Retry(ctx, queue, proto, delay); err != nil {
		s.logger.Error("failed to schedule retry",
			"task_id", task.ID(), "error", err)
		return err
	}

	if s.store != nil {
		proto.Metadata.State = types.TaskStateRetry
		if err := s.store.UpdateTask(ctx, proto); err != nil {
			s.logger.Warn("failed to update retry task in store",
				"task_id", task.ID(), "error", err)
		} else {
			s.logger.Debug("updated retry task in store", "task_id", task.ID(), "retry_count", proto.Metadata.RetryCount)
		}
	}

	return ErrTaskRetried
}

// calculateBackoff returns the delay for the given retry attempt using exponential backoff.
func (s *Server) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: base * 2^attempt
	delay := s.retryBaseDuration * time.Duration(math.Pow(2, float64(attempt-1)))

	if delay > s.retryMaxDuration {
		delay = s.retryMaxDuration
	}

	return delay
}

// runScheduler periodically checks for scheduled and retry tasks.
func (s *Server) runScheduler(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.schedulerTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processScheduled(ctx)
			s.processRetries(ctx)
		}
	}
}

// processScheduled moves scheduled tasks that are ready to their queues.
func (s *Server) processScheduled(ctx context.Context) {
	tasks, err := s.broker.GetScheduled(ctx, time.Now(), 100)
	if err != nil {
		s.logger.Error("failed to get scheduled tasks", "error", err)
		return
	}

	for _, task := range tasks {
		if err := s.broker.MoveToQueue(ctx, task); err != nil {
			s.logger.Error("failed to move scheduled task to queue",
				"task_id", task.ID, "error", err)
			continue
		}

		queue := "default"
		if task.Options != nil && task.Options.Queue != "" {
			queue = task.Options.Queue
		}

		s.logger.Debug("moved scheduled task to queue",
			"task_id", task.ID,
			"queue", queue)
	}
}

// processRetries moves retry tasks that are ready back to their queues.
func (s *Server) processRetries(ctx context.Context) {
	tasks, err := s.broker.GetRetry(ctx, time.Now(), 100)
	if err != nil {
		s.logger.Error("failed to get retry tasks", "error", err)
		return
	}

	if len(tasks) > 0 {
		s.logger.Info("processing retry tasks", "count", len(tasks))
	}

	for _, task := range tasks {
		if err := s.broker.MoveToQueue(ctx, task); err != nil {
			s.logger.Error("failed to move retry task to queue",
				"task_id", task.ID, "error", err)
			continue
		}

		queue := "default"
		if task.Options != nil && task.Options.Queue != "" {
			queue = task.Options.Queue
		}

		retryCount := int32(0)
		if task.Metadata != nil {
			retryCount = task.Metadata.RetryCount
		}

		s.logger.Info("moved retry task to queue",
			"task_id", task.ID,
			"queue", queue,
			"retry_count", retryCount)
	}
}
