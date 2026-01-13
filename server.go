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

	"github.com/sirupsen/logrus"

	"github.com/erennakbas/strego/broker"
	"github.com/erennakbas/strego/types"
)

// Default configuration values
const (
	DefaultConcurrency       = 10
	DefaultShutdownTimeout   = 10 * time.Second
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

	// Consumer cleanup (optional)
	enableConsumerCleanup bool
	cleanupInterval       time.Duration
	cleanupIdleThreshold  time.Duration

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

// WithConsumerCleanup enables automatic cleanup of dead consumers.
// This prevents consumer list bloat from frequent deployments.
// interval: how often to check for dead consumers (e.g., 10 minutes)
// threshold: min idle time before removal (e.g., 30 minutes)
func WithConsumerCleanup(interval, threshold time.Duration) ServerOption {
	return func(s *Server) {
		s.enableConsumerCleanup = true
		s.cleanupInterval = interval
		s.cleanupIdleThreshold = threshold
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

	s.logger.WithFields(logrus.Fields{
		"worker_id":   s.workerID,
		"concurrency": s.concurrency,
		"queues":      s.queues,
	}).Info("starting strego server")

	// Create a context that's cancelled on shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the scheduler for scheduled and retry tasks
	s.wg.Add(1)
	go s.runScheduler(ctx)

	// Start consumer cleanup if enabled
	if s.enableConsumerCleanup {
		s.wg.Add(1)
		go s.runConsumerCleanup(ctx)
	}

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
		s.logger.WithField("signal", sig).Info("received signal, shutting down")
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			s.logger.WithError(err).Error("broker subscription error")
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

	s.logger.WithFields(logrus.Fields{
		"task_id":     task.ID(),
		"task_type":   task.Type(),
		"queue":       task.Queue(),
		"retry_count": task.RetryCount(),
	}).Debug("processing task")

	// Check for idempotency (exactly-once processing)
	// Only check on first attempt - retries should always be processed
	if task.RetryCount() == 0 {
		shouldProcess, err := s.broker.SetProcessed(ctx, task.ID(), s.processedTTL)
		if err != nil {
			s.logger.WithField("task_id", task.ID()).WithError(err).Error("idempotency check failed")
			// On error, still try to process (fail open)
		} else if !shouldProcess {
			s.logger.WithField("task_id", task.ID()).Debug("task already processed, skipping")
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
			s.logger.WithField("task_id", task.ID()).WithError(err).Warn("failed to update task state in store")
		}
	}

	// Find the handler
	handler := s.mux.Handler(task.Type())
	if handler == nil {
		s.logger.WithField("task_type", task.Type()).Error("no handler registered for task type")
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

	// Execute the handler with panic recovery
	startTime := time.Now()
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
				s.logger.WithFields(logrus.Fields{
					"task_id":   task.ID(),
					"task_type": task.Type(),
					"panic":     r,
				}).Error("handler panicked")
			}
		}()
		err = handler.ProcessTask(taskCtx, task)
	}()
	duration := time.Since(startTime)

	if err != nil {
		s.logger.WithFields(logrus.Fields{
			"task_id":   task.ID(),
			"task_type": task.Type(),
			"duration":  duration,
		}).WithError(err).Error("task processing failed")
		return s.handleFailure(ctx, task, err)
	}

	// Success
	s.logger.WithFields(logrus.Fields{
		"task_id":   task.ID(),
		"task_type": task.Type(),
		"duration":  duration,
	}).Info("task processed successfully")

	completedAt := time.Now()
	proto.Metadata.State = types.TaskStateCompleted
	proto.Metadata.CompletedAt = &completedAt

	if s.store != nil {
		if err := s.store.UpdateTask(ctx, proto); err != nil {
			s.logger.WithField("task_id", task.ID()).WithError(err).Warn("failed to update completed task in store")
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
		s.logger.WithFields(logrus.Fields{
			"task_id":     task.ID(),
			"retry_count": proto.Metadata.RetryCount,
		}).Warn("max retries exceeded, moving to DLQ")

		if err := s.broker.MoveToDLQ(ctx, queue, proto, taskErr); err != nil {
			s.logger.WithField("task_id", task.ID()).WithError(err).Error("failed to move task to DLQ")
			return err
		}

		if s.store != nil {
			now := time.Now()
			proto.Metadata.State = types.TaskStateDead
			proto.Metadata.CompletedAt = &now
			if err := s.store.UpdateTask(ctx, proto); err != nil {
				s.logger.WithField("task_id", task.ID()).WithError(err).Warn("failed to update dead task in store")
			} else {
				s.logger.WithField("task_id", task.ID()).Debug("updated dead task in store")
			}
		}

		return ErrTaskDead
	}

	// Calculate exponential backoff
	delay := s.calculateBackoff(int(proto.Metadata.RetryCount))

	s.logger.WithFields(logrus.Fields{
		"task_id":     task.ID(),
		"retry_count": proto.Metadata.RetryCount,
		"delay":       delay,
	}).Info("scheduling retry")

	if err := s.broker.Retry(ctx, queue, proto, delay); err != nil {
		s.logger.WithField("task_id", task.ID()).WithError(err).Error("failed to schedule retry")
		return err
	}

	if s.store != nil {
		proto.Metadata.State = types.TaskStateRetry
		if err := s.store.UpdateTask(ctx, proto); err != nil {
			s.logger.WithField("task_id", task.ID()).WithError(err).Warn("failed to update retry task in store")
		} else {
			s.logger.WithFields(logrus.Fields{
				"task_id":     task.ID(),
				"retry_count": proto.Metadata.RetryCount,
			}).Debug("updated retry task in store")
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

// runConsumerCleanup periodically cleans up dead consumers from the consumer group.
func (s *Server) runConsumerCleanup(ctx context.Context) {
	defer s.wg.Done()

	interval := s.cleanupInterval
	if interval == 0 {
		interval = 10 * time.Minute // default
	}

	threshold := s.cleanupIdleThreshold
	if threshold == 0 {
		threshold = 60 * time.Minute // default: 60 minutes (safe threshold)
	}

	s.logger.WithFields(logrus.Fields{
		"interval_minutes":  interval.Minutes(),
		"threshold_minutes": threshold.Minutes(),
	}).Info("consumer cleanup started")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("consumer cleanup stopped")
			return
		case <-ticker.C:
			s.cleanupDeadConsumers(ctx, threshold)
		}
	}
}

// cleanupDeadConsumers removes consumers that have been idle for too long with no pending tasks.
func (s *Server) cleanupDeadConsumers(ctx context.Context, idleThreshold time.Duration) {
	var totalRemoved int

	// Only cleanup consumers in queues we're processing
	for _, queue := range s.queues {
		// Get consumer groups for this queue
		groups, err := s.broker.GetConsumerGroups(ctx, queue)
		if err != nil {
			continue
		}

		// Find our consumer group
		for _, group := range groups {
			consumers, err := s.broker.GetGroupConsumers(ctx, queue, group.Name)
			if err != nil {
				continue
			}

			for _, c := range consumers {
				// Skip ourselves
				if c.Name == s.workerID {
					continue
				}

				// Safety checks: only remove if no pending tasks and idle > threshold
				shouldCleanup := c.Pending == 0 &&
					c.Idle > int64(idleThreshold.Milliseconds())

				if shouldCleanup {
					err := s.broker.RemoveConsumer(ctx, queue, group.Name, c.Name)
					if err != nil {
						s.logger.WithError(err).WithFields(logrus.Fields{
							"queue":    queue,
							"group":    group.Name,
							"consumer": c.Name,
						}).Error("failed to remove consumer")
						continue
					}

					totalRemoved++
					s.logger.WithFields(logrus.Fields{
						"queue":        queue,
						"group":        group.Name,
						"consumer":     c.Name,
						"idle_minutes": c.Idle / 60000,
					}).Info("removed dead consumer")
				}
			}
		}
	}

	if totalRemoved > 0 {
		s.logger.WithField("removed_count", totalRemoved).Info("consumer cleanup completed")
	}
}

// processScheduled moves scheduled tasks that are ready to their queues.
func (s *Server) processScheduled(ctx context.Context) {
	tasks, err := s.broker.GetScheduled(ctx, time.Now(), 100)
	if err != nil {
		s.logger.WithError(err).Error("failed to get scheduled tasks")
		return
	}

	for _, task := range tasks {
		if err := s.broker.MoveToQueue(ctx, task); err != nil {
			s.logger.WithField("task_id", task.ID).WithError(err).Error("failed to move scheduled task to queue")
			continue
		}

		queue := "default"
		if task.Options != nil && task.Options.Queue != "" {
			queue = task.Options.Queue
		}

		s.logger.WithFields(logrus.Fields{
			"task_id": task.ID,
			"queue":   queue,
		}).Debug("moved scheduled task to queue")
	}
}

// processRetries moves retry tasks that are ready back to their queues.
func (s *Server) processRetries(ctx context.Context) {
	tasks, err := s.broker.GetRetry(ctx, time.Now(), 100)
	if err != nil {
		s.logger.WithError(err).Error("failed to get retry tasks")
		return
	}

	if len(tasks) > 0 {
		s.logger.WithField("count", len(tasks)).Info("processing retry tasks")
	}

	for _, task := range tasks {
		if err := s.broker.MoveToQueue(ctx, task); err != nil {
			s.logger.WithField("task_id", task.ID).WithError(err).Error("failed to move retry task to queue")
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

		s.logger.WithFields(logrus.Fields{
			"task_id":     task.ID,
			"queue":       queue,
			"retry_count": retryCount,
		}).Info("moved retry task to queue")
	}
}
