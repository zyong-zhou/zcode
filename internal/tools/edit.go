package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Edit replaces exact text in an existing file. Unlike WriteFile it does not
// overwrite the whole file, so targeted changes are safer and easier to review.
type Edit struct{ Root string }

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

	path, err := resolveUnder(t.Root, in.Path)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	data, err := os.ReadFile(path)
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

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
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
