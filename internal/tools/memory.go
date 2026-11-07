package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zyong-zhou/zcode/internal/memory"
)

// Remember stores a durable project fact. The memory file lives in the project,
// so it is available again in later sessions.
type Remember struct{ Store *memory.Store }

func (t Remember) Name() string { return "remember" }

func (t Remember) Description() string {
	return "Store a durable project fact in project memory so it is available in future sessions. Use it for conventions, decisions, and gotchas, not for transient state."
}

func (t Remember) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "The fact to remember, ideally one sentence."},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional tags for grouping, for example [\"架构\"].",
			},
		},
		"required": []string{"text"},
	}
}

// PlanSafe reports that remembering is allowed in plan mode; it does not change
// the user's project.
func (t Remember) PlanSafe() bool { return true }

func (t Remember) Run(_ context.Context, args string) (string, error) {
	if t.Store == nil {
		return "", fmt.Errorf("remember: memory is disabled")
	}
	var in struct {
		Text string   `json:"text"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("remember: bad arguments: %w", err)
	}
	if strings.TrimSpace(in.Text) == "" {
		return "", fmt.Errorf("remember: text is required")
	}
	_, added, err := t.Store.Add(in.Text, in.Tags)
	if err != nil {
		return "", fmt.Errorf("remember: %w", err)
	}
	if !added {
		return "already remembered", nil
	}
	return "remembered: " + strings.TrimSpace(in.Text), nil
}

// Recall searches project memory for facts matching a query.
type Recall struct{ Store *memory.Store }

func (t Recall) Name() string { return "recall" }

func (t Recall) Description() string {
	return "Search project memory for facts matching a query. Use it to check what is already known before re-investigating."
}

func (t Recall) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Substring to match against fact text and tags."},
			"limit": map[string]any{"type": "integer", "description": "Maximum facts to return (default 20)."},
		},
		"required": []string{"query"},
	}
}

// PlanSafe reports that recalling is allowed in plan mode.
func (t Recall) PlanSafe() bool { return true }

func (t Recall) Run(_ context.Context, args string) (string, error) {
	if t.Store == nil {
		return "", fmt.Errorf("recall: memory is disabled")
	}
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("recall: bad arguments: %w", err)
	}
	entries := t.Store.Search(in.Query, in.Limit)
	if len(entries) == 0 {
		return "(no matching memories)", nil
	}
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		if len(e.Tags) > 0 {
			b.WriteString("[" + strings.Join(e.Tags, ", ") + "] ")
		}
		b.WriteString(e.Text)
	}
	return b.String(), nil
}

// Forget removes facts whose text matches a substring.
type Forget struct{ Store *memory.Store }

func (t Forget) Name() string { return "forget" }

func (t Forget) Description() string {
	return "Remove facts from project memory whose text contains a substring. Returns how many were removed."
}

func (t Forget) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"match": map[string]any{"type": "string", "description": "Substring of the fact text to remove."},
		},
		"required": []string{"match"},
	}
}

// PlanSafe reports that forgetting is allowed in plan mode.
func (t Forget) PlanSafe() bool { return true }

func (t Forget) Run(_ context.Context, args string) (string, error) {
	if t.Store == nil {
		return "", fmt.Errorf("forget: memory is disabled")
	}
	var in struct {
		Match string `json:"match"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("forget: bad arguments: %w", err)
	}
	n := t.Store.Remove(in.Match)
	return fmt.Sprintf("removed %d memories", n), nil
}
