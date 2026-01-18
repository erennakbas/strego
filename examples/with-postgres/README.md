# strego with PostgreSQL Example

This example demonstrates strego with PostgreSQL for full task history and UI features.

## Prerequisites

- Redis running on `localhost:6379`
- PostgreSQL running on `localhost:5432`

## Setup

### 1. Start Redis

```bash
# Using Docker
docker run -d --name strego-redis -p 6379:6379 redis:alpine

# Or if you have Redis installed locally
redis-server
```

### 2. Create PostgreSQL Database

```bash
# Connect to PostgreSQL
psql -U erenakbas -h localhost

# Create database
CREATE DATABASE strego;

# Exit
\q
```

### 3. Run Migrations

You have several options to run the required migrations:

```bash
# Option 1: Using psql directly
psql -h localhost -U postgres -d strego -f migrations/001_create_strego_tasks.sql
psql -h localhost -U postgres -d strego -f migrations/002_create_strego_stats.sql

# Option 2: Using the migration example
cd examples/migration
go run main.go -dsn 'postgres://user:pass@localhost:5432/strego?sslmode=disable'

# Option 3: Print SQL and pipe to psql
cd examples/migration
go run main.go -print | psql -h localhost -U postgres -d strego

# Option 4: Programmatically in your code
# See examples/migration/README.md for details
```

For more migration options (golang-migrate, goose, etc.), see [examples/migration/README.md](../migration/README.md).

### 4. Run the Example

```bash
cd examples/with-postgres
go run main.go
```

### 5. Open the Dashboard

Visit [http://localhost:8080](http://localhost:8080) to see the web UI.

## Configuration

Edit `main.go` to change connection settings:

```go
// Redis
redisClient := redis.NewClient(&redis.Options{
    Addr:     "localhost:6379",
    Password: "", // if needed
    DB:       0,
})

// PostgreSQL
pgStore, _ := postgres.New(postgres.Config{
    DSN: "postgres://erenakbas:admin123@localhost:5432/strego?sslmode=disable",
})
```

## What This Example Does

1. **Connects** to Redis (message broker) and PostgreSQL (task history)
2. **Registers handlers** for 5 task types:
   - `email:send` - Email sending (simulated 20% failure rate)
   - `order:process` - Order processing (simulated 10% failure rate)
   - `report:generate` - Report generation (long-running)
   - `notification:push` - Push notifications
   - `image:resize` - Image resizing (simulated 15% failure rate)
3. **Starts the Web UI** on port 8080
4. **Enqueues example tasks**:
   - 10 notifications (critical queue)
   - 20 emails (default queue)
   - 5 orders (default queue)
   - 8 images (low queue)
   - 3 scheduled reports (low queue, delayed)
   - 1 unique task (deduplication demo)

## Dashboard Features

With PostgreSQL enabled, the dashboard provides:

- **Real-time stats** - Task counts by state
- **Queue overview** - Pending, active, dead counts per queue
- **Task list** - Search, filter by queue/state/type
- **Task detail** - Full payload, error, timeline
- **Dead letter queue** - View, retry, purge failed tasks

## Cleanup

```bash
# Stop Redis
docker stop strego-redis && docker rm strego-redis

# Drop database
psql -U erenakbas -h localhost -c "DROP DATABASE strego;"
```
