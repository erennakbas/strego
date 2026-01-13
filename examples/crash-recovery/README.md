# Crash Recovery Example

This example demonstrates how strego handles worker crashes using Redis Streams' consumer group features.

## How It Works

When a worker starts processing a task, the task is marked as "pending" in the consumer group. If the worker crashes before acknowledging (ACK) the task, it becomes an **orphaned task**.

The `ClaimStaleAfter` configuration tells other workers to automatically claim these orphaned tasks after a specified duration.

## Prerequisites

1. **Redis** running on localhost:6379
2. **PostgreSQL** running with the database configured
3. Run migrations:

```bash
psql -U erenakbas -d picus_ng_test -f migrations/001_create_strego_tasks.sql
psql -U erenakbas -d picus_ng_test -f migrations/002_create_strego_stats.sql
```

## 🌐 Visual Monitoring with UI

For the **best experience**, monitor the crash recovery in real-time:

```bash
# Terminal 1: Start Monitor UI
cd examples/monitor
go run main.go
# Open http://localhost:8080

# Terminal 2: Run crash simulation
cd examples/crash-recovery
go run main.go
```

**💡 Tip:** In the UI, select **"crash-recovery-demo"** from the dropdown at top-right to see this worker's stats!

**What you'll see in the UI:**

### During Crash (First Run):
```
Queue: crash-test
├─ Pending:   0
├─ Active:    1   ← Task being processed
├─ Processed: 0
└─ Dead:      0

💥 Worker crashes!

├─ Pending:   0
├─ Active:    1   ← ORPHANED! No worker processing it!
├─ Processed: 0
└─ Dead:      0
```

### During Recovery (Second Run):
```
New worker starts...
Wait 15 seconds...

├─ Pending:   0
├─ Active:    1   ← Still orphaned
├─ Processed: 0
└─ Dead:      0

XAUTOCLAIM happens!

├─ Pending:   0
├─ Active:    1   ← New worker claimed it!
├─ Processed: 0
└─ Dead:      0

Task completes (5 seconds)

├─ Pending:   0
├─ Active:    0
├─ Processed: 1   ← Success!
└─ Dead:      0
```

**Monitor Features:**
- ✅ **Clear data sources** - Real-time (Redis) vs Historical (PostgreSQL) badges
- ✅ Real-time queue stats
- ✅ **Worker health status** (🟢 Active / 🟡 Idle / 🔴 Dead based on idle time)
- ✅ **Info tooltips** explaining each metric and state
- ✅ **Collapsible sections** for clean UI when many workers/queues
- ✅ Task history (PostgreSQL)
- ✅ Individual task details
- ✅ Consumer tracking (see which worker has which task)
- ✅ Search and filter

## Running the Demo

This example has **two modes** that automatically detect the scenario:

### 🎬 Mode 1: CRASH SIMULATION (First Run)

```bash
cd examples/crash-recovery
go run main.go
```

**What happens:**
```
╔════════════════════════════════════════════════════╗
║            💥 CRASH SIMULATION MODE 💥             ║
║  This is the FIRST run - will crash during work   ║
╚════════════════════════════════════════════════════╝

📤 enqueueing 1 slow task
🔄 starting slow task - will take 60 seconds
⏳ still working... progress=5/60
⏳ still working... progress=10/60

💥💥💥 SIMULATED CRASH - Worker died unexpectedly! 💥💥💥
💥 Task is now ORPHANED - run again to recover!
```

**Timeline:**
- ✅ Task enqueued
- ✅ Worker starts processing (60 second task)
- 💥 **Auto-crash after 10 seconds** (simulated failure)
- ⚠️ Task remains in Redis as "active" (orphaned!)

### 🔄 Mode 2: RECOVERY MODE (Second Run)

Within 15 seconds, run again:

```bash
go run main.go
```

**What happens:**
```
╔════════════════════════════════════════════════════╗
║            🔄 RECOVERY MODE ACTIVATED 🔄           ║
║  Found orphaned tasks - claiming in 15 seconds    ║
║  Tasks will process FAST (5 seconds each)         ║
╚════════════════════════════════════════════════════╝

⏰ orphaned tasks will be claimed after this duration
🔄 RECOVERY MODE: fast processing - will take 5 seconds
⏳ still working... progress=5/5
✅ slow task completed!
```

**Timeline:**
- 🔍 Detects orphaned tasks in queue
- ⏱️ Waits 15 seconds (ClaimStaleAfter)
- ✅ **XAUTOCLAIM** claims orphaned task
- ⚡ Fast processing (5 seconds instead of 60!)
- ✅ Task completed successfully

## Configuration

```go
brokerRedis.NewBroker(redisClient, brokerRedis.WithConsumerConfig(broker.ConsumerConfig{
    ClaimStaleAfter: 15 * time.Second, // Demo: 15 seconds
    // Production: 5 * time.Minute or more
}))
```

**Important**: In production, use a longer `ClaimStaleAfter` (5+ minutes) to avoid claiming tasks from workers that are just slow, not dead.

## What Happens Under the Hood

1. Worker A reads task from stream → Redis marks it as "pending" for Worker A
2. Worker A crashes before ACK
3. Worker B starts and calls `XAUTOCLAIM` with `MinIdle=ClaimStaleAfter`
4. Redis transfers ownership of stale pending tasks to Worker B
5. Worker B processes and ACKs the task

## 🔍 Inspect PEL (Pending Entries List)

Watch the Redis PEL in real-time:

```bash
# Terminal 3: Monitor PEL
watch -n 1 'redis-cli XPENDING strego:stream:crash-test crash-recovery-demo - + 10'
```

**During crash:**
```
1) "1768145234567-0"
2) "worker-MacBook-Pro-12345"  ← Dead worker!
3) (integer) 18234               ← 18 seconds idle (> 15 seconds!)
4) (integer) 1                   ← Delivery count: 1
```

**After recovery:**
```
1) "1768145234567-0"
2) "worker-MacBook-Pro-67890"  ← New worker claimed it!
3) (integer) 1234                ← Fresh idle time
4) (integer) 2                   ← Delivery count: 2 (was redelivered!)
```

## Redis Commands Used

- `XREADGROUP` - Read new messages from stream
- `XAUTOCLAIM` - Claim stale pending messages from dead consumers (⭐ THE KEY!)
- `XACK` - Acknowledge processed messages
- `XPENDING` - Inspect pending entries list (for debugging)

