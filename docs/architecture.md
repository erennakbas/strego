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

```plantuml
@startuml System Architecture
!theme plain
skinparam componentStyle rectangle

package "Application Layer" {
  component [Client] as Client
  component [Server] as Server
  component [Web UI] as UI
}

package "Strego Core" {
  component [Broker Interface] as Broker
  component [Store Interface] as Store
}

package "Redis" {
  component "Streams\nstrego:stream:*" as Streams
  component "DLQ\nstrego:dlq:*" as DLQ
  component "Scheduled\nstrego:scheduled" as Scheduled
  component "Retry\nstrego:retry" as Retry
  component "Idempotency\nstrego:processed:*" as Idempotency
}

database "PostgreSQL\n(Optional)" as PG {
  component [Task History] as TaskDB
}

Client --> Broker : Enqueue
Server --> Broker : Subscribe
UI --> Broker : Query
UI --> Store : Query

Broker --> Streams
Broker --> DLQ
Broker --> Scheduled
Broker --> Retry
Broker --> Idempotency

Client ..> Store : Optional
Server ..> Store : Optional
Store --> TaskDB

@enduml
```

## Component Architecture

```plantuml
@startuml Component Diagram
!theme plain
skinparam componentStyle rectangle

package "pkg/strego" {
  component [Client] as Client
  component [Server] as Server
  component [Task] as Task
  component [Handler] as Handler
  component [ServeMux] as ServeMux
}

package "pkg/broker" {
  interface "Broker" as IBroker
  component [Redis Broker] as RedisBroker
}

package "pkg/store" {
  interface "Store" as IStore
  component [PostgreSQL Store] as PGStore
}

package "pkg/types" {
  component [TaskProto] as TaskProto
  component [TaskMetadata] as Metadata
  component [TaskOptions] as Options
}

Client --> IBroker
Client --> IStore
Server --> IBroker
Server --> IStore
Server --> ServeMux
ServeMux --> Handler

IBroker <|.. RedisBroker
IStore <|.. PGStore

Task --> TaskProto
TaskProto --> Metadata
TaskProto --> Options

@enduml
```

## Task Lifecycle

A task goes through several states during its lifecycle:

```plantuml
@startuml Task Lifecycle
!theme plain
skinparam state {
  BackgroundColor LightBlue
  BorderColor DarkBlue
}

[*] --> Pending : Enqueue
Pending --> Scheduled : ProcessAt in future
Scheduled --> Pending : Scheduler moves to queue
Pending --> Active : Consumer picks up
Active --> Completed : Handler succeeds
Active --> Retry : Handler fails (retry < max)
Retry --> Pending : Backoff delay expires
Active --> Dead : Handler fails (retry >= max)
Completed --> [*]
Dead --> [*]

note right of Scheduled
  Stored in Redis Sorted Set
  (strego:scheduled)
end note

note right of Retry
  Stored in Redis Sorted Set
  (strego:retry)
  with exponential backoff
end note

note right of Dead
  Moved to Dead Letter Queue
  (strego:dlq:queue)
end note

@enduml
```

## Task Processing Flow

### Enqueue Flow

```plantuml
@startuml Enqueue Flow
!theme plain

actor "Application" as App
participant "Client" as Client
participant "Broker" as Broker
database "Redis" as Redis
database "PostgreSQL" as PG

App -> Client: Enqueue(task)
activate Client

alt Unique Key Set
  Client -> Broker: SetUnique(key, taskID, TTL)
  Broker -> Redis: SET NX unique:key
  Redis --> Broker: OK/NX
  alt Key Exists
    Broker --> Client: ErrDuplicateTask
    Client --> App: Error
    deactivate Client
  end
end

alt ProcessAt in Future
  Client -> Broker: Schedule(task, processAt)
  Broker -> Redis: ZADD scheduled (score=processAt)
  Redis --> Broker: OK
else Immediate
  Client -> Broker: Publish(queue, task)
  Broker -> Redis: XADD stream:queue
  Broker -> Redis: SADD queues
  Broker -> Redis: HINCRBY stats:queue
  Redis --> Broker: OK
end

opt PostgreSQL Store Configured
  Client -> PG: INSERT task
  PG --> Client: OK
end

Broker --> Client: Success
Client --> App: TaskInfo
deactivate Client

@enduml
```

### Processing Flow

