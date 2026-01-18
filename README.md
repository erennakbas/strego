# strego

[![Go Reference](https://pkg.go.dev/badge/github.com/erennakbas/strego.svg)](https://pkg.go.dev/github.com/erennakbas/strego)
[![Go Report Card](https://goreportcard.com/badge/github.com/erennakbas/strego)](https://goreportcard.com/report/github.com/erennakbas/strego)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Release](https://img.shields.io/github/v/release/erennakbas/strego)](https://github.com/erennakbas/strego/releases/tag/v0.3.5)

A modern, production-ready distributed task queue library for Go.

## Features

- **Redis Streams** - Native consumer groups, at-least-once delivery, crash recovery
- **JSON** - Human-readable, debuggable, no code generation needed
- **Exactly-once processing** - Idempotency via Redis SET NX
- **Dead Letter Queue** - Failed tasks after max retries
- **Scheduled tasks** - Delayed and cron-based execution
- **Retry with backoff** - Exponential backoff strategy
- **Crash Recovery** - Automatic claiming of orphaned tasks from dead workers
- **Multi-worker** - Horizontal scaling with consumer groups
- **PostgreSQL (optional)** - Task history, search, filtering for UI
- **Built-in Web UI** - Real-time dashboard with consumer group monitoring
  - Redis Streams: Live queue stats, active consumers
  - PostgreSQL: Task history and search (optional)
- **Logrus logging** - Structured logging out of the box

## Documentation

- [Architecture Documentation](docs/architecture.md) - Detailed architecture diagrams and design decisions

## Installation

```bash
go get github.com/erennakbas/strego@v0.3.5
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
    "github.com/sirupsen/logrus"
    
    "github.com/erennakbas/strego"
    "github.com/erennakbas/strego/broker"
    brokerRedis "github.com/erennakbas/strego/broker/redis"
)

func main() {
    // Setup logger
    logger := logrus.New()
    
    // Connect to Redis
    redisClient := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })

    // Create broker
    b := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
        Group:         "my-app",
        BatchSize:     10,
        BlockDuration: 5 * time.Second,
    }))
    
    client := strego.NewClient(b, strego.WithClientLogger(logger))

    // Create and enqueue a task
    task := strego.NewTaskFromBytes("email:send",
        []byte(`{"to":"user@example.com","subject":"Welcome!"}`),
        strego.WithQueue("default"),
        strego.WithMaxRetry(3),
    )

    info, err := client.Enqueue(context.Background(), task)
    if err != nil {
        logger.WithError(err).Fatal("failed to enqueue")
    }

    logger.WithField("task_id", info.ID).Info("task enqueued")
}
```

### Consumer

```go
package main

import (
    "context"
    "time"
    
    "github.com/redis/go-redis/v9"
    "github.com/sirupsen/logrus"
    
    "github.com/erennakbas/strego"
    "github.com/erennakbas/strego/broker"
    brokerRedis "github.com/erennakbas/strego/broker/redis"
)

func main() {
    logger := logrus.New()
    
    // Connect to Redis
    redisClient := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })

    // Create broker
    b := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
        Group:           "my-app",
        BatchSize:       10,
        BlockDuration:   5 * time.Second,
        ClaimStaleAfter: 5 * time.Minute, // Claim orphaned tasks from dead workers
    }))
    
    // Create server
    server := strego.NewServer(b,
        strego.WithConcurrency(10),
        strego.WithQueues("default", "critical"),
        strego.WithServerLogger(logger),
    )

    // Register handlers
    server.HandleFunc("email:send", func(ctx context.Context, task *strego.Task) error {
        logrus.WithField("payload", string(task.Payload())).Info("processing email")
        // Your email sending logic here
        return nil
    })

    // Start processing (blocks until shutdown)
    if err := server.Start(); err != nil {
        logger.WithError(err).Fatal("server error")
    }
}
```

### With Web UI

```go
import "github.com/erennakbas/strego/ui"

// Create UI server
uiServer, _ := ui.NewServer(ui.Config{
    Addr:   ":8080",
    Broker: b,
    Store:  pgStore, // optional PostgreSQL store
    Logger: logger,
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

```go
b := brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
    Group:           "my-workers",         // Consumer group name
    Consumer:        "",                   // Auto-generated if empty
    BatchSize:       10,                   // Tasks to fetch per batch
    BlockDuration:   5 * time.Second,      // Wait time for new messages
    ClaimStaleAfter: 5 * time.Minute,      // Claim orphaned tasks from crashed workers
}))
```

**Configuration Options:**
- **Group**: Consumer group name. Multiple workers in the same group share the workload.
- **BatchSize**: Number of tasks fetched in a single Redis call.
- **BlockDuration**: How long to wait for new messages.
- **ClaimStaleAfter**: Duration after which pending messages from crashed workers are automatically claimed.

## Server Options

```go
server := strego.NewServer(b,
    strego.WithConcurrency(10),                    // Worker count
    strego.WithQueues("default", "critical"),      // Queues to process
    strego.WithShutdownTimeout(30*time.Second),    // Graceful shutdown
    strego.WithProcessedTTL(24*time.Hour),         // Idempotency TTL
    strego.WithRetryConfig(1*time.Second, 10*time.Minute), // Backoff config
    strego.WithServerStore(pgStore),               // Optional PostgreSQL
    strego.WithConsumerCleanup(10*time.Minute, 60*time.Minute), // Auto-cleanup dead consumers
    strego.WithServerLogger(logger),               // Logger
)
```

## PostgreSQL Integration (Optional)

PostgreSQL is optional but enables task history, search, and full UI features.

```go
import "github.com/erennakbas/strego/store/postgres"

store, _ := postgres.New(postgres.Config{
    DSN: "postgres://user:pass@localhost/strego?sslmode=disable",
})

client := strego.NewClient(b, strego.WithStore(store))
server := strego.NewServer(b, strego.WithServerStore(store))
uiServer, _ := ui.NewServer(ui.Config{
    Broker: b,
    Store:  store,
})
```

## Examples

| Example | Description |
|---------|-------------|
| [basic](examples/basic) | Simple producer/consumer setup |
| [with-postgres](examples/with-postgres) | PostgreSQL integration for task history |
| [production](examples/production) | Multi-phase retry scenarios |
| [crash-recovery](examples/crash-recovery) | Worker crash and task claiming demo |

## Redis Deployment

| Mode | Supported | Notes |
|------|-----------|-------|
| Single Instance | ✅ | Default, works out of the box |
| Sentinel (HA) | ✅ | Use `redis.NewFailoverClient` |
| Cluster | ❌ | Not yet supported |

**Sentinel Example:**

```go
redisClient := redis.NewFailoverClient(&redis.FailoverOptions{
    MasterName:    "mymaster",
    SentinelAddrs: []string{"sentinel1:26379", "sentinel2:26379"},
})

broker := brokerRedis.NewBroker(redisClient)
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
strego:queues                 # Set - all queue names
```

## Performance & Scaling

- **Horizontal Scaling**: Add more workers to the same consumer group
- **Crash Recovery**: Orphaned tasks automatically claimed by healthy workers
- **Queue Isolation**: Multiple queues with minimal overhead
- **Batch Processing**: Configurable batch size for optimal throughput
- **Panic Recovery**: Handler panics are caught and result in retry

## Comparison

| Feature | asynq | machinery | strego |
|---------|-------|-----------|--------|
| Redis Backend | Lists | Lists | **Streams** |
| Consumer Groups | Custom | Custom | **Native** |
| Crash Recovery | Limited | Limited | **Built-in** |
| PostgreSQL | ❌ | ❌ | ✅ |
| Web UI | Separate | ❌ | **Built-in** |
| Exactly-once | UniqueTask | ❌ | ✅ |
| Panic Recovery | ✅ | ✅ | ✅ |
| Logging | slog | - | **logrus** |

## Roadmap

- [ ] Redis Cluster support
- [ ] Prometheus metrics
- [ ] OpenTelemetry tracing
- [ ] Workflow support (chains, groups)
- [ ] Periodic/Cron tasks
- [ ] Rate limiting

## License

MIT
