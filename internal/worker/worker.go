package worker

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/kapiljain/taskqueue/internal/broker"
	"github.com/kapiljain/taskqueue/internal/task"
)

// Handler processes a task. Return an error to trigger a retry.
type Handler func(ctx context.Context, t *task.Task) error

// Worker consumes tasks from queues and processes them.
type Worker struct {
	broker     *broker.Broker
	handlers   map[string]Handler
	queues     []string
	id         string
	numWorkers int
	logger     *slog.Logger
}

// Option configures a Worker.
type Option func(*Worker)

// WithConcurrency sets how many tasks to process in parallel.
func WithConcurrency(n int) Option {
	return func(w *Worker) {
		w.numWorkers = n
	}
}

// WithQueues sets which queues this worker listens on.
func WithQueues(queues ...string) Option {
	return func(w *Worker) {
		w.queues = queues
	}
}

// WithLogger sets a custom logger.
func WithLogger(l *slog.Logger) Option {
	return func(w *Worker) {
		w.logger = l
	}
}

// New creates a new Worker.
func New(b *broker.Broker, opts ...Option) *Worker {
	hostname, _ := os.Hostname()

	w := &Worker{
		broker:     b,
		handlers:   make(map[string]Handler),
		queues:     []string{"default"},
		id:         fmt.Sprintf("%s-%d", hostname, os.Getpid()),
		numWorkers: 5,
		logger:     slog.Default(),
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// Handle registers a handler for a task type.
func (w *Worker) Handle(taskType string, h Handler) {
	w.handlers[taskType] = h
}

// Run starts the worker pool and blocks until interrupted.
func (w *Worker) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Ensure consumer groups exist for all queues.
	for _, q := range w.queues {
		if err := w.broker.EnsureGroup(ctx, q); err != nil {
			return fmt.Errorf("ensure group for queue %q: %w", q, err)
		}
	}

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup

	w.logger.Info("starting worker",
		"id", w.id,
		"concurrency", w.numWorkers,
		"queues", w.queues,
	)

	// Launch worker goroutines.
	for i := 0; i < w.numWorkers; i++ {
		wg.Add(1)
		go func(workerNum int) {
			defer wg.Done()
			consumerID := fmt.Sprintf("%s-%d", w.id, workerNum)
			w.loop(ctx, consumerID)
		}(i)
	}

	// Wait for shutdown signal.
	sig := <-sigCh
	w.logger.Info("received signal, shutting down", "signal", sig)
	cancel()

	// Wait for in-flight tasks to complete.
	wg.Wait()
	w.logger.Info("all workers stopped")

	return nil
}

func (w *Worker) loop(ctx context.Context, consumerID string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		for _, queue := range w.queues {
			t, msgID, err := w.broker.Dequeue(ctx, queue, consumerID, 2*time.Second)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				w.logger.Error("dequeue error", "queue", queue, "error", err)
				time.Sleep(time.Second)
				continue
			}
			if t == nil {
				continue
			}

			w.process(ctx, t, queue, msgID)
		}
	}
}

func (w *Worker) process(ctx context.Context, t *task.Task, queue, msgID string) {
	handler, ok := w.handlers[t.Type]
	if !ok {
		w.logger.Error("no handler registered", "type", t.Type, "task_id", t.ID)
		w.broker.Ack(ctx, queue, msgID)
		return
	}

	w.logger.Info("processing task", "type", t.Type, "task_id", t.ID, "attempt", t.Retried+1)

	err := handler(ctx, t)
	if err != nil {
		w.handleFailure(ctx, t, queue, msgID, err)
		return
	}

	// Success — acknowledge and remove from pending.
	if ackErr := w.broker.Ack(ctx, queue, msgID); ackErr != nil {
		w.logger.Error("ack failed", "task_id", t.ID, "error", ackErr)
	}

	w.logger.Info("task completed", "type", t.Type, "task_id", t.ID)
}

func (w *Worker) handleFailure(ctx context.Context, t *task.Task, queue, msgID string, taskErr error) {
	t.Retried++
	t.LastError = taskErr.Error()

	w.logger.Warn("task failed",
		"type", t.Type,
		"task_id", t.ID,
		"attempt", t.Retried,
		"max_retry", t.MaxRetry,
		"error", taskErr,
	)

	// Acknowledge the original message regardless — we'll re-enqueue if retrying.
	w.broker.Ack(ctx, queue, msgID)

	if t.Retried >= t.MaxRetry {
		// Exhausted retries — move to dead-letter queue.
		t.State = task.StateFailed
		w.logger.Error("task moved to dead letter queue", "type", t.Type, "task_id", t.ID)
		if err := w.broker.SendToDeadLetter(ctx, t); err != nil {
			w.logger.Error("failed to send to dead letter", "task_id", t.ID, "error", err)
		}
		return
	}

	// Exponential backoff before re-enqueue.
	t.State = task.StateRetry
	backoff := time.Duration(math.Pow(2, float64(t.Retried))) * time.Second
	w.logger.Info("retrying task", "task_id", t.ID, "backoff", backoff)

	time.Sleep(backoff)

	if err := w.broker.Enqueue(ctx, t); err != nil {
		w.logger.Error("retry enqueue failed", "task_id", t.ID, "error", err)
	}
}
