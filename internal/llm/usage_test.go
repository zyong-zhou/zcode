package llm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUsageIsZeroAndAdd(t *testing.T) {
	if !(Usage{}).IsZero() {
		t.Error("zero Usage should report IsZero")
	}
	if (Usage{PromptTokens: 1}).IsZero() {
		t.Error("non-zero Usage should not report IsZero")
	}
	got := Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}.
		Add(Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30})
	if want := (Usage{PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33}); got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
}

func TestStatusError(t *testing.T) {
	e := &StatusError{Code: 429, Body: " slow down\n"}
	if s := e.Error(); !strings.Contains(s, "429") || !strings.Contains(s, "slow down") {
		t.Errorf("Error() = %q, want code and trimmed body", s)
	}
	if strings.HasSuffix(e.Error(), "\n") {
		t.Errorf("Error() = %q, want the trailing newline trimmed", e.Error())
	}

	cases := []struct {
		code int
		want bool
	}{
		{200, false}, {400, false}, {404, false}, {429, true}, {500, true}, {503, true},
	}
	for _, tc := range cases {
		if got := (&StatusError{Code: tc.code}).Retryable(); got != tc.want {
			t.Errorf("Retryable(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestBuildBodyIncludesToolCallsAndTools(t *testing.T) {
	c := &OpenAIClient{Model: "m"}
	body := c.buildBody(Request{
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Name: "read_file", Arguments: `{"path":"a"}`}}},
			{Role: "tool", ToolCallID: "1", Content: "ok"},
		},
		Tools: []ToolSpec{{Name: "read_file", Description: "read", Parameters: map[string]any{"type": "object"}}},
	})
	if len(body.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(body.Messages))
	}
	tc := body.Messages[0].ToolCalls
	if len(tc) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(tc))
	}
	if tc[0].ID != "1" || tc[0].Type != "function" || tc[0].Function.Name != "read_file" || tc[0].Function.Arguments != `{"path":"a"}` {
		t.Errorf("wire tool call = %+v", tc[0])
	}
	if body.Messages[1].ToolCallID != "1" {
		t.Errorf("tool_call_id = %q, want 1", body.Messages[1].ToolCallID)
	}
	if len(body.Tools) != 1 || body.Tools[0].Function.Name != "read_file" || body.Tools[0].Function.Parameters["type"] != "object" {
		t.Errorf("wire tools = %+v", body.Tools)
	}
}

func TestHTTPClientDefaultAndOverride(t *testing.T) {
	c := &OpenAIClient{}
	if got := c.httpClient(); got.Timeout != 120*time.Second {
		t.Errorf("default timeout = %v, want 120s", got.Timeout)
	}
	custom := &http.Client{}
	c.HTTP = custom
	if c.httpClient() != custom {
		t.Error("httpClient should return the configured client")
	}
}

func TestRetryAttemptsAndBaseDefaults(t *testing.T) {
	c := RetryClient{}
	if c.attempts() != 3 {
		t.Errorf("attempts() = %d, want 3", c.attempts())
	}
	if c.base() != 500*time.Millisecond {
		t.Errorf("base() = %v, want 500ms", c.base())
	}
	c = RetryClient{Attempts: 5, Base: time.Second}
	if c.attempts() != 5 || c.base() != time.Second {
		t.Errorf("explicit attempts/base = %d/%v", c.attempts(), c.base())
	}
}

func TestRetryableClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"429", &StatusError{Code: 429}, true},
		{"500", &StatusError{Code: 500}, true},
		{"400", &StatusError{Code: 400}, false},
		{"transport", errors.New("connection reset"), true},
	}
	for _, tc := range cases {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("retryable(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A wrapped cancellation is still not retried.
	wrapped := errors.Join(errors.New("wrap"), context.Canceled)
	if retryable(wrapped) {
		t.Error("wrapped context.Canceled should not be retryable")
	}
}

func TestRetryStreamFallsBackToChat(t *testing.T) {
	// A client without StreamClient should have ChatStream delegate to Chat.
	f := &flakyClient{}
	c := RetryClient{Inner: f, Attempts: 1, Base: time.Millisecond}
	resp, err := c.ChatStream(context.Background(), Request{}, func(string) {})
	if err != nil || resp.Content != "ok" {
		t.Fatalf("ChatStream fallback = (%+v, %v)", resp, err)
	}
}
