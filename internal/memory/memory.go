// Package memory stores durable project facts in a human-editable markdown file
// and renders them for injection into the agent's system prompt.
//
// The markdown file is the single source of truth so users can review and edit
// it by hand. Each fact is a bullet, optionally prefixed with its date and tags:
//
//   - (2026-09-24) [架构] TUI 使用 bubbletea
//
// Bullets without the metadata prefix are accepted and treated as plain text.
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxEntries = 200
	defaultMaxBytes   = 32 << 10 // 32 KiB
)

var (
	// bulletRe matches a markdown list item.
	bulletRe = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)
	// metaRe splits the optional "(date) [tags] " prefix from a fact's text.
	metaRe = regexp.MustCompile(`^\((\d{4}-\d{2}-\d{2})\)\s*(?:\[([^\]]*)\])?\s*(.*)$`)
)

// Entry is one remembered fact.
type Entry struct {
	Text string
	Tags []string
	At   time.Time
}

// Store is a concurrency-safe, file-backed set of facts.
type Store struct {
	path       string
	mu         sync.Mutex
	entries    []Entry
	maxEntries int
	maxBytes   int
}

// Open loads the store at path, creating nothing until the first write.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("memory: empty path")
	}
	s := &Store{path: path, maxEntries: defaultMaxEntries, maxBytes: defaultMaxBytes}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("memory: read %s: %w", s.path, err)
	}
	s.entries = parse(string(data))
	return nil
}

// Add appends a fact unless an identical one already exists. added is false for
// a duplicate.
func (s *Store) Add(text string, tags []string) (Entry, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Entry{}, false, errors.New("memory: empty text")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	norm := normalize(text)
	for _, e := range s.entries {
		if normalize(e.Text) == norm {
			return e, false, nil
		}
	}
	e := Entry{Text: text, Tags: cleanTags(tags), At: time.Now()}
	s.entries = append(s.entries, e)
	s.enforceLocked()
	if err := s.saveLocked(); err != nil {
		return e, false, err
	}
	return e, true, nil
}

// Remove deletes every fact whose text contains match (case-insensitive) and
// returns how many were removed.
func (s *Store) Remove(match string) int {
	match = strings.ToLower(strings.TrimSpace(match))
	if match == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := s.entries[:0]
	removed := 0
	for _, e := range s.entries {
		if strings.Contains(strings.ToLower(e.Text), match) {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	s.entries = kept
	if removed > 0 {
		_ = s.saveLocked()
	}
	return removed
}

// Clear removes all facts and returns how many were removed.
func (s *Store) Clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.entries)
	s.entries = nil
	if n > 0 {
		_ = s.saveLocked()
	}
	return n
}

// Search returns facts matching query (substring of text or tag), newest first.
func (s *Store) Search(query string, limit int) []Entry {
	q := strings.ToLower(strings.TrimSpace(query))
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []Entry
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if q == "" || strings.Contains(strings.ToLower(e.Text), q) || hasTag(e.Tags, q) {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// Entries returns a copy of all facts in insertion order.
func (s *Store) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Count returns the number of stored facts.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Render returns the facts as markdown bullets, ready to inject into a prompt.
func (s *Store) Render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renderLocked()
}

func (s *Store) renderLocked() string {
	if len(s.entries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range s.entries {
		b.WriteString(renderEntry(e))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Store) enforceLocked() {
	for len(s.entries) > s.maxEntries {
		s.entries = s.entries[1:]
	}
	for len(s.entries) > 1 && len(s.renderLocked()) > s.maxBytes {
		s.entries = s.entries[1:]
	}
}

func (s *Store) saveLocked() error {
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("memory: mkdir: %w", err)
		}
	}
	var b strings.Builder
	b.WriteString("# 项目记忆\n\n")
	b.WriteString("<!-- 由 zcode 维护，可手工编辑；每行一条：- (日期) [标签] 文本 -->\n\n")
	if len(s.entries) == 0 {
		b.WriteString("<!-- 暂无记忆 -->\n")
	} else {
		for _, e := range s.entries {
			b.WriteString(renderEntry(e))
			b.WriteByte('\n')
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("memory: write: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("memory: rename: %w", err)
	}
	return nil
}

func parse(body string) []Entry {
	var out []Entry
	for _, line := range strings.Split(body, "\n") {
		m := bulletRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[1])
		if text == "" {
			continue
		}
		e := Entry{Text: text}
		if mm := metaRe.FindStringSubmatch(text); mm != nil {
			if t, err := time.Parse("2006-01-02", mm[1]); err == nil {
				e.At = t
			}
			for _, tag := range strings.Split(mm[2], ",") {
				if tag = strings.TrimSpace(tag); tag != "" {
					e.Tags = append(e.Tags, tag)
				}
			}
			e.Text = strings.TrimSpace(mm[3])
		}
		if e.Text == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

func renderEntry(e Entry) string {
	prefix := ""
	if !e.At.IsZero() {
		prefix = "(" + e.At.Format("2006-01-02") + ") "
	}
	if len(e.Tags) > 0 {
		prefix += "[" + strings.Join(e.Tags, ", ") + "] "
	}
	return "- " + prefix + strings.ReplaceAll(e.Text, "\n", " ")
}

func normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func cleanTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}

func hasTag(tags []string, q string) bool {
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}
