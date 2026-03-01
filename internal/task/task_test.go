package task

import (
	"testing"
)

func TestNewTask(t *testing.T) {
	payload := map[string]string{"key": "value"}
	tk, err := New("test:task", payload, WithMaxRetry(5), WithQueue("critical"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tk.Type != "test:task" {
		t.Errorf("type = %q, want %q", tk.Type, "test:task")
	}
	if tk.Queue != "critical" {
		t.Errorf("queue = %q, want %q", tk.Queue, "critical")
	}
	if tk.MaxRetry != 5 {
		t.Errorf("max_retry = %d, want %d", tk.MaxRetry, 5)
	}
	if tk.State != StatePending {
		t.Errorf("state = %q, want %q", tk.State, StatePending)
	}
	if tk.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original, err := New("email:send", map[string]string{"to": "test@example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := original.Encode()
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}

	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Type != original.Type {
		t.Errorf("Type mismatch: got %q, want %q", decoded.Type, original.Type)
	}
	if decoded.Queue != original.Queue {
		t.Errorf("Queue mismatch: got %q, want %q", decoded.Queue, original.Queue)
	}
}

func TestDefaultValues(t *testing.T) {
	tk, err := New("simple", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tk.Queue != "default" {
		t.Errorf("default queue = %q, want %q", tk.Queue, "default")
	}
	if tk.MaxRetry != 3 {
		t.Errorf("default max_retry = %d, want %d", tk.MaxRetry, 3)
	}
}
