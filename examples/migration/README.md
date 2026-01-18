# Strego Migration Example

This example demonstrates how to set up the required PostgreSQL schema for strego.

## Prerequisites

- PostgreSQL database
- Go 1.21+

## Quick Start

### Option 1: Print SQL (Recommended for Production)

Print the migration SQL to stdout. You can pipe this to `psql` or save to a file:

```bash
# Print SQL
go run main.go -print

# Pipe directly to psql
go run main.go -print | psql -h localhost -U postgres -d mydb

# Save to file for review
go run main.go -print > strego_migration.sql
```

### Option 2: Run Migration Directly

Run the migration against your database:

```bash
go run main.go -dsn 'postgres://user:password@localhost:5432/mydb?sslmode=disable'
```

### Option 3: List Available Migrations

```bash
go run main.go -list
```

## Using with External Migration Tools

Strego provides SQL migration files in the `migrations/` directory. You can use these with your preferred migration tool.

### With golang-migrate

```bash
# Install golang-migrate
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# Run migrations
migrate -path ./migrations -database "postgres://user:pass@localhost:5432/mydb?sslmode=disable" up
```

### With goose

```bash
# Install goose
go install github.com/pressly/goose/v3/cmd/goose@latest

# Run migrations
goose -dir ./migrations postgres "postgres://user:pass@localhost:5432/mydb?sslmode=disable" up
```

### With psql (Manual)

```bash
# Using the migration files directly
psql -h localhost -U postgres -d mydb -f migrations/001_create_strego_tasks.sql
psql -h localhost -U postgres -d mydb -f migrations/002_create_strego_stats.sql
```

## Programmatic Usage

You can also run migrations in your application code:

```go
package main

import (
    "database/sql"
    "log"
    
    _ "github.com/lib/pq"
    "github.com/erennakbas/strego/migrations"
)

func main() {
    db, _ := sql.Open("postgres", "postgres://...")
    
    // Get migration SQL
    migrationSQL, err := migrations.GetAllSQL()
    if err != nil {
        log.Fatal(err)
    }
    
    // Execute
    if _, err := db.Exec(migrationSQL); err != nil {
        log.Fatal(err)
    }
    
    log.Println("Migration completed!")
}
```

## Migration Files

| File | Description |
|------|-------------|
| `001_create_strego_tasks.sql` | Creates the main `strego_tasks` table with indexes |
| `002_create_strego_stats.sql` | Creates optional statistics views |

## Schema Overview

The migration creates:

- **`strego_tasks`** table - Stores task history and state
- **Indexes** - Optimized for common query patterns (queue+state, scheduled tasks, etc.)
- **Views** (optional) - `strego_task_stats`, `strego_queue_stats`, `strego_type_stats`

## Notes

- All migrations use `IF NOT EXISTS` / `OR REPLACE` - safe to run multiple times
- The `strego_tasks` table uses JSONB for payload and labels
- Views in `002_create_strego_stats.sql` are optional but useful for monitoring
