# Strego Monitor - Standalone UI

**Read-only monitoring dashboard** for strego workers.

This is a standalone UI server that **does NOT process any tasks**. It only monitors and displays queue statistics.

## Use Case

Run this in one terminal to monitor workers running in other terminals:

```
Terminal 1: go run examples/crash-recovery/main.go    (worker)
Terminal 2: go run examples/monitor/main.go            (UI only)
```

## Prerequisites

### Required
1. **Redis** running on localhost:6379
   - Powers all real-time features
   - Consumer groups, queue stats, DLQ

### Optional (for full features)
2. **PostgreSQL** running with the database configured
   - Task history and details
   - Search and filtering
   - System statistics

Run migrations if using PostgreSQL:
```bash
# From project root
psql -U erenakbas -d picus_ng_test -f migrations/001_create_strego_tasks.sql
psql -U erenakbas -d picus_ng_test -f migrations/002_create_strego_stats.sql
```

**Note:** The monitor works with Redis only, but PostgreSQL adds task history features.

## Quick Start

### 1. Start Redis

```bash
redis-server
```

### 2. Start the Monitor

**Important:** Set the `CONSUMER_GROUP` env variable to match your workers!

```bash
cd examples/monitor

# Monitor crash-recovery workers
CONSUMER_GROUP=crash-recovery-demo go run main.go

# OR monitor production workers
CONSUMER_GROUP=strego-prod-example go run main.go

# OR use default group
go run main.go  # Uses "strego-workers"
```

You'll see:

```
📋 using consumer group for monitoring group=crash-recovery-demo
✅ connected to redis
✅ connected to postgresql
╔════════════════════════════════════════════════════╗
║                                                    ║
║        🌐  STREGO MONITOR UI IS READY  🌐         ║
║                                                    ║
║          http://localhost:8080                     ║
║                                                    ║
║  This is a read-only monitoring dashboard.        ║
║  Start workers in other terminals to see them.    ║
║                                                    ║
║  📋 Consumer Group: crash-recovery-demo            ║
║  ✅ PostgreSQL: Task history enabled               ║
║                                                    ║
╚════════════════════════════════════════════════════╝
```

### 3. Open Your Browser

Go to **http://localhost:8080**

**Pages:**
- **Dashboard** ⭐ **Consumer group selector + active workers** - Select a group from dropdown to view its stats
- **Queues** - Queue statistics and details
- **Tasks** - Task history (requires PostgreSQL)
- **Dead Letter Queue** - Failed tasks

### 4. Start Some Workers

In another terminal:

```bash
# Example 1: Crash recovery demo
cd examples/crash-recovery
go run main.go
```

Or:

```bash
# Example 2: Production demo with scenarios
cd examples/production
go run main.go
```

### 5. Watch the UI

The dashboard will show:
- 📊 Queue statistics (pending, active, processed)
- 💀 Dead letter queue tasks
- ⏱️ Real-time updates (refresh page)

## What You'll See

### Dashboard

```
┌─────────────────────────────────────────────────────────┐
│  Consumer Group: crash-recovery-demo ▼                  │
├─────────────────────────────────────────────────────────┤
│  👥 Workers in Consumer Group (2 workers)               │
│  ├─ 🟢 Active   worker-1234-567-890  PEL: 1  Idle: 2s  │
│  └─ 🔴 Dead     worker-5678-901-234  PEL: 3  Idle: 8m  │
├─────────────────────────────────────────────────────────┤
│  📬 Queue: crash-test                                   │
│  ├─ Pending:   5  ⏳                                    │
│  ├─ Active:    2  ⚡ (tasks in PEL)                    │
│  ├─ Processed: 23 ✅                                    │
│  └─ Dead:      0  💀                                    │
└─────────────────────────────────────────────────────────┘
```

**Note:** The 🔴 Dead worker still shows in the list because it has tasks in the PEL (Pending Entries List). These orphaned tasks will be automatically claimed by healthy workers after the `ClaimStaleAfter` timeout.

### Features Available

| Feature | Data Source | Notes |
|---------|-------------|-------|
| **Dashboard** | | |
| Consumer Group Selector | 🔴 Redis | Dropdown to switch groups instantly |
| Worker Status & Health | 🔴 Redis | 🟢 Active / 🟡 Idle / 🔴 Dead based on idle time |
| Workers in PEL | 🔴 Redis | Shows all workers with pending tasks (alive & dead) |
| Queue Statistics | 🔴 Redis | Real-time from XPENDING, XLEN |
| System Stats | 🟣 PostgreSQL | Overall metrics (optional) |
| Collapsible Sections | UI | Click headers to expand/collapse large lists |
| **Pages** | | |
| Queues | 🔴 Redis | Consumer group queue info |
| Tasks | 🟣 PostgreSQL | Task execution history |
| Task Details | 🟣 PostgreSQL | Individual task info |
| Dead Letter Queue | 🔴 Redis | Failed tasks stream |
| **Actions** | | |
| Retry/Purge DLQ | 🔴 Redis | Move tasks back to queue |
| Search & Filter | 🟣 PostgreSQL | By queue, state, type |

**Legend:**
- 🔴 **Redis Streams** - Real-time, always available
- 🟣 **PostgreSQL** - History tracking, requires setup

## PostgreSQL Configuration

PostgreSQL is **enabled by default** in this example!

If you want to **disable** it (Redis-only mode):

1. Edit `main.go` and comment out the PostgreSQL section
2. Set `Store: nil` in the UI config

If you need to **change the connection string**:

