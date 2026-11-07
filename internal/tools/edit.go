package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Edit replaces exact text in an existing file. Unlike WriteFile it does not
// overwrite the whole file, so targeted changes are safer and easier to review.
type Edit struct {
	Root string
	// Backup, when set, snapshots the file before modifying it so the change can
	// be undone. It receives the project-relative source path and the absolute
	// path.
	Backup func(source, abs string) error
}

func (t Edit) Name() string { return "edit" }

// PlanSafe reports that editing is not allowed in plan mode.
func (t Edit) PlanSafe() bool { return false }

func (t Edit) Description() string {
	return "Replace exact text in an existing file. Provide one or more edits, each with the text to find (old) and its replacement (new). Each old must match exactly once unless replace_all is set. Use write_file to create new files."
}

func (t Edit) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "File to edit, relative to the project root."},
			"edits": map[string]any{
				"type":        "array",
				"description": "Edits applied in order.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old":         map[string]any{"type": "string", "description": "Exact text to find."},
						"new":         map[string]any{"type": "string", "description": "Replacement text; may be empty to delete."},
						"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match."},
					},
					"required": []string{"old", "new"},
				},
			},
		},
		"required": []string{"path", "edits"},
	}
}

// Preview summarises the edits for an approval prompt.
func (t Edit) Preview(args string) (string, bool) {
	var in struct {
		Path  string `json:"path"`
		Edits []struct {
			Old string `json:"old"`
			New string `json:"new"`
		} `json:"edits"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil || strings.TrimSpace(in.Path) == "" || len(in.Edits) == 0 {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "编辑 %s（%d 处）:", in.Path, len(in.Edits))
	for _, e := range in.Edits {
		fmt.Fprintf(&b, "\n- %s\n+ %s", oneLine(e.Old, previewMaxWidth), oneLine(e.New, previewMaxWidth))
	}
	return b.String(), true
}

func (t Edit) Run(_ context.Context, args string) (string, error) {
	var in struct {
		Path  string `json:"path"`
		Edits []struct {
			Old        string `json:"old"`
			New        string `json:"new"`
			ReplaceAll bool   `json:"replace_all"`
		} `json:"edits"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("edit: bad arguments: %w", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("edit: path is required")
	}
	if len(in.Edits) == 0 {
		return "", fmt.Errorf("edit: at least one edit is required")
	}

	name, err := relName(in.Path)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	root, err := rootHandle(t.Root)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	defer root.Close()

	info, err := root.Stat(name)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	// Editing streams the whole file through memory, so refuse oversized files
	// rather than loading them: unlike read_file there is no incremental path.
	if info.Size() > hardReadLimit {
		return "", fmt.Errorf("edit: %s is %d bytes, larger than the %d-byte edit limit", in.Path, info.Size(), hardReadLimit)
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	content := string(data)

	for i, e := range in.Edits {
		if e.Old == "" {
			return "", fmt.Errorf("edit: edits[%d]: old is required", i)
		}
		switch n := strings.Count(content, e.Old); {
		case n == 0:
			return "", fmt.Errorf("edit: edits[%d]: text not found: %q", i, editSnippet(e.Old))
		case n > 1 && !e.ReplaceAll:
			return "", fmt.Errorf("edit: edits[%d]: text occurs %d times; set replace_all or add more context: %q", i, n, editSnippet(e.Old))
		case e.ReplaceAll:
			content = strings.ReplaceAll(content, e.Old, e.New)
		default:
			content = strings.Replace(content, e.Old, e.New, 1)
		}
	}

	if t.Backup != nil {
		abs, absErr := filepath.Abs(filepath.Join(t.Root, name))
		if absErr != nil {
			return "", fmt.Errorf("edit: %w", absErr)
		}
		if err := t.Backup(in.Path, abs); err != nil {
			return "", fmt.Errorf("edit: %w", err)
		}
	}

	if err := root.WriteFile(name, []byte(content), info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("edit: write: %w", err)
	}
	return fmt.Sprintf("edited %s (%d edit(s))", in.Path, len(in.Edits)), nil
}

func editSnippet(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "..."
	}
	return s
}
