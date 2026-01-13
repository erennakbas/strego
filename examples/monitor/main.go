// Package main provides a standalone monitoring UI for strego.
//
// This is a read-only dashboard that doesn't process any tasks.
// Use it to monitor workers running in other processes.
//
// Usage:
// 1. Start your workers (crash-recovery, production, etc.)
// 2. Run this monitor: go run main.go
// 3. Open http://localhost:8080 to watch everything
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"

	"github.com/erennakbas/strego/broker"
	brokerRedis "github.com/erennakbas/strego/broker/redis"
	"github.com/erennakbas/strego/store/postgres"
	"github.com/erennakbas/strego/ui"
)

func main() {
	// Setup logger
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	ctx := context.Background()

	logger.Info("🔍 starting strego monitor (UI only)")

	// Get consumer group from env variable (optional - UI has dropdown selector now)
	consumerGroup := os.Getenv("CONSUMER_GROUP")
	if consumerGroup == "" {
		consumerGroup = "strego-workers" // Default fallback
	}
	logger.WithField("consumer_group", consumerGroup).Info("📋 default consumer group (can be changed in UI)")

	// Connect to Redis
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.WithError(err).Fatal("❌ failed to connect to redis")
	}
	logger.Info("✅ connected to redis")

	// Create broker (only for reading queue info)
	// Consumer group must match the workers you want to monitor!
	b := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
		Group:           consumerGroup,
		BatchSize:       10,
		BlockDuration:   5 * time.Second,
		ClaimStaleAfter: 5 * time.Minute,
	}))

	// ============================================
	// Connect to PostgreSQL for task history
	// ============================================
	pgStore, err := postgres.New(postgres.Config{
		DSN:             "postgres://erenakbas:admin123@localhost:5432/picus_ng_test?sslmode=disable",
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
	})
	if err != nil {
		logger.WithError(err).Warn("⚠️  failed to connect to postgresql (task history disabled)")
		pgStore = nil
	} else {
		logger.Info("✅ connected to postgresql")
	}
	if pgStore != nil {
		defer pgStore.Close()
	}

	// Create UI Server (read-only monitoring)
	uiServer, err := ui.NewServer(ui.Config{
		Addr:   ":8080",
		Broker: b,
		Store:  pgStore, // Task history enabled if PostgreSQL connected
		Logger: logger,
	})
	if err != nil {
		logger.WithError(err).Fatal("❌ failed to create UI server")
	}

	logger.Info("")
	logger.Info("╔════════════════════════════════════════════════════╗")
	logger.Info("║                                                    ║")
	logger.Info("║        🌐  STREGO MONITOR UI IS READY  🌐         ║")
	logger.Info("║                                                    ║")
	logger.Info("║          http://localhost:8080                     ║")
	logger.Info("║                                                    ║")
	logger.Info("║  This is a read-only monitoring dashboard.        ║")
	logger.Info("║  Start workers in other terminals to see them.    ║")
	logger.Info("║                                                    ║")
	logger.WithField("group", consumerGroup).Info("║  📋 Consumer Group: ", consumerGroup)
	if pgStore != nil {
		logger.Info("║  ✅ PostgreSQL: Task history enabled               ║")
	} else {
		logger.Info("║  ⚠️  PostgreSQL: Task history disabled             ║")
	}
	logger.Info("║                                                    ║")
	logger.Info("╚════════════════════════════════════════════════════╝")
	logger.Info("")

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		logger.Info("🛑 shutting down monitor...")
		uiServer.Shutdown(context.Background())
		os.Exit(0)
	}()

	// Start UI server (no worker, no task processing)
	if err := uiServer.Start(); err != nil {
		logger.WithError(err).Fatal("UI server error")
	}
}

// Note: This program does NOT process any tasks.
// It only provides a web interface to monitor:
// - Queue statistics (pending, active, processed, dead)
// - Task history (if PostgreSQL store is configured)
// - Dead letter queues
//
// To see data in the UI:
// 1. Run workers in other terminals:
//    cd examples/crash-recovery && go run main.go
//    cd examples/production && go run main.go
// 2. Keep this monitor running
// 3. Watch the dashboard update as workers process tasks