```go
pgStore, err := postgres.New(postgres.Config{
    DSN: "postgres://youruser:yourpass@localhost:5432/yourdb?sslmode=disable",
})
```

## 🎯 Consumer Group Monitoring

**NEW: Dynamic Group Selection! 🎉**

The monitor automatically discovers all consumer groups. Simply use the **dropdown selector** on the dashboard:

### Dashboard Features
- **Clear data source indicators**:
  - 🔴 **Real-time** badge with pulsing dot = Redis Streams (live data)
  - 🟣 **Historical** badge = PostgreSQL (stored data)
- **Dropdown selector** at the top-right to choose consumer group
- **Worker status indicators** based on idle time:
  - 🟢 **Active** - < 1 minute idle (healthy worker)
  - 🟡 **Idle** - 1-5 minutes idle (may be slow/stuck)
  - 🔴 **Dead** - > 5 minutes idle (crashed/terminated)
- **Info tooltips** (ℹ️) - hover over info icons to learn about:
  - Worker status thresholds and what they mean
  - Task states (Pending, Active, Completed, Dead)
  - Difference between Redis PEL and PostgreSQL states
  - Auto-recovery behavior for crashed workers
- Shows workers in consumer group (pending tasks in PEL, idle time, status)
- **Collapsible sections** - click headers to expand/collapse
- Displays queue statistics for that group
- Instant switching - no restart needed!
- No need to set env vars!

### CONSUMER_GROUP Env Var (Optional)
You can still set `CONSUMER_GROUP` to pre-select a default group:

```bash
CONSUMER_GROUP=crash-recovery-demo go run main.go
```

But it's **no longer required** - just select from the dropdown in the UI!

## Example Workflow

### Monitoring Crash Recovery

```bash
# Terminal 1: Monitor (MUST match worker's group!)
cd examples/monitor
CONSUMER_GROUP=crash-recovery-demo go run main.go

# Terminal 2: Worker
cd examples/crash-recovery
go run main.go

# In browser: http://localhost:8080
# → You'll see pending/active counts ✅ CORRECTLY
# → Kill worker (Ctrl+C)
# → See "active" count stay high (orphaned!)
# → Restart worker
# → Watch as tasks get claimed and completed
```

### Monitoring Production Scenarios

```bash
# Terminal 1: Monitor (match production group)
cd examples/monitor
CONSUMER_GROUP=strego-prod-example go run main.go

# Terminal 2: Production workers
cd examples/production
go run main.go

# In browser: http://localhost:8080
# → Watch all phases of task processing
# → See retries happening
# → View dead letter queue growing
# → Monitor success/failure rates
```

### Monitoring Multiple Workers

```bash
# All workers using the same group can be monitored together
# Terminal 1: Monitor
CONSUMER_GROUP=my-app go run examples/monitor/main.go

# Terminal 2-4: Multiple workers (same group)
CONSUMER_GROUP=my-app go run examples/crash-recovery/main.go
CONSUMER_GROUP=my-app go run examples/crash-recovery/main.go
CONSUMER_GROUP=my-app go run examples/crash-recovery/main.go

# Monitor will show aggregate stats for ALL workers!
```

## Why Use This?

✅ **Separation of Concerns**
- Workers in production servers
- Monitoring UI on ops machine

✅ **No Resource Impact**
- Monitor doesn't process tasks
- Lightweight read-only queries
- Safe to run alongside workers

✅ **Multi-Worker Monitoring**
- Monitor ALL workers from one UI
- See aggregate queue statistics
- Track system-wide metrics

✅ **Debugging**
- Watch queue behavior without interfering
- Monitor orphaned tasks
- Track DLQ growth

## Tips

- **Refresh the page** to see latest stats (auto-refresh coming soon)
- **Multiple workers** all show up in the same queue stats (if using same consumer group)
- **🔴 Dead workers** remain visible if they have pending tasks in PEL - these will auto-recover
- **🧹 Manual Cleanup** - Click the "Cleanup" button in Workers section to manually remove dead consumers (idle > 30 min, pending = 0)
- **Collapsible sections** help when you have many workers/queues - just click the header
- **Port conflict?** Change `:8080` to another port in `main.go`
- **No data?** Check these:
  1. Workers using the same Redis instance? ✅
  2. **CONSUMER_GROUP matches workers?** ⚠️ **MOST COMMON ISSUE!**
  3. PostgreSQL connected for task history? ✅

## Troubleshooting

### Problem: UI shows `0 active` but tasks are processing

**Cause:** Consumer group mismatch!

```bash
# Check which groups exist in Redis
redis-cli XINFO GROUPS strego:stream:crash-test

# Check PEL for specific group
redis-cli XPENDING strego:stream:crash-test crash-recovery-demo
```

**Solution:** Set `CONSUMER_GROUP` to match your worker:
```bash
CONSUMER_GROUP=crash-recovery-demo go run examples/monitor/main.go
```

### Problem: Can't see task history

**Cause:** PostgreSQL not configured or migrations not run.

**Solution:** Run migrations and restart monitor.

## Comparison with Other Examples

| Example | Processes Tasks? | Has UI? | Best For |
|---------|-----------------|---------|----------|
| crash-recovery | ✅ | ❌ | Learning crash recovery |
| crash-recovery-ui | ✅ | ✅ | Demo with visual feedback |
| production | ✅ | ✅ | Full-featured example |
| **monitor** (this) | ❌ | ✅ | **Monitoring running workers** |

## Next Steps

- Run multiple workers and monitor them all from one UI
- Configure PostgreSQL to see full task history
- Use in production to monitor real workers
- Customize port and Redis connection as needed
