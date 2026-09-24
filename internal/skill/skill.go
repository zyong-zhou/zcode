// Package skill loads named, on-demand instruction sets from a directory of
// Markdown files. The model loads one through the "skill" tool when its
// description matches the task at hand.
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const ext = ".md"

// Skill is one loadable instruction set.
type Skill struct {
	Name        string
	Description string
	Body        string
	Path        string
}

// Load reads *.md files from dir, sorted by name. A missing directory yields no
// skills, not an error.
func Load(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("skill: list %s: %w", dir, err)
	}
	var out []Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("skill: read %s: %w", path, err)
		}
		name := strings.TrimSuffix(e.Name(), ext)
		meta, body := splitFrontMatter(string(data))
		sk := Skill{Name: name, Path: path, Body: strings.TrimSpace(body)}
		if meta["name"] != "" {
			sk.Name = meta["name"]
		}
		sk.Description = meta["description"]
		if strings.TrimSpace(sk.Body) == "" {
			continue
		}
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// splitFrontMatter parses an optional leading "---" block of "key: value"
// lines, returning the metadata and the remaining body.
func splitFrontMatter(s string) (meta map[string]string, body string) {
	meta = map[string]string{}
	trimmed := strings.TrimLeft(s, "\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return meta, s
	}
	rest := trimmed[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return meta, s
	}
	for _, line := range strings.Split(rest[:idx], "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok {
			meta[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return meta, rest[idx+len("\n---"):]
}
