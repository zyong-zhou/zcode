package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
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
	name, err := relName(in.Path)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	root, err := rootHandle(t.Root)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	defer root.Close()

	f, err := root.Open(name)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, hardReadLimit+1))
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	out := string(data)
	if len(data) > hardReadLimit {
		// Read one byte past the limit so a file of exactly hardReadLimit bytes
		// is not reported as truncated, and cut on a rune boundary so the text
		// stays valid UTF-8.
		n := hardReadLimit
		for n > 0 && !utf8.RuneStart(data[n]) {
			n--
		}
		out = string(data[:n]) + "\n... [file cut off at 8 MiB] ..."
	}

	maxBytes := in.MaxBytes
	if maxBytes <= 0 {
		maxBytes = t.MaxBytes
	}
	return truncate(out, maxBytes), nil
}

// WriteFile writes a UTF-8 text file under Root, creating parent directories.
type WriteFile struct {
	Root string
	// Backup, when set, snapshots the previous contents before writing so the
	// change can be undone. It receives the project-relative source path and the
	// absolute path. A new file (no previous contents) is left to the tool.
	Backup func(source, abs string) error
}

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

// Preview summarises the write for an approval prompt.
func (t WriteFile) Preview(args string) (string, bool) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil || strings.TrimSpace(in.Path) == "" {
		return "", false
	}
	return fmt.Sprintf("写入 %s（%d 字节）:\n%s", in.Path, len(in.Content), firstLines(in.Content, previewMaxLines, previewMaxWidth)), true
}

func (t WriteFile) Run(_ context.Context, args string) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("write_file: bad arguments: %w", err)
	}
	name, err := relName(in.Path)
	if err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	root, err := rootHandle(t.Root)
	if err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	defer root.Close()

	if dir := filepath.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("write_file: %w", err)
		}
	}
	if t.Backup != nil {
		abs, absErr := filepath.Abs(filepath.Join(t.Root, name))
		if absErr != nil {
			return "", fmt.Errorf("write_file: %w", absErr)
		}
		if err := t.Backup(in.Path, abs); err != nil {
			return "", fmt.Errorf("write_file: %w", err)
		}
	}
	if err := root.WriteFile(name, []byte(in.Content), 0o644); err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
}

// rootHandle opens the project root for containment-checked access. Every
// file-access tool routes through os.Root rather than a lexical path check:
// a lexical check cannot see through a symbolic link or an NTFS junction, and
// filepath.EvalSymlinks does not resolve junctions on Windows at all. os.Root
// rejects any path whose components leave the root, including via links.
func rootHandle(root string) (*os.Root, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("project root is empty")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open project root: %w", err)
	}
	return r, nil
}

// relName converts a model-supplied path into the root-relative name os.Root
// expects. Absolute paths and paths escaping the root are rejected by os.Root
// itself, so they are passed through rather than pre-judged here.
func relName(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("path is empty")
	}
	name := filepath.Clean(filepath.FromSlash(rel))
	if name == "" {
		return ".", nil
	}
	return name, nil
}
