# strego

A modern, production-ready distributed task queue library for Go.

## Features

- **Redis Streams** - Native consumer groups, at-least-once delivery, crash recovery
- **Protobuf** - Type-safe, fast, language-agnostic serialization
- **Exactly-once processing** - Idempotency via Redis SET NX
- **Dead Letter Queue** - Failed tasks after max retries
- **Scheduled tasks** - Delayed and cron-based execution
- **Retry with backoff** - Exponential backoff strategy
- **Multi-worker** - Horizontal scaling with consumer groups
- **PostgreSQL (optional)** - Task history, search, filtering for UI
- **Built-in Web UI** - Dashboard with HTMX + Tailwind

## Installation

```bash
go get github.com/erennakbas/strego
```

## Quick Start

### Producer

```go
package main

import (
    "context"
    "github.com/redis/go-redis/v9"
    
    brokerRedis "github.com/erennakbas/strego/pkg/broker/redis"
    "github.com/erennakbas/strego/pkg/strego"
)

func main() {
    // Connect to Redis
    redisClient := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })

    // Create broker and client
    broker := brokerRedis.NewBroker(redisClient)
    client := strego.NewClient(broker)

    // Create and enqueue a task
    task := strego.NewTaskFromBytes("email:send",
        []byte(`{"to":"user@example.com","subject":"Welcome!"}`),
        strego.WithQueue("default"),
        strego.WithMaxRetry(3),
    )

    info, err := client.Enqueue(context.Background(), task)
    if err != nil {
        panic(err)
    }

    fmt.Printf("Task enqueued: %s\n", info.ID)
}
```

### Consumer

```go
package main

import (
    "context"
    "log/slog"
    
    "github.com/redis/go-redis/v9"
    
    brokerRedis "github.com/erennakbas/strego/pkg/broker/redis"
    "github.com/erennakbas/strego/pkg/strego"
)

func main() {
    // Connect to Redis
    redisClient := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })

    // Create broker and server
    broker := brokerRedis.NewBroker(redisClient)
    server := strego.NewServer(broker,
        strego.WithConcurrency(10),
        strego.WithQueues("default", "critical"),
    )

    // Register handlers
    server.HandleFunc("email:send", func(ctx context.Context, task *strego.Task) error {
        slog.Info("processing email", "payload", string(task.Payload()))
        // Your email sending logic here
        return nil
    })

    // Start processing (blocks until shutdown)
    if err := server.Start(); err != nil {
        panic(err)
    }
}
```

### With Web UI

```go
// Create UI server
uiServer, _ := ui.NewServer(ui.Config{
    Addr:   ":8080",
    Broker: broker,
    Store:  pgStore, // optional PostgreSQL store
})

go uiServer.Start()
// Visit http://localhost:8080
```

## Task Options

```go
task := strego.NewTaskFromBytes("task:type", payload,
    strego.WithQueue("critical"),          // Queue name
    strego.WithMaxRetry(5),                // Max retry attempts
    strego.WithTimeout(30*time.Second),    // Processing timeout
    strego.WithProcessIn(10*time.Minute),  // Delay execution
    strego.WithPriority(5),                // Priority (0-10)
    strego.WithUniqueKey("key", 1*time.Hour), // Deduplication
    strego.WithLabels(map[string]string{   // Custom labels
        "user_id": "123",
    }),
)
```

## Server Options

```go
server := strego.NewServer(broker,
    strego.WithConcurrency(10),                    // Worker count
    strego.WithQueues("default", "critical"),      // Queues to process
    strego.WithShutdownTimeout(30*time.Second),    // Graceful shutdown
    strego.WithProcessedTTL(24*time.Hour),         // Idempotency TTL
    strego.WithRetryConfig(1*time.Second, 10*time.Minute), // Backoff config
    strego.WithServerStore(pgStore),               // Optional PostgreSQL
    strego.WithServerLogger(logger),               // Custom logger
)
```

## PostgreSQL Integration (Optional)

PostgreSQL is optional but enables task history, search, and full UI features.

```go
import "github.com/erennakbas/strego/pkg/store/postgres"

store, _ := postgres.New(postgres.Config{
    DSN: "postgres://user:pass@localhost/strego?sslmode=disable",
})

client := strego.NewClient(broker, strego.WithStore(store))
server := strego.NewServer(broker, strego.WithServerStore(store))
uiServer, _ := ui.NewServer(ui.Config{
    Broker: broker,
    Store:  store,
})
```

## Redis Key Structure

```
strego:stream:{queue}         # Stream - main queue
strego:dlq:{queue}            # Stream - dead letter queue
strego:retry                  # Sorted Set - delayed retries
strego:scheduled              # Sorted Set - scheduled tasks
strego:processed:{task_id}    # String + TTL - idempotency
strego:unique:{key}           # String + TTL - deduplication
strego:stats:{queue}          # Hash - counters
```

## Comparison

| Feature | asynq | machinery | strego |
|---------|-------|-----------|--------|
| Redis Backend | Lists | Lists | **Streams** |
| Serialization | JSON | JSON | **Protobuf** |
| Consumer Groups | Custom | Custom | **Native** |
| Crash Recovery | Limited | Limited | **Built-in** |
| PostgreSQL | ❌ | ❌ | ✅ |
| Web UI | Separate | ❌ | **Built-in** |
| Exactly-once | UniqueTask | ❌ | ✅ |

## License

MIT
