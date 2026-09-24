// Package llm defines the model-backend boundary for the agent.
//
// The single Client interface is the seam used to swap providers and to inject
// fakes in tests. OpenAIClient is the production implementation and speaks the
// OpenAI-compatible /chat/completions wire format.
package llm

import "context"

// Message is one entry in the chat transcript.
type Message struct {
	// Role is "system", "user", "assistant", or "tool".
	Role string `json:"role"`
	// Content is the text payload. It may be empty for an assistant message
	// that only requests tool calls.
	Content string `json:"content,omitempty"`
	// ToolCalls is set on assistant messages that request tool execution.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a "tool" message back to the assistant ToolCall it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolSpec advertises a callable tool to the model.
type ToolSpec struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object describing the arguments.
	Parameters map[string]any
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON object, exactly as produced by the model
}

// Request is a single chat completion request.
type Request struct {
	Messages []Message
	Tools    []ToolSpec
}

// Usage reports token consumption for a single request.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// IsZero reports whether no token counts were provided.
func (u Usage) IsZero() bool { return u == Usage{} }

// Add returns the component-wise sum, used to accumulate a run total.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + o.PromptTokens,
		CompletionTokens: u.CompletionTokens + o.CompletionTokens,
		TotalTokens:      u.TotalTokens + o.TotalTokens,
	}
}

// Response is a single chat completion response.
type Response struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
}

// Client is a model backend. Implementations must be safe for reuse across calls.
type Client interface {
	Chat(ctx context.Context, req Request) (Response, error)
}

// StreamClient is optionally implemented by clients that can stream a reply
// incrementally. Content fragments are passed to onDelta as they arrive; the
// fully assembled Response is still returned at the end.
type StreamClient interface {
	ChatStream(ctx context.Context, req Request, onDelta func(string)) (Response, error)
}
