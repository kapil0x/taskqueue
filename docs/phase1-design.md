# TaskQueue — Phase 1 Design Document

## What Is This Project?

A **distributed task queue** written in Go, backed by Redis Streams. It lets you enqueue work (tasks) from any part of your application, and have a pool of workers process them asynchronously with automatic retries and failure handling.

Think of it like Sidekiq (Ruby) or Celery (Python), but in Go.

### Real-world use cases
- Sending emails after user signup (don't block the HTTP response)
- Processing image uploads (resize, thumbnail, optimize)
- Syncing data to third-party APIs
- Running periodic reports

---

## Architecture

```
┌──────────────┐         ┌───────────────────┐         ┌──────────────────┐
│   Producer   │         │   Redis Streams    │         │   Worker Pool    │
│  (Client)    │─XADD──▶│                   │◀─XREAD──│  (N goroutines)  │
│              │         │  taskqueue:stream: │  GROUP  │                  │
│  queue.      │         │    default         │         │  worker.Worker   │
│  NewClient() │         │                   │         │                  │
│  .Enqueue()  │         │  Consumer Group:   │──XACK──│  Handlers:       │
│              │         │  "taskqueue-       │         │  email:send → fn │
│              │         │   workers"         │         │  img:resize → fn │
└──────────────┘         │                   │         └──────────────────┘
                         │  Dead Letter:      │
                         │  taskqueue:        │
                         │    dead_letter     │
                         └───────────────────┘
```

### Data flow

1. **Producer** calls `client.Enqueue(ctx, "email:send", payload)`
2. Client serializes the task to JSON, calls `XADD` to append it to the Redis Stream `taskqueue:stream:default`
3. **Worker pool** has N goroutines, each calling `XREADGROUP` in a loop — Redis delivers each message to exactly one consumer within the group
4. Worker looks up the handler registered for `"email:send"`, runs it
5. **On success**: Worker calls `XACK` — Redis removes the message from the pending list
6. **On failure**: Worker increments retry count, applies exponential backoff, and re-enqueues the task. After max retries exhausted, task moves to the dead-letter stream

---

## Components

### 1. Task (`internal/task/task.go`)

The core data model. A Task represents a single unit of work.

**Fields:**

| Field | Type | Purpose |
|-------|------|---------|
| `ID` | `string` | 32-char random hex, globally unique |
| `Queue` | `string` | Which queue this belongs to (default: `"default"`) |
| `Type` | `string` | Identifies which handler processes it (e.g. `"email:send"`) |
| `Payload` | `json.RawMessage` | Arbitrary JSON data the handler will read |
| `State` | `State` | Lifecycle state: pending → active → completed/failed/retry |
| `Retried` | `int` | How many times this task has been retried |
| `MaxRetry` | `int` | Maximum retry attempts before dead-lettering (default: 3) |
| `CreatedAt` | `time.Time` | When the task was created |
| `LastError` | `string` | Error message from the most recent failure |

**State machine:**

```
pending ──▶ active ──▶ completed
                │
                ▼
              retry ──▶ active (re-enqueued)
                │
                ▼ (max retries exceeded)
              failed (moved to dead letter queue)
```

**Key design decisions:**
- `Payload` is `json.RawMessage` (raw bytes), not `interface{}`. This avoids double-encoding — the payload is already JSON when stored, and the handler deserializes it into whatever struct it needs.
- ID generation uses `crypto/rand` (not `math/rand`) — cryptographically random, no seed needed, no collisions.
- Uses the **functional options pattern** for configuration: `task.New("email:send", payload, task.WithMaxRetry(5), task.WithQueue("critical"))`

**Methods:**
- `New(taskType, payload, ...Option) → (*Task, error)` — creates a task with defaults, applies options
- `Encode() → ([]byte, error)` — serializes to JSON
- `Decode([]byte) → (*Task, error)` — deserializes from JSON

---

### 2. Broker (`internal/broker/broker.go`)

The Redis interface layer. Handles all Redis communication. No business logic — just transport.

**Redis key scheme:**

| Key | Type | Purpose |
|-----|------|---------|
| `taskqueue:stream:<queue>` | Stream | One stream per queue name |
| `taskqueue:dead_letter` | Stream | Failed tasks land here |
| Consumer group: `taskqueue-workers` | Group | Created on each stream for coordinated consumption |

**Methods:**

| Method | Redis Command | What it does |
|--------|---------------|-------------|
| `New(addr)` | `PING` | Connects to Redis, verifies connection |
| `Enqueue(ctx, task)` | `XADD` | Serializes task, appends to stream |
| `EnsureGroup(ctx, queue)` | `XGROUP CREATE` | Creates consumer group (idempotent — ignores "already exists" error) |
| `Dequeue(ctx, queue, consumerID, timeout)` | `XREADGROUP` | Blocking read — waits up to `timeout` for a message, returns task + message ID |
| `Ack(ctx, queue, msgID)` | `XACK` | Acknowledges message, removes from pending entries list |
| `SendToDeadLetter(ctx, task)` | `XADD` | Writes failed task to the dead-letter stream |
| `Close()` | — | Closes Redis connection |

**Why XREADGROUP with `">"` ?**

```go
Streams: []string{streamKey, ">"},
```

The `">"` tells Redis: "give me only NEW messages that haven't been delivered to any consumer in this group yet." Without it, you'd re-read old messages.

**Error handling note:** `EnsureGroup` silently handles the `BUSYGROUP` error (group already exists). This makes it safe to call on every worker startup without coordination.

---

### 3. Worker (`internal/worker/worker.go`)

The consumer side. Manages a pool of goroutines that pull tasks from queues and process them.

**Configuration** (via functional options):

| Option | Default | Purpose |
|--------|---------|---------|
| `WithConcurrency(n)` | 5 | Number of goroutines processing tasks in parallel |
| `WithQueues(q...)` | `["default"]` | Which queues to listen on |
| `WithLogger(l)` | `slog.Default()` | Structured logger |

**Concurrency model:**

```
Run()
  │
  ├── goroutine 0: loop(ctx, "hostname-pid-0")
  ├── goroutine 1: loop(ctx, "hostname-pid-1")
  ├── goroutine 2: loop(ctx, "hostname-pid-2")
  ├── goroutine 3: loop(ctx, "hostname-pid-3")
  ├── goroutine 4: loop(ctx, "hostname-pid-4")
  │
  └── main goroutine: blocks on signal channel (SIGINT/SIGTERM)
                       on signal → cancel context → all loops exit
                       wg.Wait() → waits for in-flight tasks to finish
```

Each goroutine gets a unique `consumerID` (e.g. `"macbook-1234-0"`). Redis uses this to track which consumer has which pending messages.

**The loop:**

```
for each goroutine:
    loop forever:
        check if context cancelled → exit
        for each queue:
            XREADGROUP (blocks up to 2 seconds)
            if got a task → process it
            if no task → try next queue
```

**Retry logic:**

```
Task fails
  │
  ├── retried < maxRetry?
  │     YES → increment retried count
  │           ACK the original message (remove from pending)
  │           sleep(2^retried seconds)    ← exponential backoff
  │           re-enqueue the task (goes back into the stream)
  │
  │     NO  → set state to "failed"
  │           ACK the original message
  │           send to dead-letter queue
```

**Why ACK before re-enqueue?** If we don't ACK the original message, it stays in the pending list. When we re-enqueue, there are now TWO copies — the pending one and the new one. ACKing first ensures exactly one copy exists at any time.

**Graceful shutdown sequence:**
1. `SIGINT` or `SIGTERM` received
2. Context cancelled → all `loop()` goroutines see `ctx.Done()` and stop pulling new tasks
3. In-flight tasks (already being processed) run to completion
4. `wg.Wait()` returns once all goroutines exit
5. `Run()` returns cleanly

---

### 4. Client (`internal/queue/client.go`)

The producer-side SDK. Thin wrapper over the Broker — gives callers a clean API without exposing Redis internals.

```go
client, _ := queue.NewClient("localhost:6379")
task, _ := client.Enqueue(ctx, "email:send", EmailPayload{...}, task.WithMaxRetry(5))
client.Close()
```

This is intentionally minimal. The client doesn't need to know about consumer groups, streams, or message IDs. It just puts tasks into the queue.

---

### 5. Example (`cmd/example/main.go`)

A self-contained demo that acts as both producer and consumer in a single binary:

1. Creates a client and enqueues 5 email tasks
2. Creates a worker with 3 goroutines
3. Registers a handler for `"email:send"` that simulates sending an email (500ms sleep)
4. Runs the worker until Ctrl+C

---

## Go Patterns Used

### Functional Options
Both `task.New()` and `worker.New()` use this pattern. Options are closures that modify the struct after it's initialized with defaults. See `task.Option` and `worker.Option`.

### Context Propagation
Every function that does I/O takes `context.Context` as its first parameter. This enables:
- Timeout propagation (the broker connection timeout)
- Cancellation (graceful shutdown cancels the context, all goroutines exit)

### Structured Logging (`log/slog`)
All log lines include key-value pairs (`"task_id", t.ID`), not string formatting. This makes logs machine-parseable for future metrics/alerting.

### Error Wrapping (`fmt.Errorf("...: %w", err)`)
Errors are wrapped with context at each layer. `%w` preserves the original error so callers can use `errors.Is()` or `errors.As()` to inspect it.

---

## File Inventory

```
taskqueue/
├── go.mod                           # Module: github.com/kapiljain/taskqueue
├── go.sum                           # Dependency checksums
├── cmd/
│   └── example/
│       └── main.go                  # Demo: enqueue 5 tasks + process them
└── internal/
    ├── task/
    │   ├── task.go                  # Task model, state machine, encode/decode
    │   └── task_test.go             # Unit tests (3 tests, all passing)
    ├── broker/
    │   └── broker.go                # Redis Streams: enqueue, dequeue, ack, dead-letter
    ├── worker/
    │   └── worker.go                # Worker pool, retry with backoff, graceful shutdown
    └── queue/
        └── client.go                # Producer-side SDK
```

**Dependencies:** `github.com/redis/go-redis/v9` (Redis client)

---

## How to Run

```bash
# Prerequisites: Redis running locally
brew install redis && redis-server

# Run the example
cd ~/claude/taskqueue
go run cmd/example/main.go

# Run tests
go test ./...

# Custom Redis address
REDIS_ADDR=localhost:6380 go run cmd/example/main.go
```

---

## What's NOT Built Yet (Future Phases)

| Feature | Phase |
|---------|-------|
| Exactly-once delivery (XCLAIM for stuck messages) | Phase 2 |
| Task deduplication (idempotency keys) | Phase 2 |
| Heartbeats (detect stuck workers) | Phase 2 |
| Delayed/scheduled tasks | Phase 3 |
| Priority queues | Phase 3 |
| Rate limiting per queue | Phase 3 |
| Postgres persistence + task history | Phase 4 |
| Prometheus metrics | Phase 4 |
| REST API + web dashboard | Phase 4 |
| Task chaining / DAG workflows | Phase 5 |
