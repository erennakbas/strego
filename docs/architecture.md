# Strego Architecture

This document describes the architecture and design of Strego, a distributed task queue library for Go.

## Table of Contents

- [System Overview](#system-overview)
- [Component Architecture](#component-architecture)
- [Task Lifecycle](#task-lifecycle)
- [Task Processing Flow](#task-processing-flow)
- [Redis Data Structures](#redis-data-structures)
- [Scaling & Performance](#scaling--performance)

## System Overview

Strego is built on top of Redis Streams and provides a production-ready task queue system with features like scheduled tasks, retries, dead letter queues, and exactly-once processing.
<img width="991" height="401" alt="image" src="https://github.com/user-attachments/assets/0ee8fa97-952f-4f88-9286-1b458d0799dd" />


## Component Architecture
<img width="978" height="318" alt="image" src="https://github.com/user-attachments/assets/2dbec6c5-194c-4a53-94c0-1439db1ec0af" />


## Task Lifecycle

A task goes through several states during its lifecycle:

## Task Processing Flow
<img width="924" height="536" alt="image" src="https://github.com/user-attachments/assets/1a53994e-2820-48ee-b8ce-fba512694cdc" />

### Enqueue Flow
<img width="720" height="712" alt="image" src="https://github.com/user-attachments/assets/99238d33-3a78-485f-bc77-26da4a4a7989" />

### Processing Flow
<img width="754" height="1279" alt="image" src="https://github.com/user-attachments/assets/5e099c3b-69b0-45bd-bd77-49f678649823" />

### Scheduler Flow
<img width="674" height="812" alt="image" src="https://github.com/user-attachments/assets/07d1a86a-419c-41f8-9405-b9607c5b821c" />


participant "Server" as Server
participant "Scheduler" as Scheduler
participant "Broker" as Broker
database "Redis" as Redis

loop Every 1 Second
  Scheduler -> Broker: GetScheduled(until=now, limit=100)
  Broker -> Redis: ZRANGEBYSCORE scheduled\n(-inf, now)
  Redis --> Broker: Tasks
  Broker -> Redis: ZPOPMIN scheduled
  Redis --> Broker: Tasks (removed)
  Broker --> Scheduler: Tasks
  
  loop For Each Task
    Scheduler -> Broker: MoveToQueue(task)
    activate Broker
    Broker -> Redis: Pipeline:\nXADD stream:queue\nSADD queues\nHINCRBY stats:queue
    activate Redis
    Redis --> Broker: OK (all commands)
    deactivate Redis
    deactivate Broker
  end
  
  Scheduler -> Broker: GetRetry(until=now, limit=100)
  Broker -> Redis: ZRANGEBYSCORE retry\n(-inf, now)
  Redis --> Broker: Tasks
  Broker -> Redis: ZPOPMIN retry
  Redis --> Broker: Tasks (removed)
  Broker --> Scheduler: Tasks
  
  loop For Each Retry Task
    Scheduler -> Broker: MoveToQueue(task)
    activate Broker
    Broker -> Redis: Pipeline:\nXADD stream:queue\nSADD queues\nHINCRBY stats:queue
    activate Redis
    Redis --> Broker: OK (all commands)
    deactivate Redis
    deactivate Broker
  end
end

@enduml
```

## Redis Data Structures

```plantuml
@startuml Redis Data Structures
!theme plain
skinparam componentStyle rectangle

package "Redis Keys" {
  component "strego:stream:{queue}" as Stream {
    note right: Redis Stream\nXADD, XREADGROUP
  }
  
  component "strego:dlq:{queue}" as DLQ {
    note right: Redis Stream\nDead Letter Queue
  }
  
  component "strego:scheduled" as Scheduled {
    note right: Sorted Set (ZSET)\nScore = Unix Timestamp
  }
  
  component "strego:retry" as Retry {
    note right: Sorted Set (ZSET)\nScore = Retry Time
  }
  
  component "strego:processed:{task_id}" as Processed {
    note right: String with TTL\nIdempotency Check
  }
  
  component "strego:unique:{key}" as Unique {
    note right: String with TTL\nDeduplication
  }
  
  component "strego:stats:{queue}" as Stats {
    note right: Hash\nQueue Statistics
  }
  
  component "strego:queues" as Queues {
    note right: Set\nAll Queue Names
  }
}

@enduml
```

## Scaling & Performance

### Horizontal Scaling

<img width="1539" height="338" alt="image" src="https://github.com/user-attachments/assets/75dd3d8f-26b4-4fac-a1aa-583a855fb3aa" />

### Queue Isolation

<img width="1023" height="260" alt="image" src="https://github.com/user-attachments/assets/1714c07d-2239-48db-a874-d8b47d9832e3" />


## Key Design Decisions

1. **Redis Streams over Lists**: Native consumer groups, better crash recovery, built-in message tracking
2. **Protobuf Serialization**: Type-safe, efficient, language-agnostic
3. **Lazy Queue Creation**: Queues created on first use, no upfront cost
4. **Idempotency via SET NX**: Exactly-once processing guarantee
5. **Exponential Backoff**: Configurable retry strategy with max duration cap
6. **Optional PostgreSQL**: Redis is primary, PostgreSQL for history/search
7. **Single Consumer Group**: All workers in same group for load balancing

## Performance Characteristics

- **Queue Creation**: Lazy, ~1-2KB per empty queue
- **Batch Processing**: Configurable batch size (default: 10)
- **Blocking Reads**: Configurable timeout (default: 5s)
- **Horizontal Scaling**: Linear with number of workers
- **Memory Efficient**: Automatic cleanup of processed tasks
