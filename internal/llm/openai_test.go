package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// A large upstream error body must not be echoed in full into logs, traces and
// the terminal; the field keeps the original text for programmatic callers.
func TestStatusErrorTruncatesBody(t *testing.T) {
	err := &StatusError{Code: 500, Body: strings.Repeat("x", 64<<10)}
	msg := err.Error()
	if len(msg) > errorBodyLimit+128 {
		t.Errorf("Error() returned %d bytes, want it capped near %d", len(msg), errorBodyLimit)
	}
	if !strings.Contains(msg, "bytes omitted") {
		t.Errorf("want an omission marker, got %q", msg)
	}
	if err.Body == "" {
		t.Error("Body must keep the full upstream text")
	}

	// Truncation must not split a multi-byte rune.
	msg = (&StatusError{Code: 500, Body: strings.Repeat("中", 4096)}).Error()
	if !utf8.ValidString(msg) {
		t.Error("Error() produced invalid UTF-8")
	}
}

func TestOpenAIClientChat(t *testing.T) {
	var got wireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("auth = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{
			"content":"ok",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]
		}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "test-key", "test-model")
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools:    []ToolSpec{{Name: "read_file", Description: "read", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want ok", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "read_file" || resp.ToolCalls[0].ID != "call_1" {
		t.Errorf("tool call = %+v", resp.ToolCalls[0])
	}
	if resp.Usage != (Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if got.Model != "test-model" || len(got.Tools) != 1 || got.Tools[0].Function.Name != "read_file" {
		t.Errorf("sent request = %+v", got)
	}
}

func TestOpenAIClientErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "k", "m")
	if _, err := c.Chat(context.Background(), Request{}); err == nil {
		t.Fatal("expected error on non-2xx status")
	}
}

func TestOpenAIClientChatStream(t *testing.T) {
	var got wireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = io.WriteString(w, s)
			if fl != nil {
				fl.Flush()
			}
		}
		write("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		write("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		write("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"pa\"}}]}}]}\n\n")
		write("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"th\\\":\\\"a.go\\\"}\"}}]}}]}\n\n")
		write("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":9,\"total_tokens\":16}}\n\n")
		write("data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "k", "m")
	var deltas []string
	resp, err := c.ChatStream(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	if resp.Content != "Hello" {
		t.Errorf("content = %q, want Hello", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if tc := resp.ToolCalls[0]; tc.ID != "c1" || tc.Name != "read_file" || tc.Arguments != `{"path":"a.go"}` {
		t.Errorf("tool call = %+v", tc)
	}
	if resp.Usage != (Usage{PromptTokens: 7, CompletionTokens: 9, TotalTokens: 16}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if joined := strings.Join(deltas, ""); joined != "Hello" {
		t.Errorf("deltas = %v (joined %q)", deltas, joined)
	}
	if !got.Stream || got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
		t.Errorf("request should stream with usage: %+v", got)
	}
}

func TestOpenAIClientSetModel(t *testing.T) {
	var got wireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "k", "old-model")
	if c.Model != "old-model" {
		t.Fatalf("initial model = %q", c.Model)
	}
	c.SetModel(" new-model ")
	if c.Model != "new-model" {
		t.Errorf("model = %q, want trimmed new-model", c.Model)
	}
	if _, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got.Model != "new-model" {
		t.Errorf("request model = %q, want new-model", got.Model)
	}
}
