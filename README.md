# TaskQueue

A distributed task queue written in Go, backed by Redis Streams. Enqueue work from any part of your application and have a pool of workers process it asynchronously with automatic retries and failure handling.

## Features

- **Redis Streams** — reliable message delivery with consumer groups
- **Concurrent workers** — configurable goroutine pool (default: 5)
- **Automatic retries** — exponential backoff with configurable max retries
- **Dead-letter queue** — failed tasks are preserved for inspection
- **Graceful shutdown** — in-flight tasks complete before exit (SIGINT/SIGTERM)
- **Structured logging** — `log/slog` with key-value pairs

## Quick Start

### Prerequisites

```bash
# Redis running locally
brew install redis && redis-server
```

### Run the Example

```bash
go run cmd/example/main.go
```

This enqueues 5 email tasks and processes them with 3 workers. Press `Ctrl+C` to stop.

### Use a Custom Redis Address

```bash
REDIS_ADDR=localhost:6380 go run cmd/example/main.go
```

## Usage

### Producer

```go
client, err := queue.NewClient("localhost:6379")
if err != nil {
    log.Fatal(err)
}
defer client.Close()

t, err := client.Enqueue(ctx, "email:send", EmailPayload{
    To:      "user@example.com",
    Subject: "Welcome!",
    Body:    "Thanks for signing up.",
}, task.WithMaxRetry(5))
```

### Consumer

```go
b, err := broker.New("localhost:6379")
if err != nil {
    log.Fatal(err)
}
defer b.Close()

w := worker.New(b,
    worker.WithConcurrency(3),
    worker.WithQueues("default", "critical"),
)

w.Handle("email:send", func(ctx context.Context, t *task.Task) error {
    var p EmailPayload
    json.Unmarshal(t.Payload, &p)
    return sendEmail(p)
})

w.Run() // blocks until SIGINT/SIGTERM
```

## Architecture

```
Producer ──XADD──▶ Redis Stream ◀──XREADGROUP── Worker Pool
                        │                            │
                        │◀──────────XACK─────────────┘
                        │
                   Dead Letter
```

1. Producer calls `client.Enqueue()` → `XADD` to Redis Stream
2. Worker goroutines call `XREADGROUP` → Redis delivers each message to one consumer
3. On success → `XACK` removes the message from the pending list
4. On failure → retry with exponential backoff, or move to dead-letter queue after max retries

## Project Structure

```
taskqueue/
├── cmd/example/main.go          # Demo: enqueue + process tasks
├── docs/phase1-design.md        # Detailed design document
└── internal/
    ├── task/task.go              # Task model, state machine, encode/decode
    ├── task/task_test.go         # Unit tests
    ├── broker/broker.go          # Redis Streams communication layer
    ├── worker/worker.go          # Worker pool, retry logic, graceful shutdown
    └── queue/client.go           # Producer-side SDK
```

## Testing

```bash
go test ./...
```

## Design

See [docs/phase1-design.md](docs/phase1-design.md) for the full design document covering architecture decisions, retry semantics, and the Redis key scheme.

## Roadmap

| Phase | Features |
|-------|----------|
| 1 (current) | Core queue, workers, retries, dead-letter |
| 2 | Exactly-once delivery, deduplication, heartbeats |
| 3 | Delayed/scheduled tasks, priority queues, rate limiting |
| 4 | Postgres persistence, Prometheus metrics, web dashboard |
| 5 | Task chaining / DAG workflows |

## License

MIT
