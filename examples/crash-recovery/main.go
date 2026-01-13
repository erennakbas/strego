// Package main demonstrates worker crash recovery with ClaimStaleAfter.
//
// Scenario:
// 1. Run this example with: go run main.go
// 2. Tasks will start processing
// 3. Kill the process mid-task (Ctrl+C or kill -9)
// 4. Run another instance within 15 seconds
// 5. Watch the new worker claim and complete the orphaned tasks
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
)

func main() {
	// Setup logger
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	ctx := context.Background()
	workerID := fmt.Sprintf("worker-%d", os.Getpid())

	logger.WithField("worker_id", workerID).Info("🚀 starting worker")

	// Check if this is a recovery run (tasks already in queue)
	isRecoveryRun := false

	// Connect to Redis
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.WithError(err).Fatal("failed to connect to redis")
	}
	logger.Info("✅ connected to redis")

	// ============================================
	// Connect to PostgreSQL (for task history)
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
	logger.Info("✅ connected to postgresql")

	// Create broker with SHORT ClaimStaleAfter for demo
	// In production, use 5+ minutes
	// Note: Consumer ID is auto-generated as "worker-hostname-pid" if not specified
	b := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
		Group:           "crash-recovery-demo",
		BatchSize:       5,
		BlockDuration:   2 * time.Second,
		ClaimStaleAfter: 15 * time.Second, // Short for demo - normally 5+ minutes
	}))

	// Create client (with PostgreSQL store)
	client := strego.NewClient(b,
		strego.WithStore(pgStore),
		strego.WithClientLogger(logger),
	)

	// Create server (with PostgreSQL store)
	server := strego.NewServer(b,
		strego.WithConcurrency(2),
		strego.WithQueues("crash-test"),
		strego.WithServerStore(pgStore),
		strego.WithServerLogger(logger),
		strego.WithRetryConfig(5*time.Second, 30*time.Second),
		strego.WithProcessedTTL(24*time.Hour),
	)

	// Register handler that takes a LONG time (simulates work)
	server.HandleFunc("slow:task", func(ctx context.Context, task *strego.Task) error {
		// Recovery run: Fast processing (5 seconds)
		// First run: Slow processing (60 seconds) - will crash during this
		duration := 60
		progressMsg := "60"
		if isRecoveryRun {
			duration = 5
			progressMsg = "5"
			logger.WithFields(logrus.Fields{
				"task_id":   task.ID()[:8],
				"worker_id": workerID,
			}).Info("🔄 RECOVERY MODE: fast processing - will take 5 seconds")
		} else {
			logger.WithFields(logrus.Fields{
				"task_id":   task.ID()[:8],
				"worker_id": workerID,
			}).Info("🔄 starting slow task - will take 60 seconds")
		}

		// Simulate long-running work
		// If worker is killed during this, task becomes orphaned
		for i := 1; i <= duration; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
				if i%5 == 0 || isRecoveryRun {
					logger.WithFields(logrus.Fields{
						"task_id":  task.ID()[:8],
						"progress": fmt.Sprintf("%d/%s", i, progressMsg),
					}).Info("⏳ still working...")
				}
			}
		}

		logger.WithFields(logrus.Fields{
			"task_id":   task.ID()[:8],
			"worker_id": workerID,
		}).Info("✅ slow task completed!")

		return nil
	})

	// Enqueue tasks only if queue is empty (first run)
	go func() {
		time.Sleep(1 * time.Second)

		// Check if there are pending tasks
		info, _ := b.GetQueueInfo(ctx, "crash-test")
		if info != nil && (info.Pending > 0 || info.Active > 0) {
			isRecoveryRun = true
			logger.Info("")
			logger.Info("╔════════════════════════════════════════════════════╗")
			logger.Info("║                                                    ║")
			logger.Info("║            🔄 RECOVERY MODE ACTIVATED 🔄           ║")
			logger.Info("║                                                    ║")
			logger.WithFields(logrus.Fields{
				"pending": info.Pending,
				"active":  info.Active,
			}).Info("║  Found orphaned tasks - claiming in 15 seconds     ║")
			logger.Info("║                                                    ║")
			logger.Info("║  Tasks will process FAST (5 seconds each)         ║")
			logger.Info("║                                                    ║")
			logger.Info("╚════════════════════════════════════════════════════╝")
			logger.Info("")
			return
		}

		logger.Info("")
		logger.Info("╔════════════════════════════════════════════════════╗")
		logger.Info("║                                                    ║")
		logger.Info("║            💥 CRASH SIMULATION MODE 💥             ║")
		logger.Info("║                                                    ║")
		logger.Info("║  This is the FIRST run - will crash during work   ║")
		logger.Info("║                                                    ║")
		logger.Info("╚════════════════════════════════════════════════════╝")
		logger.Info("")

		logger.Info("📤 enqueueing 1 slow task")

		task := strego.NewTaskFromBytes("slow:task",
			[]byte(fmt.Sprintf(`{"task_num":%d}`, 1)),
			strego.WithQueue("crash-test"),
			strego.WithMaxRetry(3),
		)

		taskInfo, err := client.Enqueue(ctx, task)
		if err != nil {
			logger.WithError(err).Error("failed to enqueue")
		}

		logger.WithFields(logrus.Fields{
			"task_id": taskInfo.ID[:8],
		}).Info("📤 enqueued slow task")

		logger.Info("")
		logger.Info("💡 DEMO INSTRUCTIONS:")
		logger.Info("💡 1. Monitor UI: go run examples/monitor/main.go")
		logger.Info("💡 2. Open http://localhost:8080, select 'crash-recovery-demo' from dropdown")
		logger.Info("💡 3. Watch task start processing...")
		logger.Info("💡 4. This worker will AUTO-CRASH in 10 seconds!")
		logger.Info("💡 5. Run again to see FAST recovery mode")
		logger.Info("")

		// Auto-crash after 10 seconds (simulating unexpected failure)
		time.Sleep(10 * time.Second)
		logger.Error("💥💥💥 SIMULATED CRASH - Worker died unexpectedly! 💥💥💥")
		logger.Error("💥 Task is now ORPHANED - run again to recover!")
		os.Exit(1)
	}()

	// Handle shutdown (only for manual Ctrl+C)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		if isRecoveryRun {
			logger.Info("🛑 Graceful shutdown in recovery mode...")
			server.Shutdown()
			os.Exit(0)
		} else {
			logger.Warn("⚠️  received manual shutdown signal - orphaning in-progress tasks!")
			logger.Warn("⚠️  start another worker to claim them (within 15 seconds)")
			os.Exit(1) // Simulate crash - don't graceful shutdown
		}
	}()

	// Start processing
	logger.Info("🎯 starting task processing...")
	logger.WithField("claim_after", "15s").Info("⏰ orphaned tasks will be claimed after this duration")

	if err := server.Start(); err != nil {
		logger.WithError(err).Fatal("server error")
	}
}
