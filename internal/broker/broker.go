package broker

import (
	"context"
	"fmt"
	"time"

	"github.com/kapiljain/taskqueue/internal/task"
	"github.com/redis/go-redis/v9"
)

const (
	// Redis key prefixes
	streamPrefix  = "taskqueue:stream:"
	groupName     = "taskqueue-workers"
	deadLetterKey = "taskqueue:dead_letter"
)

// Broker manages task enqueueing and dequeueing via Redis Streams.
type Broker struct {
	rdb *redis.Client
}

// New creates a new Broker connected to Redis.
func New(addr string) (*Broker, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connect: %w", err)
	}

	return &Broker{rdb: rdb}, nil
}

// Enqueue adds a task to the appropriate Redis Stream.
func (b *Broker) Enqueue(ctx context.Context, t *task.Task) error {
	data, err := t.Encode()
	if err != nil {
		return fmt.Errorf("encode task: %w", err)
	}

	streamKey := streamPrefix + t.Queue

	_, err = b.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{
			"task": string(data),
		},
	}).Result()
	if err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}

	return nil
}

// EnsureGroup creates the consumer group for a queue if it doesn't exist.
func (b *Broker) EnsureGroup(ctx context.Context, queue string) error {
	streamKey := streamPrefix + queue
	err := b.rdb.XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

// Dequeue reads the next available task from the queue using consumer groups.
// It blocks for up to `timeout` waiting for new messages.
func (b *Broker) Dequeue(ctx context.Context, queue, consumerID string, timeout time.Duration) (*task.Task, string, error) {
	streamKey := streamPrefix + queue

	results, err := b.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: consumerID,
		Streams:  []string{streamKey, ">"},
		Count:    1,
		Block:    timeout,
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, "", nil // no messages available
		}
		return nil, "", fmt.Errorf("dequeue: %w", err)
	}

	if len(results) == 0 || len(results[0].Messages) == 0 {
		return nil, "", nil
	}

	msg := results[0].Messages[0]
	data, ok := msg.Values["task"].(string)
	if !ok {
		return nil, "", fmt.Errorf("invalid task data in message %s", msg.ID)
	}

	t, err := task.Decode([]byte(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode task: %w", err)
	}

	return t, msg.ID, nil
}

// Ack acknowledges a processed message, removing it from the pending list.
func (b *Broker) Ack(ctx context.Context, queue, msgID string) error {
	streamKey := streamPrefix + queue
	return b.rdb.XAck(ctx, streamKey, groupName, msgID).Err()
}

// SendToDeadLetter moves a permanently failed task to the dead-letter queue.
func (b *Broker) SendToDeadLetter(ctx context.Context, t *task.Task) error {
	data, err := t.Encode()
	if err != nil {
		return err
	}

	return b.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: deadLetterKey,
		Values: map[string]interface{}{
			"task": string(data),
		},
	}).Err()
}

// Close closes the Redis connection.
func (b *Broker) Close() error {
	return b.rdb.Close()
}
