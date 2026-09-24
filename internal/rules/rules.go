// Package rules loads project instruction files (for example AGENTS.md) that
// are injected into the system prompt so the model follows repository
// conventions. Loading is best-effort: a missing file is skipped, not an error.
package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultMaxBytes caps how much rule text reaches the prompt.
const DefaultMaxBytes = 32 << 10

// Store holds the concatenated rule text from the loaded files.
type Store struct {
	files []string
	text  string
}

// Load reads each file under root that exists, concatenating them in order.
// Absolute paths are used as-is. The combined text is capped at maxBytes runes.
func Load(root string, files []string, maxBytes int) (*Store, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	s := &Store{}
	var b strings.Builder
	for _, name := range files {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("rules: read %s: %w", path, err)
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(body)
		s.files = append(s.files, name)
	}

	text := b.String()
	if r := []rune(text); len(r) > maxBytes {
		text = string(r[:maxBytes]) + "\n\n[rules truncated]"
	}
	s.text = text
	return s, nil
}

// Render returns the rule text (empty when nothing was loaded).
func (s *Store) Render() string {
	if s == nil {
		return ""
	}
	return s.text
}

// Files returns the names that were actually loaded.
func (s *Store) Files() []string {
	if s == nil {
		return nil
	}
	return s.files
}
