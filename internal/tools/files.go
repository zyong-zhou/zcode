package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadFile reads a UTF-8 text file under Root. Output is capped: at most
// MaxBytes are returned (with a marker when cut), and no more than
// hardReadLimit bytes are loaded from disk at all.
type ReadFile struct {
	Root string
	// MaxBytes caps the returned text. Defaults to 64 KiB when <= 0.
	MaxBytes int
}

// hardReadLimit bounds how much of a single file is loaded into memory.
const hardReadLimit = 8 << 20 // 8 MiB

func (t ReadFile) Name() string { return "read_file" }

// PlanSafe reports that reading is allowed in plan mode.
func (t ReadFile) PlanSafe() bool { return true }

func (t ReadFile) Description() string {
	return "Read a UTF-8 text file. The path is relative to the project root. Large files are truncated."
}

func (t ReadFile) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string", "description": "File path relative to the project root."},
			"max_bytes": map[string]any{"type": "integer", "description": "Maximum bytes to return (default 65536)."},
		},
		"required": []string{"path"},
	}
}

func (t ReadFile) Run(_ context.Context, args string) (string, error) {
	var in struct {
		Path     string `json:"path"`
		MaxBytes int    `json:"max_bytes"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("read_file: bad arguments: %w", err)
	}
	abs, err := resolveUnder(t.Root, in.Path)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, hardReadLimit))
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	out := string(data)
	if len(data) == hardReadLimit {
		out += "\n... [file cut off at 8 MiB] ..."
	}

	maxBytes := in.MaxBytes
	if maxBytes <= 0 {
		maxBytes = t.MaxBytes
	}
	return truncate(out, maxBytes), nil
}

// WriteFile writes a UTF-8 text file under Root, creating parent directories.
type WriteFile struct{ Root string }

func (t WriteFile) Name() string { return "write_file" }

// PlanSafe reports that writing is not allowed in plan mode.
func (t WriteFile) PlanSafe() bool { return false }

func (t WriteFile) Description() string {
	return "Write a UTF-8 text file, creating parent directories. The path is relative to the project root."
}

func (t WriteFile) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "File path relative to the project root."},
			"content": map[string]any{"type": "string", "description": "Full file contents to write."},
		},
		"required": []string{"path", "content"},
	}
}

func (t WriteFile) Run(_ context.Context, args string) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("write_file: bad arguments: %w", err)
	}
	abs, err := resolveUnder(t.Root, in.Path)
	if err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
}

// resolveUnder joins root and rel and rejects any result that escapes root.
// All file-access tools must route paths through this helper.
func resolveUnder(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("path is empty")
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(base, rel))
	if err != nil {
		return "", err
	}
	relToBase, err := filepath.Rel(base, abs)
	if err != nil {
		return "", err
	}
	if relToBase == ".." || strings.HasPrefix(relToBase, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes project root", rel)
	}
	return abs, nil
}
