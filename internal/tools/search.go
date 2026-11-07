package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	if in.MaxResults > maxSearchResults {
		in.MaxResults = maxSearchResults
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", fmt.Errorf("search: bad pattern: %w", err)
	}

	name, err := relName(in.Path)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	root, err := rootHandle(t.Root)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	defer root.Close()

	var results []string
	// Walking Root.FS() keeps traversal inside the root even when the tree
	// contains links.
	walkErr := fs.WalkDir(root.FS(), name, func(p string, d fs.DirEntry, err error) error {
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
		// Skip links. DirEntry.Info reports the link itself (tiny), so a size
		// guard alone would pass and the read would follow the link.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if in.Include != "" {
			ok, matchErr := filepath.Match(in.Include, d.Name())
			if matchErr != nil {
				return fmt.Errorf("search: bad include %q: %w", in.Include, matchErr)
			}
			if !ok {
				return nil
			}
		}
		if fi, infoErr := d.Info(); infoErr != nil || fi.Size() > maxSearchFileBytes {
			return nil
		}
		f, openErr := root.Open(p)
		if openErr != nil {
			return nil
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxSearchFileBytes+1))
		_ = f.Close()
		// Bound the read as well: the size seen above may not match the size
		// actually read.
		if readErr != nil || len(data) > maxSearchFileBytes {
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil // skip binary files
		}
		rel := p

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
