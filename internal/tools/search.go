package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxSearchFileBytes skips files too large to scan line by line.
const maxSearchFileBytes = 1 << 20 // 1 MiB

// Search greps file contents under Root for a regular expression.
type Search struct{ Root string }

func (t Search) Name() string { return "search" }

// PlanSafe reports that searching is allowed in plan mode.
func (t Search) PlanSafe() bool { return true }

func (t Search) Description() string {
	return "Search file contents under a path for a Go regular expression. Returns matches as path:line:text. Skips noise directories and binary or oversized files."
}

func (t Search) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":     map[string]any{"type": "string", "description": "Go regular expression to match within each line."},
			"path":        map[string]any{"type": "string", "description": "Directory or file relative to the project root (default \".\")."},
			"include":     map[string]any{"type": "string", "description": "Optional file name glob, for example \"*.go\"."},
			"max_results": map[string]any{"type": "integer", "description": "Maximum matches returned (default 100)."},
		},
		"required": []string{"pattern"},
	}
}

// errSearchLimit is an internal sentinel that stops the walk once enough
// matches have been collected.
var errSearchLimit = errors.New("search: result limit reached")

func (t Search) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Include    string `json:"include"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("search: bad arguments: %w", err)
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("search: pattern is required")
	}
	if strings.TrimSpace(in.Path) == "" {
		in.Path = "."
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 100
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", fmt.Errorf("search: bad pattern: %w", err)
	}

	abs, err := resolveUnder(t.Root, in.Path)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	base, err := filepath.Abs(t.Root)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	var results []string
	walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if isIgnoredDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if in.Include != "" {
			if ok, _ := filepath.Match(in.Include, d.Name()); !ok {
				return nil
			}
		}
		if fi, infoErr := d.Info(); infoErr != nil || fi.Size() > maxSearchFileBytes {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil // skip unreadable or binary files
		}
		rel, relErr := filepath.Rel(base, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		for i, line := range strings.Split(string(data), "\n") {
			if !re.MatchString(line) {
				continue
			}
			results = append(results, fmt.Sprintf("%s:%d:%s", rel, i+1, strings.TrimRight(line, "\r")))
			if len(results) >= in.MaxResults {
				return errSearchLimit
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errSearchLimit) {
		return "", fmt.Errorf("search: %w", walkErr)
	}
	if len(results) == 0 {
		return "(no matches)", nil
	}

	out := strings.Join(results, "\n")
	if errors.Is(walkErr, errSearchLimit) {
		out += fmt.Sprintf("\n... [stopped at %d matches] ...", in.MaxResults)
	}
	return truncate(out, 0), nil
}
