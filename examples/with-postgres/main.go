// Package main demonstrates strego with PostgreSQL for task history and full UI features.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"

	"github.com/erennakbas/strego"
	"github.com/erennakbas/strego/broker"
	brokerRedis "github.com/erennakbas/strego/broker/redis"
	"github.com/erennakbas/strego/store/postgres"
	"github.com/erennakbas/strego/ui"
)

func main() {
	// Setup logger
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	ctx := context.Background()

	// ============================================
	// Connect to Redis (required - message broker)
	// ============================================
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.WithError(err).Fatal("failed to connect to redis")
	}
	logger.WithField("addr", "localhost:6379").Info("connected to redis")

	// ============================================
	// Connect to PostgreSQL (optional - for UI/history)
	// ============================================
	pgStore, err := postgres.New(postgres.Config{
		DSN:             "postgres://erenakbas:admin123@localhost:5432/picus_ng_test?sslmode=disable",
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
	})
	if err != nil {
		logger.WithError(err).Fatal("failed to connect to postgresql")
	}
	defer pgStore.Close()
	logger.WithField("addr", "localhost:5432").Info("connected to postgresql")

	// ============================================
	// Create Broker (Redis Streams)
	// ============================================
	// Note: Consumer ID is auto-generated as "worker-hostname-pid" if not specified
	b := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
		Group:           "strego-postgres-example",
		BatchSize:       10,
		BlockDuration:   5 * time.Second,
		ClaimStaleAfter: 5 * time.Minute,
	}))

	// ============================================
	// Create Client (Producer) with PostgreSQL
	// ============================================
	client := strego.NewClient(b,
		strego.WithStore(pgStore),
		strego.WithClientLogger(logger),
	)

	// ============================================
	// Create Server (Consumer) with PostgreSQL
	// ============================================
	server := strego.NewServer(b,
		strego.WithConcurrency(10),
		strego.WithQueues("default", "critical", "low"),
		strego.WithServerStore(pgStore),
		strego.WithServerLogger(logger),
		strego.WithShutdownTimeout(30*time.Second),
		strego.WithProcessedTTL(24*time.Hour),
		strego.WithRetryConfig(1*time.Second, 5*time.Minute),
	)

	// ============================================
	// Register Task Handlers
	// ============================================
	server.HandleFunc("email:send", func(ctx context.Context, task *strego.Task) error {
		logrus.WithField("task_id", task.ID()[:8]).Info("📧 sending email")
		time.Sleep(100 * time.Millisecond)
		logrus.WithField("task_id", task.ID()[:8]).Info("✅ email sent")
		return nil
	})

	server.HandleFunc("order:process", func(ctx context.Context, task *strego.Task) error {
		logrus.WithField("task_id", task.ID()[:8]).Info("🛒 processing order")
		time.Sleep(200 * time.Millisecond)
		logrus.WithField("task_id", task.ID()[:8]).Info("✅ order processed")
		return nil
	})

	server.HandleFunc("notification:push", func(ctx context.Context, task *strego.Task) error {
		logrus.WithField("task_id", task.ID()[:8]).Info("🔔 pushing notification")
		time.Sleep(50 * time.Millisecond)
		logrus.WithField("task_id", task.ID()[:8]).Info("✅ notification pushed")
		return nil
	})

	// ============================================
	// Start UI Server with PostgreSQL
	// ============================================
	uiServer, err := ui.NewServer(ui.Config{
		Addr:   ":8080",
		Broker: b,
		Store:  pgStore,
		Logger: logger,
	})
	if err != nil {
		logger.WithError(err).Fatal("failed to create UI server")
	}

	go func() {
		logger.WithField("url", "http://localhost:8080").Info("starting UI server")
		if err := uiServer.Start(); err != nil {
			logger.WithError(err).Error("UI server error")
		}
	}()

	// ============================================
	// Enqueue Example Tasks
	// ============================================
	go func() {
		time.Sleep(2 * time.Second)
		enqueueExampleTasks(ctx, client, logger)
	}()

	// ============================================
	// Handle Graceful Shutdown
	// ============================================
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		logger.Info("shutting down gracefully...")
		server.Shutdown()
		uiServer.Shutdown(context.Background())
	}()

	// ============================================
	// Start Task Processing (blocking)
	// ============================================
	logger.Info("starting task processing...")
	if err := server.Start(); err != nil {
		logger.WithError(err).Fatal("server error")
	}

	logger.Info("shutdown complete")
}

func enqueueExampleTasks(ctx context.Context, client *strego.Client, logger *logrus.Logger) {
	logger.Info("enqueuing example tasks...")

	// Emails
	for i := 0; i < 10; i++ {
		task := strego.NewTaskFromBytes("email:send",
			[]byte(fmt.Sprintf(`{"to":"user%d@example.com","subject":"Welcome!"}`, i)),
			strego.WithQueue("default"),
			strego.WithMaxRetry(3),
		)
		client.Enqueue(ctx, task)
		time.Sleep(100 * time.Millisecond)
	}

	// Orders
	for i := 0; i < 5; i++ {
		task := strego.NewTaskFromBytes("order:process",
			[]byte(fmt.Sprintf(`{"order_id":"ORD-%d","amount":%.2f}`, 1000+i, float64(i)*29.99)),
			strego.WithQueue("critical"),
			strego.WithMaxRetry(5),
		)
		client.Enqueue(ctx, task)
		time.Sleep(100 * time.Millisecond)
	}

	// Notifications
	for i := 0; i < 5; i++ {
		task := strego.NewTaskFromBytes("notification:push",
			[]byte(fmt.Sprintf(`{"user_id":%d,"message":"Alert!"}`, i)),
			strego.WithQueue("low"),
			strego.WithMaxRetry(2),
		)
		client.Enqueue(ctx, task)
		time.Sleep(100 * time.Millisecond)
	}

	logger.WithField("total", 20).Info("example tasks enqueued")
}
