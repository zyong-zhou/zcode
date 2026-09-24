// Package command loads user-defined slash commands from a directory of
// Markdown files. The file name is the command; its body is the prompt, with
// $ARGUMENTS, {{args}}, and $1..$9 substituted from the typed arguments.
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ext = ".md"

// Command is a user-defined slash command.
type Command struct {
	Name        string
	Description string
	// Body is the prompt template.
	Body string
	Path string
}

// Expand substitutes the typed arguments into the body. When the template has
// no placeholder, the arguments are appended as a final paragraph.
func (c Command) Expand(args string) string {
	args = strings.TrimSpace(args)
	out := c.Body
	hadPlaceholder := strings.Contains(out, "$ARGUMENTS") || strings.Contains(out, "{{args}}") || containsPositional(out)
	out = strings.ReplaceAll(out, "$ARGUMENTS", args)
	out = strings.ReplaceAll(out, "{{args}}", args)

	fields := strings.Fields(args)
	// Longest positional first so $10 is not mistaken for $1 followed by "0".
	for i := len(fields) - 1; i >= 0 && i < 9; i-- {
		out = strings.ReplaceAll(out, fmt.Sprintf("$%d", i+1), fields[i])
	}

	if args != "" && !hadPlaceholder {
		out = strings.TrimRight(out, "\n") + "\n\n" + args
	}
	return strings.TrimSpace(out)
}

// containsPositional reports whether s references a positional argument such as
// $1 or $2.
func containsPositional(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && s[i+1] >= '1' && s[i+1] <= '9' {
			return true
		}
	}
	return false
}

// Load reads *.md files from dir, keyed by command name. A missing directory
// yields an empty map, not an error.
func Load(dir string) (map[string]Command, error) {
	out := map[string]Command{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("command: list %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("command: read %s: %w", path, err)
		}
		name := strings.TrimSuffix(e.Name(), ext)
		desc, body := splitFrontMatter(string(data))
		out[name] = Command{Name: name, Description: desc, Body: strings.TrimSpace(body), Path: path}
	}
	return out, nil
}

// splitFrontMatter parses an optional leading "---" block carrying a
// "description:" key, returning the description and the remaining body.
func splitFrontMatter(s string) (desc, body string) {
	trimmed := strings.TrimLeft(s, "\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return "", s
	}
	rest := trimmed[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", s
	}
	head := rest[:idx]
	body = rest[idx+len("\n---"):]
	for _, line := range strings.Split(head, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			desc = strings.TrimSpace(v)
		}
	}
	return desc, body
}
