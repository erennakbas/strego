# strego Production-Like Example

This example demonstrates a realistic production scenario with:

- **Phase 1**: All tasks succeed immediately
- **Phase 2**: Tasks fail once, then succeed on retry
- **Phase 3**: Tasks fail multiple times, some go to Dead Letter Queue

## Prerequisites

```bash
# Redis running
docker ps | grep redis

# PostgreSQL with strego tables
psql -U erenakbas -h localhost -d picus_ng_test -c "SELECT COUNT(*) FROM strego_tasks;"
```

## Run

```bash
# 1. Clear Redis first
docker exec redigo-redis redis-cli FLUSHDB

# 2. Run the example
cd examples/production
go run main.go

# 3. Open dashboard
open http://localhost:8080
```

## Scenarios

| Phase | Duration | Tasks | Behavior |
|-------|----------|-------|----------|
| Phase 1 | 0-10s | 5 welcome emails | All succeed ✅ |
| Phase 2 | 10-40s | 5 newsletters | Fail once, retry succeeds 🔄✅ |
| Phase 3 | 40-70s | 3 orders + 3 notifications | Orders: 2 fails → success. Notifications: always fail → DLQ 💀 |

## Expected Results

After ~70 seconds:

- **Completed**: 13 tasks (5 welcome + 5 newsletter + 3 orders)
- **Dead**: 3 tasks (notifications in DLQ)

## Task Types

| Task Type | Behavior |
|-----------|----------|
| `email:welcome` | Always succeeds |
| `email:newsletter` | Fails on 1st attempt, succeeds on retry |
| `order:confirm` | Fails 2 times, succeeds on 3rd (max_retry=3) |
| `notification:push` | Always fails → goes to DLQ (max_retry=2) |
