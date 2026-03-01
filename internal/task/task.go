package task

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// State represents the lifecycle state of a task.
type State string

const (
	StatePending   State = "pending"
	StateActive    State = "active"
	StateRetry     State = "retry"
	StateFailed    State = "failed"
	StateCompleted State = "completed"
)

// Task represents a unit of work to be processed by a worker.
type Task struct {
	ID        string          `json:"id"`
	Queue     string          `json:"queue"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	State     State           `json:"state"`
	Retried   int             `json:"retried"`
	MaxRetry  int             `json:"max_retry"`
	CreatedAt time.Time       `json:"created_at"`
	LastError string          `json:"last_error,omitempty"`
}

// Option configures a task.
type Option func(*Task)

// WithMaxRetry sets the maximum number of retries for a task.
func WithMaxRetry(n int) Option {
	return func(t *Task) {
		t.MaxRetry = n
	}
}

// WithQueue sets the queue name for a task.
func WithQueue(name string) Option {
	return func(t *Task) {
		t.Queue = name
	}
}

// New creates a new task with the given type and payload.
func New(taskType string, payload any, opts ...Option) (*Task, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	t := &Task{
		ID:        generateID(),
		Queue:     "default",
		Type:      taskType,
		Payload:   data,
		State:     StatePending,
		MaxRetry:  3,
		CreatedAt: time.Now(),
	}

	for _, opt := range opts {
		opt(t)
	}

	return t, nil
}

// Encode serializes the task to JSON bytes.
func (t *Task) Encode() ([]byte, error) {
	return json.Marshal(t)
}

// Decode deserializes a task from JSON bytes.
func Decode(data []byte) (*Task, error) {
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