```plantuml
@startuml Processing Flow
!theme plain

participant "Server" as Server
participant "Broker" as Broker
database "Redis" as Redis
participant "Handler" as Handler
database "PostgreSQL" as PG

loop Consumer Loop
  Server -> Broker: Subscribe(queues)
  activate Broker
  
  Broker -> Redis: XReadGroup\n(Block 5s, Count 10)
  activate Redis
  Redis --> Broker: Messages
  deactivate Redis
  
  Broker -> Server: processTask(task)
  activate Server
  
  Server -> Broker: SetProcessed(taskID, TTL)
  Broker -> Redis: SET NX processed:taskID
  Redis --> Broker: OK/NX
  
  alt Already Processed
    Server --> Broker: Skip (idempotency)
  else New Task
    Server -> PG: UPDATE state=active
    activate PG
    PG --> Server: OK
    deactivate PG
    
    Server -> Handler: ProcessTask(ctx, task)
    activate Handler
    
    alt Handler Succeeds
      Handler --> Server: Success
      deactivate Handler
      
      Server -> Broker: Ack(msgID)
      Broker -> Redis: XACK + XDEL
      activate Redis
      Redis --> Broker: OK
      deactivate Redis
      
      Server -> PG: UPDATE state=completed
      activate PG
      PG --> Server: OK
      deactivate PG
      
    else Handler Fails
      Handler --> Server: Error
      deactivate Handler
      
      Server -> Server: handleFailure()
      
      alt Retry Count < Max
        Server -> Broker: Retry(queue, task, delay)
        Broker -> Redis: ZADD retry\n(score=now+delay)
        activate Redis
        Redis --> Broker: OK
        deactivate Redis
        
        Server -> PG: UPDATE state=retry
        activate PG
        PG --> Server: OK
        deactivate PG
        
      else Retry Count >= Max
        Server -> Broker: MoveToDLQ(queue, task)
        Broker -> Redis: XADD dlq:queue
        Broker -> Redis: XACK original
        activate Redis
        Redis --> Broker: OK
        deactivate Redis
        
        Server -> PG: UPDATE state=dead
        activate PG
        PG --> Server: OK
        deactivate PG
      end
    end
  end
  
  Server --> Broker: Done
  deactivate Server
  deactivate Broker
end

@enduml
```

### Scheduler Flow

```plantuml
@startuml Scheduler Flow
!theme plain

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
    Broker -> Redis: XADD stream:queue
    Redis --> Broker: OK
  end
  
  Scheduler -> Broker: GetRetry(until=now, limit=100)
  Broker -> Redis: ZRANGEBYSCORE retry\n(-inf, now)
  Redis --> Broker: Tasks
  Broker -> Redis: ZPOPMIN retry
  Redis --> Broker: Tasks (removed)
  Broker --> Scheduler: Tasks
  
  loop For Each Retry Task
    Scheduler -> Broker: MoveToQueue(task)
    Broker -> Redis: XADD stream:queue
    Redis --> Broker: OK
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

```plantuml
@startuml Horizontal Scaling
!theme plain

cloud "Redis Streams" as Redis {
  component "strego:stream:email" as EmailQueue
  component "strego:stream:notification" as NotifQueue
}

node "Worker 1" as W1 {
  component [Server] as S1
  component "Consumer Group: strego-workers" as CG1
}

node "Worker 2" as W2 {
  component [Server] as S2
  component "Consumer Group: strego-workers" as CG2
}

node "Worker 3" as W3 {
  component [Server] as S3
  component "Consumer Group: strego-workers" as CG3
}

W1 --> Redis : XReadGroup
W2 --> Redis : XReadGroup
W3 --> Redis : XReadGroup

Redis --> W1 : Message 1
Redis --> W2 : Message 2
Redis --> W3 : Message 3

note right of Redis
  Redis Streams Consumer Groups
  automatically distribute messages
  across workers in the same group
end note

@enduml
```

### Queue Isolation

```plantuml
@startuml Queue Isolation
!theme plain

package "Multiple Queues" {
  component "strego:stream:email" as Email
  component "strego:stream:payment" as Payment
  component "strego:stream:analytics" as Analytics
  component "strego:stream:report" as Report
  component "..." as More
}

component "Server" as Server {
  component "Single XReadGroup Call" as Read
}

Server --> Email
Server --> Payment
Server --> Analytics
Server --> Report
Server --> More

note right of Server
  All queues consumed in
  a single efficient Redis call
  Minimal overhead per queue
  (~1-2KB when empty)
end note

@enduml
```

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
