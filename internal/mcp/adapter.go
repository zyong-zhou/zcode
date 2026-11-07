package mcp

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/zyong-zhou/zcode/internal/tools"
)

// maxToolNameLen matches the OpenAI tool-name limit (^[a-zA-Z0-9_-]{1,64}$).
const maxToolNameLen = 64

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// Tool adapts one MCP tool to the agent's tools.Tool interface.
type Tool struct {
	server   string
	client   Client
	info     ToolInfo
	planSafe bool
}

// NewTool wraps an MCP tool. readOnly marks a whole server as plan-safe.
func NewTool(server string, client Client, info ToolInfo, readOnly bool) Tool {
	return Tool{
		server:   server,
		client:   client,
		info:     info,
		planSafe: readOnly || info.ReadOnly,
	}
}

// Name returns the namespaced, sanitized tool name.
func (t Tool) Name() string { return toolName(t.server, t.info.Name) }

// Description tells the model what the tool does.
func (t Tool) Description() string {
	d := strings.TrimSpace(t.info.Description)
	if d == "" {
		d = "MCP tool " + t.info.Name + " from server " + t.server + "."
	}
	return d
}

// Parameters passes through the server's input schema, defaulting to an empty
// object schema.
func (t Tool) Parameters() map[string]any {
	if len(t.info.InputSchema) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return t.info.InputSchema
}

// PlanSafe reports whether the tool may run in plan mode.
func (t Tool) PlanSafe() bool { return t.planSafe }

// Run invokes the MCP tool and flattens its content blocks to text.
func (t Tool) Run(ctx context.Context, args string) (string, error) {
	if t.client == nil {
		return "", fmt.Errorf("mcp %s: not connected", t.server)
	}
	raw := json.RawMessage(args)
	if strings.TrimSpace(args) == "" {
		raw = json.RawMessage("{}")
	}
	res, err := t.client.CallTool(ctx, t.info.Name, raw)
	if err != nil {
		return "", err
	}
	out := tools.Truncate(renderContent(res.Content), 0)
	if strings.TrimSpace(out) == "" {
		out = "(no content)"
	}
	if res.IsError {
		return "error: " + out, nil
	}
	return out, nil
}

// renderContent flattens MCP content blocks into plain text.
func renderContent(blocks []ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image "+b.MimeType+"]")
		case "resource":
			parts = append(parts, "[resource "+b.URI+"]")
		default:
			if b.Text != "" {
				parts = append(parts, b.Text)
			} else {
				parts = append(parts, "["+b.Type+"]")
			}
		}
	}
	return strings.Join(parts, "\n")
}

// toolName builds a namespaced name that is safe for OpenAI tool specs and
// stays within the length limit by appending a short hash when needed.
func toolName(server, name string) string {
	base := "mcp__" + sanitize(server) + "__" + sanitize(name)
	if len(base) <= maxToolNameLen {
		return base
	}
	sum := sha1.Sum([]byte(base))
	suffix := hex.EncodeToString(sum[:])[:8]
	keep := maxToolNameLen - len(suffix) - 1
	if keep < 1 {
		keep = 1
	}
	return base[:keep] + "_" + suffix
}

func sanitize(s string) string {
	s = unsafeName.ReplaceAllString(strings.TrimSpace(s), "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "tool"
	}
	return s
}
