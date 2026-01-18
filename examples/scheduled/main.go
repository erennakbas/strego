// Package main demonstrates scheduled/delayed task processing with strego.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"

	"github.com/erennakbas/strego"
	"github.com/erennakbas/strego/broker"
	brokerRedis "github.com/erennakbas/strego/broker/redis"
)

func main() {
	// Setup logger
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	// Connect to Redis
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	ctx := context.Background()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.WithError(err).Fatal("failed to connect to redis")
	}
	logger.Info("connected to redis")

	// Create broker with scheduler configuration
	b := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
		Group:              "strego-scheduled-example",
		BatchSize:          10,
		BlockDuration:      5 * time.Second,
		ClaimStaleAfter:    5 * time.Minute,
		ClaimCheckInterval: 30 * time.Second,
	}))

	// Create client for enqueuing tasks
	client := strego.NewClient(b, strego.WithClientLogger(logger))

	// Create server with custom scheduler interval
	// Default is 5 seconds, you can make it faster for more precision
	server := strego.NewServer(b,
		strego.WithConcurrency(5),
		strego.WithQueues("default", "reports", "reminders"),
		strego.WithServerLogger(logger),
		strego.WithSchedulerInterval(1*time.Second), // Check every 1 second for more precision
	)

	// Register handlers
	server.HandleFunc("reminder:send", handleSendReminder)
	server.HandleFunc("report:daily", handleDailyReport)
	server.HandleFunc("email:welcome", handleWelcomeEmail)
	server.HandleFunc("cleanup:old-data", handleCleanupOldData)

	// Enqueue scheduled tasks
	go func() {
		time.Sleep(1 * time.Second)
		enqueueScheduledTasks(ctx, client, logger)
	}()

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		logger.Info("shutting down...")
		server.Shutdown()
	}()

	// Start processing
	logger.Info("starting task processing...")
	logger.Info("scheduled tasks will execute at their designated times")
	if err := server.Start(); err != nil {
		logger.WithError(err).Fatal("server error")
	}
}

func handleSendReminder(ctx context.Context, task *strego.Task) error {
	logrus.WithFields(logrus.Fields{
		"task_id": task.ID()[:8],
		"payload": string(task.Payload()),
	}).Info("sending reminder")

	time.Sleep(100 * time.Millisecond)

	logrus.WithField("task_id", task.ID()[:8]).Info("reminder sent")
	return nil
}

func handleDailyReport(ctx context.Context, task *strego.Task) error {
	logrus.WithFields(logrus.Fields{
		"task_id": task.ID()[:8],
		"payload": string(task.Payload()),
	}).Info("generating daily report")

	time.Sleep(500 * time.Millisecond)

	logrus.WithField("task_id", task.ID()[:8]).Info("daily report generated")
	return nil
}

func handleWelcomeEmail(ctx context.Context, task *strego.Task) error {
	logrus.WithFields(logrus.Fields{
		"task_id": task.ID()[:8],
		"payload": string(task.Payload()),
	}).Info("sending welcome email")

	time.Sleep(200 * time.Millisecond)

	logrus.WithField("task_id", task.ID()[:8]).Info("welcome email sent")
	return nil
}

func handleCleanupOldData(ctx context.Context, task *strego.Task) error {
	logrus.WithFields(logrus.Fields{
		"task_id": task.ID()[:8],
		"payload": string(task.Payload()),
	}).Info("cleaning up old data")

	time.Sleep(300 * time.Millisecond)

	logrus.WithField("task_id", task.ID()[:8]).Info("old data cleaned up")
	return nil
}

func enqueueScheduledTasks(ctx context.Context, client *strego.Client, logger *logrus.Logger) {
	now := time.Now()

	logger.Info("===========================================")
	logger.Info("SCHEDULING TASKS - Watch the execution order!")
	logger.Info("===========================================")
	logger.WithField("current_time", now.Format("15:04:05")).Info("current time")

	// ============================================
	// IMMEDIATE TASK - runs right away
	// ============================================
	taskImmediate := strego.NewTaskFromBytes("reminder:send",
		[]byte(`{"message": "IMMEDIATE - This runs right away!"}`),
		strego.WithQueue("reminders"),
	)
	client.Enqueue(ctx, taskImmediate)
	logger.WithField("runs_at", "NOW").Warn("[1] IMMEDIATE task enqueued")

	// ============================================
	// 15 SECONDS LATER
	// ============================================
	task15 := strego.NewTaskFromBytes("email:welcome",
		[]byte(`{"message": "15 SECONDS - Delayed welcome email"}`),
		strego.WithQueue("default"),
		strego.WithProcessIn(15*time.Second),
	)
	info15, _ := client.Enqueue(ctx, task15)
	logger.WithField("runs_at", info15.ProcessAt.Format("15:04:05")).Warn("[2] 15 SECONDS task scheduled")

	// ============================================
	// 30 SECONDS LATER
	// ============================================
	task30 := strego.NewTaskFromBytes("report:daily",
		[]byte(`{"message": "30 SECONDS - Daily report generation"}`),
		strego.WithQueue("reports"),
		strego.WithProcessIn(30*time.Second),
	)
	info30, _ := client.Enqueue(ctx, task30)
	logger.WithField("runs_at", info30.ProcessAt.Format("15:04:05")).Warn("[3] 30 SECONDS task scheduled")

	// ============================================
	// 45 SECONDS LATER
	// ============================================
	task45 := strego.NewTaskFromBytes("cleanup:old-data",
		[]byte(`{"message": "45 SECONDS - Cleanup old data"}`),
		strego.WithQueue("default"),
		strego.WithProcessIn(45*time.Second),
	)
	info45, _ := client.Enqueue(ctx, task45)
	logger.WithField("runs_at", info45.ProcessAt.Format("15:04:05")).Warn("[4] 45 SECONDS task scheduled")

	// ============================================
	// 60 SECONDS LATER (1 minute)
	// ============================================
	task60 := strego.NewTaskFromBytes("reminder:send",
		[]byte(`{"message": "60 SECONDS - Final reminder after 1 minute"}`),
		strego.WithQueue("reminders"),
		strego.WithProcessIn(60*time.Second),
	)
	info60, _ := client.Enqueue(ctx, task60)
	logger.WithField("runs_at", info60.ProcessAt.Format("15:04:05")).Warn("[5] 60 SECONDS (1 min) task scheduled")

	logger.Info("===========================================")
	logger.Info("All tasks scheduled! Timeline:")
	logger.Infof("  NOW        -> [1] IMMEDIATE")
	logger.Infof("  +15 sec    -> [2] Welcome email")
	logger.Infof("  +30 sec    -> [3] Daily report")
	logger.Infof("  +45 sec    -> [4] Cleanup")
	logger.Infof("  +60 sec    -> [5] Final reminder")
	logger.Info("===========================================")
	logger.Info("Watch the logs - tasks will execute at their scheduled times!")
	logger.Info("Press Ctrl+C to exit")
}
