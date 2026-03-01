package queue

import (
	"context"

	"github.com/kapiljain/taskqueue/internal/broker"
	"github.com/kapiljain/taskqueue/internal/task"
)

// Client is the producer-side API for enqueuing tasks.
type Client struct {
	broker *broker.Broker
}

// NewClient creates a new client connected to the task queue.
func NewClient(redisAddr string) (*Client, error) {
	b, err := broker.New(redisAddr)
	if err != nil {
		return nil, err
	}
	return &Client{broker: b}, nil
}

// Enqueue adds a task to the queue for processing.
func (c *Client) Enqueue(ctx context.Context, taskType string, payload any, opts ...task.Option) (*task.Task, error) {
	t, err := task.New(taskType, payload, opts...)
	if err != nil {
		return nil, err
	}

	if err := c.broker.Enqueue(ctx, t); err != nil {
		return nil, err
	}

	return t, nil
}

// Close releases the client's resources.
func (c *Client) Close() error {
	return c.broker.Close()
}
