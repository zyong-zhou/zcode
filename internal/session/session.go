// Package session persists a conversation transcript as a JSONL file (one
// llm.Message per line) so a run can be resumed later. It also supports listing
// and reopening past sessions.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zyong-zhou/zcode/internal/llm"
)

// ext is the suffix used for session files.
const ext = ".jsonl"

// Store is a directory of session files.
type Store struct {
	Dir string
}

// Info summarises a stored session for listing.
type Info struct {
	ID       string
	Path     string
	ModTime  time.Time
	Messages int
	Preview  string
}

// Session is an open transcript file.
type Session struct {
	id   string
	path string
	f    *os.File
}

// ID returns the session identifier.
func (s *Session) ID() string { return s.id }

// Path returns the transcript file path.
func (s *Session) Path() string { return s.path }

// New creates a fresh session with a timestamp-derived, collision-free id.
func (s Store) New() (*Session, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	base := time.Now().Format("20060102-150405")
	for i := 1; ; i++ {
		id := base
		if i > 1 {
			id = fmt.Sprintf("%s-%d", base, i)
		}
		path := filepath.Join(s.Dir, id+ext)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return s.create(id)
		}
	}
}

// Open opens an existing session. It fails when the id does not exist, so a
// typo never silently starts an empty conversation.
func (s Store) Open(id string) (*Session, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	path := filepath.Join(s.Dir, id+ext)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("session: %q not found", id)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: open: %w", err)
	}
	return &Session{id: id, path: path, f: f}, nil
}

// Delete removes a stored session file.
func (s Store) Delete(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	path := filepath.Join(s.Dir, strings.TrimSpace(id)+ext)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("session: delete: %w", err)
	}
	return nil
}

// Rename changes a session's id. It fails when the new id already exists.
func (s Store) Rename(id, newID string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if err := validateID(newID); err != nil {
		return err
	}
	id, newID = strings.TrimSpace(id), strings.TrimSpace(newID)
	from := filepath.Join(s.Dir, id+ext)
	to := filepath.Join(s.Dir, newID+ext)
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("session: %q already exists", newID)
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("session: rename: %w", err)
	}
	return nil
}

// validateID rejects ids that could escape the session directory.
func validateID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("session: empty id")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("session: invalid id %q", id)
	}
	return nil
}

func (s Store) create(id string) (*Session, error) {
	path := filepath.Join(s.Dir, id+ext)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: create: %w", err)
	}
	return &Session{id: id, path: path, f: f}, nil
}

func (s Store) ensure() error {
	if strings.TrimSpace(s.Dir) == "" {
		return fmt.Errorf("session: empty directory")
	}
	return os.MkdirAll(s.Dir, 0o700)
}

// List returns stored sessions, newest first.
func (s Store) List() ([]Info, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: list: %w", err)
	}
	var infos []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		path := filepath.Join(s.Dir, e.Name())
		info := Info{ID: strings.TrimSuffix(e.Name(), ext), Path: path}
		if fi, err := e.Info(); err == nil {
			info.ModTime = fi.ModTime()
		}
		if msgs, err := (&Session{path: path}).Load(); err == nil {
			info.Messages = len(msgs)
			info.Preview = preview(msgs)
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ModTime.After(infos[j].ModTime) })
	return infos, nil
}

// Latest returns the id of the most recently modified session, or "" when none.
func (s Store) Latest() (string, error) {
	infos, err := s.List()
	if err != nil || len(infos) == 0 {
		return "", err
	}
	return infos[0].ID, nil
}

