# Scheduled Tasks Example

This example demonstrates how to schedule tasks for future execution with strego.

## Prerequisites

- Redis running on `localhost:6379`

## Run

```bash
# Start Redis
docker run -d --name strego-redis -p 6379:6379 redis:alpine

# Run the example
cd examples/scheduled
go run main.go
```

## Features Demonstrated

### 1. WithProcessIn - Delay from Now

Schedule a task to run after a specific duration:

```go
task := strego.NewTaskFromBytes("reminder:send",
    []byte(`{"message": "Don't forget!"}`),
    strego.WithProcessIn(10*time.Second),  // Run 10 seconds from now
)
client.Enqueue(ctx, task)
```

### 2. WithProcessAt - Specific Time

Schedule a task to run at a specific time:

```go
reportTime := time.Now().Add(1 * time.Hour)
task := strego.NewTaskFromBytes("report:generate",
    []byte(`{"type": "daily"}`),
    strego.WithProcessAt(reportTime),  // Run at specific time
)
client.Enqueue(ctx, task)
```

### 3. Scheduler Configuration

Configure how often the scheduler checks for ready tasks:

```go
server := strego.NewServer(broker,
    strego.WithSchedulerInterval(1*time.Second),   // Check every 1 second (default: 5s)
    strego.WithSchedulerBatchSize(200),            // Process 200 tasks per tick (default: 100)
)
```

## How It Works

```
1. Client.Enqueue() with ProcessAt/ProcessIn
   ↓
2. Task added to Redis Sorted Set (score = Unix timestamp)
   Key: strego:scheduled
   ↓
3. Server scheduler runs every 5 seconds (configurable)
   ↓
4. Lua script atomically:
   - Finds tasks with score <= now
   - Removes from sorted set
   - Adds to target queue stream
   ↓
5. Worker picks up and processes the task
```

## Output

When you run the example, you'll see:

```
INFO connected to redis
INFO starting task processing...
INFO enqueuing scheduled tasks...
INFO scheduled: reminder in 10 seconds    task_id=abc12345 process_at=15:30:10
INFO scheduled: welcome email in 20 seconds    task_id=def67890 process_at=15:30:20
INFO scheduled: daily report at specific time    task_id=ghi11111 process_at=15:30:30
INFO enqueued: immediate task (no delay)    task_id=jkl22222
INFO sending reminder    task_id=jkl22222    <- immediate task runs first
INFO reminder sent    task_id=jkl22222
... (10 seconds later)
INFO sending reminder    task_id=abc12345    <- scheduled task runs
INFO reminder sent    task_id=abc12345
... (20 seconds later)
INFO sending welcome email    task_id=def67890
```

## Cleanup

```bash
docker stop strego-redis && docker rm strego-redis
```
