package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/kapiljain/taskqueue/internal/broker"
	"github.com/kapiljain/taskqueue/internal/queue"
	"github.com/kapiljain/taskqueue/internal/task"
	"github.com/kapiljain/taskqueue/internal/worker"
)

type EmailPayload struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func main() {
	redisAddr := "localhost:6379"
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		redisAddr = addr
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// --- Producer: enqueue some tasks ---
	client, err := queue.NewClient(redisAddr)
	if err != nil {
		logger.Error("failed to create client", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	for i := 0; i < 5; i++ {
		t, err := client.Enqueue(ctx, "email:send", EmailPayload{
			To:      fmt.Sprintf("user%d@example.com", i),
			Subject: "Welcome!",
			Body:    "Thanks for signing up.",
		}, task.WithMaxRetry(3))
		if err != nil {
			logger.Error("enqueue failed", "error", err)
			continue
		}
		logger.Info("enqueued task", "id", t.ID, "type", t.Type)
	}

	client.Close()

	// --- Consumer: process tasks ---
	b, err := broker.New(redisAddr)
	if err != nil {
		logger.Error("failed to connect broker", "error", err)
		os.Exit(1)
	}
	defer b.Close()

	w := worker.New(b,
		worker.WithConcurrency(3),
		worker.WithQueues("default"),
		worker.WithLogger(logger),
	)

	// Register a handler for "email:send" tasks.
	w.Handle("email:send", func(ctx context.Context, t *task.Task) error {
		var payload EmailPayload
		if err := json.Unmarshal(t.Payload, &payload); err != nil {
			return err
		}

		// Simulate sending an email.
		logger.Info("sending email",
			"to", payload.To,
			"subject", payload.Subject,
		)
		time.Sleep(500 * time.Millisecond) // simulate work

		return nil
	})

	logger.Info("starting worker, press Ctrl+C to stop")
	if err := w.Run(); err != nil {
		logger.Error("worker error", "error", err)
		os.Exit(1)
	}
}
