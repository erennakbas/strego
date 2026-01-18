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
	// IMMEDIATE TASKS - run right away
	// ============================================
	for i := 1; i <= 5; i++ {
		task := strego.NewTaskFromBytes("reminder:send",
			[]byte(`{"message": "IMMEDIATE task", "index": `+string(rune('0'+i))+`}`),
			strego.WithQueue("reminders"),
		)
		client.Enqueue(ctx, task)
	}
	logger.WithField("count", 5).Warn("[IMMEDIATE] 5 tasks enqueued - will run NOW")

	// ============================================
	// WAVE 1: 10 seconds - Email batch
	// ============================================
	for i := 1; i <= 10; i++ {
		task := strego.NewTaskFromBytes("email:welcome",
			[]byte(`{"email": "user`+string(rune('0'+i))+`@example.com", "wave": 1}`),
			strego.WithQueue("default"),
			strego.WithProcessIn(10*time.Second),
		)
		client.Enqueue(ctx, task)
	}
	logger.WithFields(logrus.Fields{
		"count":   10,
		"runs_at": now.Add(10 * time.Second).Format("15:04:05"),
	}).Warn("[+10 sec] 10 welcome emails scheduled")

	// ============================================
	// WAVE 2: 20 seconds - Reports batch
	// ============================================
	for i := 1; i <= 5; i++ {
		task := strego.NewTaskFromBytes("report:daily",
			[]byte(`{"report_type": "daily", "department": `+string(rune('0'+i))+`}`),
			strego.WithQueue("reports"),
			strego.WithProcessIn(20*time.Second),
		)
		client.Enqueue(ctx, task)
	}
	logger.WithFields(logrus.Fields{
		"count":   5,
		"runs_at": now.Add(20 * time.Second).Format("15:04:05"),
	}).Warn("[+20 sec] 5 daily reports scheduled")

	// ============================================
	// WAVE 3: 30 seconds - Mixed batch
	// ============================================
	for i := 1; i <= 8; i++ {
		queue := "default"
		taskType := "cleanup:old-data"
		if i%2 == 0 {
			queue = "reminders"
			taskType = "reminder:send"
		}
		task := strego.NewTaskFromBytes(taskType,
			[]byte(`{"batch": 3, "index": `+string(rune('0'+i))+`}`),
			strego.WithQueue(queue),
			strego.WithProcessIn(30*time.Second),
		)
		client.Enqueue(ctx, task)
	}
	logger.WithFields(logrus.Fields{
		"count":   8,
		"runs_at": now.Add(30 * time.Second).Format("15:04:05"),
	}).Warn("[+30 sec] 8 mixed tasks scheduled (cleanup + reminders)")

	// ============================================
	// WAVE 4: 45 seconds - Critical tasks
	// ============================================
	for i := 1; i <= 3; i++ {
		task := strego.NewTaskFromBytes("email:welcome",
			[]byte(`{"email": "vip`+string(rune('0'+i))+`@example.com", "priority": "high"}`),
			strego.WithQueue("default"),
			strego.WithProcessIn(45*time.Second),
			strego.WithMaxRetry(5),
		)
		client.Enqueue(ctx, task)
	}
	logger.WithFields(logrus.Fields{
		"count":   3,
		"runs_at": now.Add(45 * time.Second).Format("15:04:05"),
	}).Warn("[+45 sec] 3 VIP emails scheduled")

	// ============================================
	// WAVE 5: 60 seconds - Final batch
	// ============================================
	for i := 1; i <= 5; i++ {
		task := strego.NewTaskFromBytes("report:daily",
			[]byte(`{"report_type": "final", "wave": 5}`),
			strego.WithQueue("reports"),
			strego.WithProcessIn(60*time.Second),
		)
		client.Enqueue(ctx, task)
	}
	logger.WithFields(logrus.Fields{
		"count":   5,
		"runs_at": now.Add(60 * time.Second).Format("15:04:05"),
	}).Warn("[+60 sec] 5 final reports scheduled")

	// ============================================
	// SUMMARY
	// ============================================
	totalScheduled := 10 + 5 + 8 + 3 + 5
	logger.Info("===========================================")
	logger.Infof("TOTAL: %d scheduled + 5 immediate = %d tasks", totalScheduled, totalScheduled+5)
	logger.Info("===========================================")
	logger.Info("Timeline:")
	logger.Infof("  NOW        -> 5 immediate reminders")
	logger.Infof("  +10 sec    -> 10 welcome emails")
	logger.Infof("  +20 sec    -> 5 daily reports")
	logger.Infof("  +30 sec    -> 8 mixed tasks")
	logger.Infof("  +45 sec    -> 3 VIP emails")
	logger.Infof("  +60 sec    -> 5 final reports")
	logger.Info("===========================================")
	logger.Info("Watch the logs - tasks execute at their scheduled times!")
	logger.Info("Press Ctrl+C to exit")
}
