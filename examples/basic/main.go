// Package main demonstrates basic usage of strego.
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
	"github.com/erennakbas/strego/ui"
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

	// Create broker
	// Note: Consumer ID is auto-generated as "worker-hostname-pid" if not specified
	b := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
		Group:         "strego-example",
		BatchSize:     10,
		BlockDuration: 5 * time.Second,
	}))

	// Create client for enqueuing tasks
	client := strego.NewClient(b, strego.WithClientLogger(logger))

	// Create server for processing tasks
	server := strego.NewServer(b,
		strego.WithConcurrency(5),
		strego.WithQueues("default", "critical", "low"),
		strego.WithServerLogger(logger),
	)

	// Register handlers
	server.HandleFunc("email:send", handleSendEmail)
	server.HandleFunc("report:generate", handleGenerateReport)
	server.HandleFunc("notification:push", handlePushNotification)

	// Start UI server in background
	uiServer, err := ui.NewServer(ui.Config{
		Addr:   ":8080",
		Broker: b,
		Logger: logger,
	})
	if err != nil {
		logger.WithError(err).Fatal("failed to create UI server")
	}

	go func() {
		logger.WithField("addr", "http://localhost:8080").Info("starting UI server")
		if err := uiServer.Start(); err != nil {
			logger.WithError(err).Error("UI server error")
		}
	}()

	// Enqueue some example tasks
	go func() {
		time.Sleep(1 * time.Second)
		enqueueExampleTasks(ctx, client, logger)
	}()

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		logger.Info("shutting down...")
		server.Shutdown()
		uiServer.Shutdown(context.Background())
	}()

	// Start processing
	logger.Info("starting task processing...")
	if err := server.Start(); err != nil {
		logger.WithError(err).Fatal("server error")
	}
}

func handleSendEmail(ctx context.Context, task *strego.Task) error {
	logrus.WithFields(logrus.Fields{
		"task_id": task.ID(),
		"payload": string(task.Payload()),
	}).Info("sending email")

	// Simulate work
	time.Sleep(100 * time.Millisecond)

	// Simulate occasional failure
	if time.Now().UnixNano()%5 == 0 {
		return fmt.Errorf("failed to send email: SMTP connection refused")
	}

	logrus.WithField("task_id", task.ID()).Info("email sent successfully")
	return nil
}

func handleGenerateReport(ctx context.Context, task *strego.Task) error {
	logrus.WithFields(logrus.Fields{
		"task_id": task.ID(),
		"payload": string(task.Payload()),
	}).Info("generating report")

	// Simulate longer work
	time.Sleep(500 * time.Millisecond)

	logrus.WithField("task_id", task.ID()).Info("report generated")
	return nil
}

func handlePushNotification(ctx context.Context, task *strego.Task) error {
	logrus.WithFields(logrus.Fields{
		"task_id": task.ID(),
		"payload": string(task.Payload()),
	}).Info("pushing notification")

	// Simulate work
	time.Sleep(50 * time.Millisecond)

	logrus.WithField("task_id", task.ID()).Info("notification pushed")
	return nil
}

func enqueueExampleTasks(ctx context.Context, client *strego.Client, logger *logrus.Logger) {
	logger.Info("enqueuing example tasks...")

	// Email tasks
	for i := 0; i < 10; i++ {
		task := strego.NewTaskFromBytes("email:send",
			[]byte(fmt.Sprintf(`{"to":"user%d@example.com","subject":"Welcome!"}`, i)),
			strego.WithQueue("default"),
			strego.WithMaxRetry(3),
		)

		info, err := client.Enqueue(ctx, task)
		if err != nil {
			logger.WithError(err).Error("failed to enqueue")
			continue
		}
		logger.WithFields(logrus.Fields{
			"task_id": info.ID,
			"queue":   info.Queue,
		}).Debug("enqueued")
	}

	// Critical tasks
	for i := 0; i < 5; i++ {
		task := strego.NewTaskFromBytes("notification:push",
			[]byte(fmt.Sprintf(`{"user_id":%d,"message":"Alert!"}`, i)),
			strego.WithQueue("critical"),
			strego.WithMaxRetry(5),
		)

		client.Enqueue(ctx, task)
	}

	// Scheduled tasks
	for i := 0; i < 3; i++ {
		task := strego.NewTaskFromBytes("report:generate",
			[]byte(fmt.Sprintf(`{"report_id":%d}`, i)),
			strego.WithQueue("low"),
			strego.WithProcessIn(time.Duration(i+1)*10*time.Second),
		)

		info, err := client.Enqueue(ctx, task)
		if err != nil {
			logger.WithError(err).Error("failed to schedule")
			continue
		}
		logger.WithFields(logrus.Fields{
			"task_id":    info.ID,
			"process_at": info.ProcessAt,
		}).Debug("scheduled")
	}

	logger.Info("example tasks enqueued")
}
