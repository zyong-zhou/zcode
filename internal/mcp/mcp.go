// Package mcp implements a minimal Model Context Protocol client (stdio
// transport) and adapts the tools it exposes to the agent's tools.Tool
// interface.
package mcp

import (
	"context"
	"encoding/json"
)

// ServerConfig describes one MCP server launched over stdio.
type ServerConfig struct {
	Name     string
	Command  string
	Args     []string
	Env      map[string]string
	ReadOnly bool // treat all of this server's tools as plan-safe
}

// ClientInfo identifies this client during the MCP handshake.
type ClientInfo struct {
	Name    string
	Version string
	Root    string // reported to servers via roots/list
}

// ToolInfo is one tool advertised by an MCP server.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema map[string]any
	ReadOnly    bool
}

// ContentBlock is one entry of a tools/call result.
type ContentBlock struct {
	Type     string
	Text     string
	Data     string
	MimeType string
	URI      string
}

// CallResult is the outcome of tools/call.
type CallResult struct {
	Content []ContentBlock
	IsError bool
}

// Client is a connected MCP server.
type Client interface {
	// Initialize performs the handshake.
	Initialize(ctx context.Context) error
	// ListTools returns every tool the server advertises.
	ListTools(ctx context.Context) ([]ToolInfo, error)
	// CallTool invokes one tool with raw JSON arguments.
	CallTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error)
	// Close releases the connection and any child process.
	Close() error
}
