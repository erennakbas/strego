// Package main demonstrates strego with PostgreSQL - production-like example.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/erennakbas/strego"
	"github.com/erennakbas/strego/broker"
	brokerRedis "github.com/erennakbas/strego/broker/redis"
	"github.com/erennakbas/strego/store/postgres"
	"github.com/erennakbas/strego/ui"
)

// Failure simulation counters
var (
	emailAttempts        atomic.Int32
	orderAttempts        atomic.Int32
	notificationAttempts atomic.Int32
)

func main() {
	// Setup logger
	slogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(slogger)
	logger := strego.NewSlogLogger(slogger)

	ctx := context.Background()

	// ============================================
	// Connect to Redis
	// ============================================
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("failed to connect to redis: %v", err)
	}
	slogger.Info("✅ connected to redis")

	// ============================================
	// Connect to PostgreSQL
	// ============================================
	pgStore, err := postgres.New(postgres.Config{
		DSN:             "postgres://erenakbas:admin123@localhost:5432/picus_ng_test?sslmode=disable",
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
	})
	if err != nil {
		log.Fatalf("failed to connect to postgresql: %v", err)
	}
	defer pgStore.Close()
	slogger.Info("✅ connected to postgresql")

	// ============================================
	// Create Broker
	// ============================================
	b := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
		Group:           "strego-prod-example",
		BatchSize:       10,
		BlockDuration:   5 * time.Second,
		ClaimStaleAfter: 5 * time.Minute,
	}))

	// ============================================
	// Create Client
	// ============================================
	client := strego.NewClient(b,
		strego.WithStore(pgStore),
		strego.WithClientLogger(logger),
	)

	// ============================================
	// Create Server
	// ============================================
	server := strego.NewServer(b,
		strego.WithConcurrency(5),
		strego.WithQueues("default", "critical"),
		strego.WithServerStore(pgStore),
		strego.WithServerLogger(logger),
		strego.WithShutdownTimeout(30*time.Second),
		strego.WithProcessedTTL(24*time.Hour),
		strego.WithRetryConfig(2*time.Second, 30*time.Second), // Fast retry for demo
	)

	// ============================================
	// Register Handlers
	// ============================================
	server.HandleFunc("email:welcome", handleWelcomeEmail)
	server.HandleFunc("email:newsletter", handleNewsletterEmail)
	server.HandleFunc("order:confirm", handleOrderConfirm)
	server.HandleFunc("notification:push", handleNotification)

	// ============================================
	// Start UI Server
	// ============================================
	uiServer, err := ui.NewServer(ui.Config{
		Addr:   ":8080",
		Broker: b,
		Store:  pgStore,
		Logger: logger,
	})
	if err != nil {
		log.Fatalf("failed to create UI server: %v", err)
	}

	go func() {
		slogger.Info("🌐 UI ready at http://localhost:8080")
		if err := uiServer.Start(); err != nil {
			slogger.Error("UI server error", "error", err)
		}
	}()

	// ============================================
	// Run Scenarios
	// ============================================
	go runScenarios(ctx, client, logger)

	// ============================================
	// Handle Shutdown
	// ============================================
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		slogger.Info("🛑 shutting down...")
		server.Shutdown()
		uiServer.Shutdown(context.Background())
	}()

	// ============================================
	// Start Processing
	// ============================================
	slogger.Info("🚀 starting task processing...")
	if err := server.Start(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// ============================================
// SCENARIOS
// ============================================

func runScenarios(ctx context.Context, client *strego.Client, logger strego.Logger) {
	time.Sleep(2 * time.Second)

	// ========== PHASE 1: All Success ==========
	logger.Info("=" + strings.Repeat("=", 50))
	logger.Info("📗 PHASE 1: All tasks will succeed")
	logger.Info("=" + strings.Repeat("=", 50))

	// Welcome emails - always succeed
	for i := 1; i <= 5; i++ {
		task := strego.NewTaskFromBytes("email:welcome",
			[]byte(fmt.Sprintf(`{"user_id":%d,"email":"user%d@example.com"}`, i, i)),
			strego.WithQueue("default"),
			strego.WithMaxRetry(3),
		)
		info, _ := client.Enqueue(ctx, task)
		logger.Info("📧 enqueued welcome email", "task_id", info.ID[:8])
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for processing
	logger.Info("⏳ waiting 10 seconds for Phase 1 to complete...")
	time.Sleep(10 * time.Second)

	// ========== PHASE 2: 1 Retry Needed ==========
	logger.Info("=" + strings.Repeat("=", 50))
	logger.Info("📙 PHASE 2: Tasks will fail once, then succeed on retry")
	logger.Info("=" + strings.Repeat("=", 50))

	// Reset counters
	emailAttempts.Store(0)

	// Newsletter emails - fail once, succeed on retry
	for i := 1; i <= 5; i++ {
		task := strego.NewTaskFromBytes("email:newsletter",
			[]byte(fmt.Sprintf(`{"campaign_id":"CAMP-%d","recipient":"subscriber%d@example.com"}`, i, i)),
			strego.WithQueue("default"),
			strego.WithMaxRetry(3),
		)
		info, _ := client.Enqueue(ctx, task)
		logger.Info("📰 enqueued newsletter", "task_id", info.ID[:8])
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for retries
	logger.Info("⏳ waiting 30 seconds for Phase 2 retries...")
	time.Sleep(30 * time.Second)

	// ========== PHASE 3: 2 Retries, Some Fail ==========
	logger.Info("=" + strings.Repeat("=", 50))
	logger.Info("📕 PHASE 3: Tasks will fail twice, some go to DLQ")
	logger.Info("=" + strings.Repeat("=", 50))

	// Reset counters
	orderAttempts.Store(0)
	notificationAttempts.Store(0)

	// Order confirmations - fail twice, succeed on 3rd (max_retry=3)
	for i := 1; i <= 3; i++ {
		task := strego.NewTaskFromBytes("order:confirm",
			[]byte(fmt.Sprintf(`{"order_id":"ORD-%d","amount":%.2f}`, 1000+i, float64(i)*49.99)),
			strego.WithQueue("critical"),
			strego.WithMaxRetry(3),
		)
		info, _ := client.Enqueue(ctx, task)
		logger.Info("🛒 enqueued order confirmation", "task_id", info.ID[:8])
		time.Sleep(200 * time.Millisecond)
	}

	// Notifications - fail twice, max_retry=2 so they go to DLQ
	for i := 1; i <= 3; i++ {
		task := strego.NewTaskFromBytes("notification:push",
			[]byte(fmt.Sprintf(`{"user_id":%d,"message":"Important alert #%d"}`, i, i)),
			strego.WithQueue("critical"),
			strego.WithMaxRetry(2), // Will go to DLQ after 2 failures
		)
		info, _ := client.Enqueue(ctx, task)
		logger.Info("🔔 enqueued notification (will fail!)", "task_id", info.ID[:8])
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for retries and DLQ
	logger.Info("⏳ waiting 30 seconds for Phase 3 retries and DLQ...")
	time.Sleep(30 * time.Second)

	logger.Info("=" + strings.Repeat("=", 50))
	logger.Info("✅ ALL SCENARIOS COMPLETE")
	logger.Info("📊 Check the dashboard at http://localhost:8080")
	logger.Info("💀 Check Dead Letter Queue for failed notifications")
	logger.Info("=" + strings.Repeat("=", 50))
}

// ============================================
// HANDLERS
// ============================================

// handleWelcomeEmail - Always succeeds
func handleWelcomeEmail(ctx context.Context, task *strego.Task) error {
	slog.Info("📧 sending welcome email", "task_id", task.ID()[:8])
	time.Sleep(300 * time.Millisecond) // Simulate work
	slog.Info("✅ welcome email sent", "task_id", task.ID()[:8])
	return nil
}

// handleNewsletterEmail - Fails on first attempt, succeeds on retry
func handleNewsletterEmail(ctx context.Context, task *strego.Task) error {
	attempt := emailAttempts.Add(1)
	taskNum := (attempt-1)%5 + 1 // 1-5 for each task

	slog.Info("📰 sending newsletter",
		"task_id", task.ID()[:8],
		"retry", task.RetryCount())

	time.Sleep(300 * time.Millisecond)

	// First attempt for each task fails
	if task.RetryCount() == 0 {
		slog.Warn("❌ newsletter failed (SMTP timeout)",
			"task_id", task.ID()[:8],
			"task_num", taskNum)
		return fmt.Errorf("SMTP connection timeout")
	}

	slog.Info("✅ newsletter sent on retry",
		"task_id", task.ID()[:8],
		"retry", task.RetryCount())
	return nil
}

// handleOrderConfirm - Fails twice, succeeds on 3rd attempt
func handleOrderConfirm(ctx context.Context, task *strego.Task) error {
	slog.Info("🛒 processing order",
		"task_id", task.ID()[:8],
		"retry", task.RetryCount())

	time.Sleep(500 * time.Millisecond)

	// Fail first 2 attempts
	if task.RetryCount() < 2 {
		slog.Warn("❌ order processing failed (payment gateway error)",
			"task_id", task.ID()[:8],
			"retry", task.RetryCount())
		return fmt.Errorf("payment gateway timeout (attempt %d)", task.RetryCount()+1)
	}

	slog.Info("✅ order processed successfully",
		"task_id", task.ID()[:8],
		"retry", task.RetryCount())
	return nil
}

// handleNotification - Always fails (will go to DLQ)
func handleNotification(ctx context.Context, task *strego.Task) error {
	slog.Info("🔔 pushing notification",
		"task_id", task.ID()[:8],
		"retry", task.RetryCount())

	time.Sleep(200 * time.Millisecond)

	// Always fail - these will go to DLQ
	slog.Error("❌ notification failed (FCM unavailable)",
		"task_id", task.ID()[:8],
		"retry", task.RetryCount())
	return fmt.Errorf("FCM service unavailable")
}

// strings helper
var strings = struct {
	Repeat func(s string, count int) string
}{
	Repeat: func(s string, count int) string {
		result := ""
		for i := 0; i < count; i++ {
			result += s
		}
		return result
	},
}
