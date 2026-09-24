package llm

import (
	"context"
	"testing"
	"time"
)

type flakyClient struct {
	failures int
	calls    int
	status   int
}

func (f *flakyClient) Chat(context.Context, Request) (Response, error) {
	f.calls++
	if f.calls <= f.failures {
		return Response{}, &StatusError{Code: f.status, Body: "boom"}
	}
	return Response{Content: "ok"}, nil
}

func TestRetryRetriesTransient(t *testing.T) {
	f := &flakyClient{failures: 2, status: 500}
	c := RetryClient{Inner: f, Attempts: 3, Base: time.Millisecond}

	resp, err := c.Chat(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want ok", resp.Content)
	}
	if f.calls != 3 {
		t.Errorf("calls = %d, want 3", f.calls)
	}
}

func TestRetryStopsOnClientError(t *testing.T) {
	f := &flakyClient{failures: 5, status: 400}
	c := RetryClient{Inner: f, Attempts: 3, Base: time.Millisecond}

	if _, err := c.Chat(context.Background(), Request{}); err == nil {
		t.Fatal("expected an error")
	}
	if f.calls != 1 {
		t.Errorf("calls = %d, want 1 (400 is not retryable)", f.calls)
	}
}

func TestRetryGivesUpAfterAttempts(t *testing.T) {
	f := &flakyClient{failures: 10, status: 503}
	c := RetryClient{Inner: f, Attempts: 2, Base: time.Millisecond}

	if _, err := c.Chat(context.Background(), Request{}); err == nil {
		t.Fatal("expected an error")
	}
	if f.calls != 2 {
		t.Errorf("calls = %d, want 2", f.calls)
	}
}

// streamPartial emits a delta and then fails, to prove streaming is not retried
// once output has started.
type streamPartial struct{ calls int }

func (streamPartial) Chat(context.Context, Request) (Response, error) {
	return Response{}, nil
}

func (s *streamPartial) ChatStream(_ context.Context, _ Request, onDelta func(string)) (Response, error) {
	s.calls++
	onDelta("partial")
	return Response{}, &StatusError{Code: 503}
}

func TestRetryDoesNotRetryAfterDelta(t *testing.T) {
	s := &streamPartial{}
	c := RetryClient{Inner: s, Attempts: 3, Base: time.Millisecond}

	if _, err := c.ChatStream(context.Background(), Request{}, func(string) {}); err == nil {
		t.Fatal("expected an error")
	}
	if s.calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry after output)", s.calls)
	}
}
