// Package backup keeps pre-write snapshots of project files so a change can be
// rolled back with /undo. Snapshots live in a directory (by default
// .zcode/backups) as copies whose file names encode the source path.
package backup

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ext is the suffix used for snapshot files.
const ext = ".bak"

// Store is a directory of snapshots tied to a project root. Root is used to
// resolve a snapshot's source path on restore. Keep bounds how many snapshots
// are retained; the oldest are pruned when the limit is exceeded (0 disables
// pruning).
type Store struct {
	Dir  string
	Root string
	Keep int
}

// Entry describes one stored snapshot.
type Entry struct {
	ID      string
	Path    string
	Source  string
	ModTime time.Time
	Size    int64
}

func (s Store) enabled() bool {
	return strings.TrimSpace(s.Dir) != "" && strings.TrimSpace(s.Root) != ""
}

// Snapshot copies abs, whose project-relative path is source, into the store and
// returns the snapshot id. It returns "" without error when the store is
// disabled or the file does not exist yet (a new file has nothing to lose).
func (s Store) Snapshot(source, abs string) (string, error) {
	if !s.enabled() {
		return "", nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("backup: stat: %w", err)
	}
	if info.IsDir() {
		return "", nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("backup: read: %w", err)
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", fmt.Errorf("backup: mkdir: %w", err)
	}
	// The id is <unixnano>-<base64url(source)>. A coarse clock can return the
	// same timestamp twice in a row, so bump the numeric prefix until the name
	// is free; keep the digit count stable so lexical order stays time order.
	nano := time.Now().UnixNano()
	var id string
	for i := int64(0); ; i++ {
		id = fmt.Sprintf("%d-%s", nano+i, encodeSource(source))
		if _, err := os.Stat(filepath.Join(s.Dir, id+ext)); errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	if err := os.WriteFile(filepath.Join(s.Dir, id+ext), data, 0o644); err != nil {
		return "", fmt.Errorf("backup: write: %w", err)
	}
	// Retention is best-effort: a pruning failure must not block the write.
	_ = s.prune()
	return id, nil
}

// prune deletes the oldest snapshots so at most Keep remain.
func (s Store) prune() error {
	if s.Keep <= 0 {
		return nil
	}
	list, err := s.List()
	if err != nil {
		return err
	}
	for i := s.Keep; i < len(list); i++ {
		_ = os.Remove(list[i].Path)
	}
	return nil
}

// Drop deletes the snapshot identified by id. A missing snapshot is not an
// error, so a restore-then-drop sequence is safe to repeat.
func (s Store) Drop(id string) error {
	if _, ok := decodeSource(id); !ok {
		return fmt.Errorf("backup: invalid id %q", id)
	}
	if err := os.Remove(filepath.Join(s.Dir, id+ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup: drop: %w", err)
	}
	return nil
}

// List returns stored snapshots, newest first.
func (s Store) List() ([]Entry, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("backup: list: %w", err)
	}
	var out []Entry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ext)
		source, ok := decodeSource(id)
		if !ok {
			continue
		}
		en := Entry{ID: id, Path: filepath.Join(s.Dir, e.Name()), Source: source}
		if fi, err := e.Info(); err == nil {
			en.ModTime = fi.ModTime()
			en.Size = fi.Size()
		}
		out = append(out, en)
	}
	// Ids start with a nanosecond timestamp, so lexical order is time order.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// Latest returns the newest snapshot, or ok=false when there are none.
func (s Store) Latest() (Entry, bool, error) {
	list, err := s.List()
	if err != nil || len(list) == 0 {
		return Entry{}, false, err
	}
	return list[0], true, nil
}

// Restore copies the snapshot identified by id back over its source file and
// returns the affected entry.
func (s Store) Restore(id string) (Entry, error) {
	source, ok := decodeSource(id)
	if !ok {
		return Entry{}, fmt.Errorf("backup: invalid id %q", id)
	}
	path := filepath.Join(s.Dir, id+ext)
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, fmt.Errorf("backup: restore: %w", err)
	}
	dest, err := resolve(s.Root, source)
	if err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Entry{}, fmt.Errorf("backup: restore: %w", err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return Entry{}, fmt.Errorf("backup: restore: %w", err)
	}
	return Entry{ID: id, Path: path, Source: source}, nil
}

func encodeSource(source string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(source))
}

func decodeSource(id string) (string, bool) {
	i := strings.IndexByte(id, '-')
	if i < 0 {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(id[i+1:])
	if err != nil {
		return "", false
	}
	return string(b), true
}

// resolve joins root and rel and rejects any result that escapes root.
func resolve(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("backup: empty source path")
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(base, rel))
	if err != nil {
		return "", err
	}
	relToBase, err := filepath.Rel(base, abs)
	if err != nil {
		return "", err
	}
	if relToBase == ".." || strings.HasPrefix(relToBase, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("backup: path %q escapes project root", rel)
	}
	return abs, nil
}
