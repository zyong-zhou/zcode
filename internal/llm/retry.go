package llm

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

// RetryClient wraps a Client with bounded retries for transient failures
// (rate limits, 5xx, and transport errors). It implements both Client and
// StreamClient, so it can be dropped in wherever a Client is expected.
type RetryClient struct {
	Inner    Client
	Attempts int           // total tries; defaults to 3 when <= 0
	Base     time.Duration // first backoff; defaults to 500ms when <= 0
}

func (c RetryClient) attempts() int {
	if c.Attempts <= 0 {
		return 3
	}
	return c.Attempts
}

func (c RetryClient) base() time.Duration {
	if c.Base <= 0 {
		return 500 * time.Millisecond
	}
	return c.Base
}

// Chat implements Client with retries.
func (c RetryClient) Chat(ctx context.Context, req Request) (Response, error) {
	var last error
	for i := 0; i < c.attempts(); i++ {
		if i > 0 {
			if err := sleep(ctx, backoff(c.base(), i)); err != nil {
				return Response{}, err
			}
		}
		resp, err := c.Inner.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		last = err
		if !retryable(err) {
			return Response{}, err
		}
	}
	return Response{}, last
}

// ChatStream implements StreamClient. It only retries when nothing has been
// streamed yet: once a delta has reached the caller, retrying would duplicate
// output.
func (c RetryClient) ChatStream(ctx context.Context, req Request, onDelta func(string)) (Response, error) {
	sc, ok := c.Inner.(StreamClient)
	if !ok {
		return c.Chat(ctx, req)
	}
	var last error
	for i := 0; i < c.attempts(); i++ {
		if i > 0 {
			if err := sleep(ctx, backoff(c.base(), i)); err != nil {
				return Response{}, err
			}
		}
		started := false
		wrapped := func(s string) {
			started = true
			if onDelta != nil {
				onDelta(s)
			}
		}
		resp, err := sc.ChatStream(ctx, req, wrapped)
		if err == nil {
			return resp, nil
		}
		last = err
		if started || !retryable(err) {
			return Response{}, err
		}
	}
	return Response{}, last
}

// SetModel forwards a model change to the wrapped client when it supports it.
func (c RetryClient) SetModel(model string) {
	if ms, ok := c.Inner.(ModelSetter); ok {
		ms.SetModel(model)
	}
}

// retryable reports whether err is a transient failure worth another attempt.
// Context cancellation and deadline expiry are never retried.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Retryable()
	}
	return true // transport-level failure
}

// backoff returns the delay before retry attempt i (1-based) with jitter.
func backoff(base time.Duration, i int) time.Duration {
	d := base << (i - 1)
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	if d <= 0 {
		return d
	}
	jitter := time.Duration(rand.Int63n(int64(d/5 + 1)))
	return d - d/10 + jitter
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