// Load reads the transcript, oldest first. A truncated final line is ignored so
// an interrupted write does not make the whole session unreadable.
func (s *Session) Load() ([]llm.Message, error) {
	if s == nil {
		return nil, nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("session: load: %w", err)
	}
	defer f.Close()
	var msgs []llm.Message
	dec := json.NewDecoder(f)
	for {
		var m llm.Message
		if err := dec.Decode(&m); err != nil {
			break
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// Append writes one message as a JSON line.
func (s *Session) Append(m llm.Message) error {
	if s == nil || s.f == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("session: marshal: %w", err)
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("session: append: %w", err)
	}
	return nil
}

// Reset truncates the transcript. It reopens the file rather than calling
// Truncate, because an O_APPEND handle on Windows lacks the write access
// SetEndOfFile needs.
func (s *Session) Reset() error {
	if s == nil || s.f == nil {
		return nil
	}
	path := s.path
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("session: reset: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("session: reset: %w", err)
	}
	s.f = f
	return nil
}

// Close closes the transcript file.
func (s *Session) Close() error {
	if s == nil || s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// preview renders a short summary from the newest user message.
func preview(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		s := strings.Join(strings.Fields(msgs[i].Content), " ")
		if r := []rune(s); len(r) > 60 {
			return string(r[:60]) + "…"
		}
		return s
	}
	return ""
}

// Manager owns the active session so it can be rotated safely while an agent
// holds it as its transcript.
type Manager struct {
	store Store
	mu    sync.Mutex
	cur   *Session
}

// NewManager returns a manager over store.
func NewManager(store Store) *Manager { return &Manager{store: store} }

func (m *Manager) rotate(s *Session, err error) (string, error) {
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil {
		_ = m.cur.Close()
	}
	m.cur = s
	return s.id, nil
}

// New rotates to a fresh session and returns its id.
func (m *Manager) New() (string, error) {
	s, err := m.store.New()
	return m.rotate(s, err)
}

// Open rotates to an existing session and returns its id.
func (m *Manager) Open(id string) (string, error) {
	s, err := m.store.Open(id)
	return m.rotate(s, err)
}

// Load implements zcode.Transcript.
func (m *Manager) Load() ([]llm.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil, nil
	}
	return m.cur.Load()
}

// Append implements zcode.Transcript.
func (m *Manager) Append(msg llm.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	return m.cur.Append(msg)
}

// Reset implements zcode.Transcript.
func (m *Manager) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	return m.cur.Reset()
}

// Current returns the active session id, path, and message count.
func (m *Manager) Current() (id, path string, messages int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return "", "", 0
	}
	msgs, _ := m.cur.Load()
	return m.cur.id, m.cur.path, len(msgs)
}

// List returns stored sessions, newest first.
func (m *Manager) List() ([]Info, error) { return m.store.List() }

// Latest returns the most recent session id, or "" when none exists.
func (m *Manager) Latest() (string, error) { return m.store.Latest() }

// Delete removes a stored session. If it is the active session, it is closed
// and the manager becomes session-less until one is opened or created.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Normalise before comparing: ids arrive from user input, and an untrimmed
	// id would delete the file while leaving m.cur pointing at it, silently
	// discarding every later Append.
	id = strings.TrimSpace(id)
	if m.cur != nil && m.cur.id == id {
		_ = m.cur.Close()
		m.cur = nil
	}
	return m.store.Delete(id)
}

// Rename changes a session's id, updating the active session when it matches.
// The active transcript is closed before the rename and reopened afterwards,
// because Windows refuses to rename an open file.
func (m *Manager) Rename(id, newID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Normalise before comparing, for the same reason as Delete.
	id, newID = strings.TrimSpace(id), strings.TrimSpace(newID)
	active := m.cur != nil && m.cur.id == id
	if active {
		if err := m.cur.Close(); err != nil {
			return err
		}
	}
	if err := m.store.Rename(id, newID); err != nil {
		if active {
			m.cur = nil
		}
		return err
	}
	if active {
		path := filepath.Join(m.store.Dir, newID+ext)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			// Close already cleared the handle, so Append would silently drop
			// messages; going session-less makes the loss observable instead.
			m.cur = nil
			return fmt.Errorf("session: reopen after rename: %w", err)
		}
		m.cur.id = newID
		m.cur.path = path
		m.cur.f = f
	}
	return nil
}

// Close closes the active session.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	return m.cur.Close()
}
