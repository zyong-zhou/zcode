package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

// ListDir lists files and directories under Root.
type ListDir struct{ Root string }

func (t ListDir) Name() string { return "list_dir" }

// PlanSafe reports that listing is allowed in plan mode.
func (t ListDir) PlanSafe() bool { return true }

func (t ListDir) Description() string {
	return "List files and directories under a path (default project root). Directories end with '/'. Skips noise directories such as .git and node_modules."
}

func (t ListDir) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "Directory relative to the project root (default \".\")."},
			"max_depth":   map[string]any{"type": "integer", "description": "Maximum recursion depth (default 3)."},
			"max_entries": map[string]any{"type": "integer", "description": "Maximum number of entries returned (default 500)."},
		},
	}
}

func (t ListDir) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Path       string `json:"path"`
		MaxDepth   int    `json:"max_depth"`
		MaxEntries int    `json:"max_entries"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("list_dir: bad arguments: %w", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		in.Path = "."
	}
	if in.MaxDepth <= 0 {
		in.MaxDepth = 3
	}
	if in.MaxDepth > maxListDepth {
		in.MaxDepth = maxListDepth
	}
	if in.MaxEntries <= 0 {
		in.MaxEntries = 500
	}
	if in.MaxEntries > maxListEntries {
		in.MaxEntries = maxListEntries
	}

	name, err := relName(in.Path)
	if err != nil {
		return "", fmt.Errorf("list_dir: %w", err)
	}
	root, err := rootHandle(t.Root)
	if err != nil {
		return "", fmt.Errorf("list_dir: %w", err)
	}
	defer root.Close()

	info, err := root.Stat(name)
	if err != nil {
		return "", fmt.Errorf("list_dir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("list_dir: %s is not a directory", in.Path)
	}

	var lines []string
	truncated := false
	// Walking Root.FS() keeps traversal inside the root: a link that points out
	// of it yields an error instead of the linked directory's contents.
	walkErr := fs.WalkDir(root.FS(), name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip entries we cannot read
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == name {
			return nil
		}
		// Never report links: a link can hide what it really points at.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() && isIgnoredDir(d.Name()) {
			return fs.SkipDir
		}
		rel := p

		if d.IsDir() {
			if len(lines) >= in.MaxEntries {
				truncated = true
				return fs.SkipDir
			}
			lines = append(lines, rel+"/")
			if strings.Count(rel, "/")+1 >= in.MaxDepth {
				return fs.SkipDir
			}
			return nil
		}

		if len(lines) >= in.MaxEntries {
			truncated = true
			return nil
		}
		size := int64(-1)
		if fi, infoErr := d.Info(); infoErr == nil {
			size = fi.Size()
		}
		lines = append(lines, fmt.Sprintf("%s (%d bytes)", rel, size))
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("list_dir: %w", walkErr)
	}
	if len(lines) == 0 {
		return "(empty)", nil
	}

	out := strings.Join(lines, "\n")
	if truncated {
		out += fmt.Sprintf("\n... [stopped at %d entries] ...", in.MaxEntries)
	}
	return truncate(out, 0), nil
}
