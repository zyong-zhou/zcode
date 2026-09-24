package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.openai.com/v1"

// StatusError reports a non-2xx HTTP response from the model backend. It is a
// distinct type so retry logic can classify transient failures.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("llm: status %d: %s", e.Code, strings.TrimSpace(e.Body))
}

// Retryable reports whether the status is worth retrying (rate limit or 5xx).
func (e *StatusError) Retryable() bool {
	return e.Code == http.StatusTooManyRequests || e.Code >= 500
}

// OpenAIClient talks to any OpenAI-compatible /chat/completions endpoint.
type OpenAIClient struct {
	BaseURL string
	APIKey  string
	Model   string
	// HTTP is optional; a 120s-timeout client is used when nil.
	HTTP *http.Client
}

// NewOpenAIClient returns a client. An empty baseURL falls back to the public
// OpenAI API. Trailing slashes are trimmed so path joining is predictable.
func NewOpenAIClient(baseURL, apiKey, model string) *OpenAIClient {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &OpenAIClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// SetModel changes the model used for subsequent requests.
func (c *OpenAIClient) SetModel(model string) {
	c.Model = strings.TrimSpace(model)
}

// Chat implements Client.
func (c *OpenAIClient) Chat(ctx context.Context, req Request) (Response, error) {
	raw, err := json.Marshal(c.buildBody(req))
	if err != nil {
		return Response{}, fmt.Errorf("llm: marshal request: %w", err)
	}

	resp, err := c.do(ctx, raw)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("llm: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, &StatusError{Code: resp.StatusCode, Body: string(data)}
	}

	var parsed wireResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("llm: api error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("llm: response has no choices")
	}

	out := Response{Content: parsed.Choices[0].Message.Content}
	if parsed.Usage != nil {
		out.Usage = parsed.Usage.toUsage()
	}
	for _, tc := range parsed.Choices[0].Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return out, nil
}

// ChatStream implements StreamClient using server-sent events. Text fragments
// are passed to onDelta as they arrive; the fully assembled Response is
// returned once the stream ends.
func (c *OpenAIClient) ChatStream(ctx context.Context, req Request, onDelta func(string)) (Response, error) {
	body := c.buildBody(req)
	body.Stream = true
	body.StreamOptions = &wireStreamOptions{IncludeUsage: true}

	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("llm: marshal request: %w", err)
	}

	resp, err := c.do(ctx, raw)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return Response{}, &StatusError{Code: resp.StatusCode, Body: string(data)}
	}
	return parseSSE(resp.Body, onDelta)
}

func (c *OpenAIClient) buildBody(req Request) wireRequest {
	body := wireRequest{Model: c.Model}
	for _, m := range req.Messages {
		wm := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, toWireToolCall(tc))
		}
		body.Messages = append(body.Messages, wm)
	}
	for _, t := range req.Tools {
		var wt wireTool
		wt.Type = "function"
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Parameters
		body.Tools = append(body.Tools, wt)
	}
	return body
}

func (c *OpenAIClient) do(ctx context.Context, raw []byte) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("llm: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: do request: %w", err)
	}
	return resp, nil
}

func (c *OpenAIClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 120 * time.Second}
}

// parseSSE consumes an OpenAI-style event stream, forwarding content deltas and
// reassembling tool calls that arrive in fragments.
func parseSSE(r io.Reader, onDelta func(string)) (Response, error) {
	type acc struct {
		id   string
		name string
		args strings.Builder
	}
	tools := map[int]*acc{}
	var order []int

	var out Response
	var content strings.Builder

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk wireStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			out.Usage = chunk.Usage.toUsage()
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			content.WriteString(delta.Content)
			if onDelta != nil {
				onDelta(delta.Content)
			}
		}
		for _, tc := range delta.ToolCalls {
			a := tools[tc.Index]
			if a == nil {
				a = &acc{}
				tools[tc.Index] = a
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				a.id = tc.ID
			}
			if tc.Function.Name != "" {
				a.name = tc.Function.Name
			}
			a.args.WriteString(tc.Function.Arguments)
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("llm: stream: %w", err)
	}

	out.Content = content.String()
	for _, i := range order {
		a := tools[i]
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: a.id, Name: a.name, Arguments: a.args.String()})
	}
	return out, nil
}

func toWireToolCall(tc ToolCall) wireToolCall {
	var w wireToolCall
	w.ID = tc.ID
	w.Type = "function"
	w.Function.Name = tc.Name
	w.Function.Arguments = tc.Arguments
	return w
}

// The wire types below mirror the OpenAI JSON schema. They are kept private so
// the public model stays provider-neutral.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireRequest struct {
	Model         string             `json:"model"`
	Messages      []wireMessage      `json:"messages"`
	Tools         []wireTool         `json:"tools,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	StreamOptions *wireStreamOptions `json:"stream_options,omitempty"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u wireUsage) toUsage() Usage {
	return Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
}

type wireResponse struct {
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type wireStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string               `json:"content"`
			ToolCalls []wireStreamToolCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
}

type wireStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
