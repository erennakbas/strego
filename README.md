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
- **Multiple Queues** - Efficient queue isolation with minimal overhead

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
    "fmt"
    "time"
    
    "github.com/redis/go-redis/v9"
    "github.com/erennakbas/strego/pkg/broker"
    brokerRedis "github.com/erennakbas/strego/pkg/broker/redis"
    "github.com/erennakbas/strego/pkg/strego"
)

func main() {
    // Connect to Redis
    redisClient := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })

    // Create broker with custom consumer config
    broker := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
        Group:         "strego-example",
        BatchSize:     10,
        BlockDuration: 5 * time.Second,
    }))
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
    "time"
    
    "github.com/redis/go-redis/v9"
    "github.com/erennakbas/strego/pkg/broker"
    brokerRedis "github.com/erennakbas/strego/pkg/broker/redis"
    "github.com/erennakbas/strego/pkg/strego"
)

func main() {
    // Connect to Redis
    redisClient := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })

    // Create broker with consumer config
    broker := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
        Group:         "strego-example",
        BatchSize:     10,
        BlockDuration: 5 * time.Second,
    }))
    
    // Create server
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

## Broker Configuration

The broker can be configured with consumer settings that control how tasks are consumed from Redis Streams:

```go
broker := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
    Group:           "strego-workers",      // Consumer group name
    Consumer:        "",                    // Auto-generated if empty
    BatchSize:       10,                    // Tasks to fetch per batch
    BlockDuration:   5 * time.Second,       // Wait time for new messages
    ClaimStaleAfter: 5 * time.Minute,       // Claim stale pending messages
}))
```

**Configuration Options:**
- **Group**: Consumer group name. Multiple workers in the same group share the workload.
- **BatchSize**: Number of tasks fetched in a single Redis call. Higher values improve throughput but increase memory usage.
- **BlockDuration**: How long to wait for new messages. During this time, the consumer blocks waiting for new tasks. Other consumers can still read in parallel.
- **ClaimStaleAfter**: Duration after which pending messages from crashed workers are automatically claimed by other workers.

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

## Queue Management

### Multiple Queues

Strego efficiently supports multiple queues with minimal overhead. Each queue is isolated and can have different priorities, rate limits, and monitoring.

**Queue Creation:**
- Queues are created lazily on first use (no upfront cost)
- Each queue uses ~1-2KB memory when empty
- All queues are consumed in a single efficient Redis call

**Example with 20+ Queues:**
```go
// Efficient - all queues processed in one XReadGroup call
server := strego.NewServer(broker,
    strego.WithQueues(
        "email", "notification", "report", "analytics",
        "payment", "order", "inventory", "shipping",
        // ... 20+ queues
    ),
)
```

**Benefits:**
- Task type isolation
- Independent monitoring and stats
- Separate priority and rate limiting
- One queue failure doesn't affect others

## Redis Key Structure

```
strego:stream:{queue}         # Stream - main queue
strego:dlq:{queue}            # Stream - dead letter queue
strego:retry                  # Sorted Set - delayed retries
strego:scheduled              # Sorted Set - scheduled tasks
strego:processed:{task_id}    # String + TTL - idempotency
strego:unique:{key}           # String + TTL - deduplication
strego:stats:{queue}          # Hash - counters
strego:queues                 # Set - all queue names
```

## Performance & Scaling

- **Horizontal Scaling**: Add more workers to the same consumer group for automatic load balancing
- **Queue Isolation**: Multiple queues (200+) with minimal overhead (~400-600KB for empty queues)
- **Batch Processing**: Configurable batch size for optimal throughput
- **Memory Efficient**: Lazy queue creation, automatic cleanup of processed tasks
- **Low Latency**: Blocking reads with configurable timeout for immediate task processing

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
| Multiple Queues | Limited | Limited | **Efficient** |

## License

MIT
