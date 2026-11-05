package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gofrs/flock"
)

const (
	stateFile     = "state.json"
	lockFile      = "state.lock"
	tmpPattern    = "state.json.tmp-*"
	corruptPrefix = "state.json.corrupt-"
)

// ErrLocked is returned by Open when another process holds the store.
var ErrLocked = errors.New("store: state is in use by another pr-relay process")

// ErrNotDurable is wrapped by a Save error that happened after the rename:
// state.json already holds the new state, but the directory fsync that makes
// the rename durable failed. Callers must treat the new state as committed
// (swap it in memory) rather than keep the old one, or memory falls behind
// disk (review WR-05).
var ErrNotDurable = errors.New("store: state saved but the rename may not be durable")

// ErrNewerVersion is returned by Open when state.json was written by a newer
// pr-relay. The file is left untouched so a downgrade cannot wipe state.
var ErrNewerVersion = errors.New("store: state file was written by a newer pr-relay")

// Store owns a state directory for the lifetime of one process. It holds an
// exclusive flock on state.lock until Close.
type Store struct {
	dir             string
	lock            *flock.Flock
	quarantinedPath string
	quarantinedAt   time.Time
}

// Open takes the store lock in dir (creating dir with mode 0700), removes
// leftover temp files and loads state.json.
//
// A missing state.json yields NewState. An unreadable one (bad JSON, missing
// or zero version) is renamed to state.json.corrupt-<UTC timestamp> and Open
// continues with NewState; Quarantine reports where it went. A file with a
// version newer than SchemaVersion is refused with ErrNewerVersion. Another
// process holding the lock yields ErrLocked.
func Open(dir string) (*Store, *State, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("store: open: mkdir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("store: open: chmod dir: %w", err)
	}
	lockPath := filepath.Join(dir, lockFile)
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("store: open: create lock: %w", err)
	}
	cerr := lf.Chmod(0o600)
	_ = lf.Close()
	if cerr != nil {
		return nil, nil, fmt.Errorf("store: open: chmod lock: %w", cerr)
	}

	lk := flock.New(lockPath)
	ok, err := lk.TryLock()
	if err != nil {
		_ = lk.Close()
		return nil, nil, fmt.Errorf("store: open: lock: %w", err)
	}
	if !ok {
		_ = lk.Close()
		return nil, nil, ErrLocked
	}
	s := &Store{dir: dir, lock: lk}

	st, err := s.load()
	if err != nil {
		_ = s.Close()
		return nil, nil, err
	}
	return s, st, nil
}

// load removes leftover temp files and reads state.json. Caller holds the lock.
func (s *Store) load() (*State, error) {
	tmps, err := filepath.Glob(filepath.Join(s.dir, tmpPattern))
	if err != nil {
		return nil, fmt.Errorf("store: open: glob temp files: %w", err)
	}
	for _, t := range tmps {
		if err := os.Remove(t); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("store: open: remove temp file: %w", err)
		}
	}

	path := filepath.Join(s.dir, stateFile)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewState(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: open: read: %w", err)
	}

	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil || head.Version < 1 {
		return s.quarantine(path)
	}
	if head.Version > SchemaVersion {
		return nil, fmt.Errorf("%w (found version %d, this binary supports up to %d)",
			ErrNewerVersion, head.Version, SchemaVersion)
	}
	raw, err = migrate(head.Version, raw)
	if err != nil {
		return s.quarantine(path)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return s.quarantine(path)
	}
	normalize(&st)
	return &st, nil
}

// migrate upgrades raw state of the given version to SchemaVersion, one
// version step at a time. Version 3 is current. Every step so far is
// additive and rewrites only the "version" key, leaving every other byte of
// data as it was:
//
//   - 1 -> 2 adds the optional Subscription.AgentKind field.
//   - 2 -> 3 adds the optional Subscription lifecycle fields LastEventAt,
//     LastEventType, CheckinAt, Ending and EndSeq, all zero for old files.
func migrate(version int, raw []byte) ([]byte, error) {
	if version < 1 || version > SchemaVersion {
		return nil, fmt.Errorf("store: migrate: no migration from version %d", version)
	}
	for version < SchemaVersion {
		switch version {
		case 1, 2:
			out, err := bumpVersion(raw, version+1)
			if err != nil {
				return nil, fmt.Errorf("store: migrate: v%d: %w", version, err)
			}
			raw = out
		default:
			return nil, fmt.Errorf("store: migrate: no migration from version %d", version)
		}
		version++
	}
	return raw, nil
}

// bumpVersion rewrites only the top-level "version" key of raw to v.
func bumpVersion(raw []byte, v int) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["version"] = json.RawMessage(strconv.Itoa(v))
	return json.Marshal(m)
}

// normalize makes nil maps empty and drops null records so callers can
// mutate a loaded State without nil checks.
func normalize(st *State) {
	if st.PRs == nil {
		st.PRs = map[string]*PRRecord{}
	}
	if st.Subs == nil {
		st.Subs = map[string]*Subscription{}
	}
	for k, r := range st.PRs {
		if r == nil {
			delete(st.PRs, k)
			continue
		}
		r.Memory = r.Memory.Clone()
	}
	for k, sub := range st.Subs {
		if sub == nil {
			delete(st.Subs, k)
		}
	}
}

// quarantine renames an unreadable state file out of the way (D-20) and
// returns an empty state. The caller is responsible for logging.
func (s *Store) quarantine(path string) (*State, error) {
	now := time.Now().UTC()
	base := filepath.Join(s.dir, corruptPrefix+now.Format("20060102T150405Z"))
	dst := base
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			break
		}
		dst = base + "-" + strconv.Itoa(i)
	}
	if err := os.Rename(path, dst); err != nil {
		return nil, fmt.Errorf("store: open: quarantine: %w", err)
	}
	s.quarantinedPath = dst
	s.quarantinedAt = now
	return NewState(), nil
}

// Save atomically replaces state.json with st: write a 0600 temp file,
// fsync it (F_FULLFSYNC on darwin), rename it over state.json, then fsync the
// directory so the rename itself is durable. On a failure before the rename
// the previous state.json is left intact. A failure after the rename (opening,
// syncing or closing the directory) wraps ErrNotDurable: state.json then
// already holds st. Save never logs state content.
func (s *Store) Save(st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("store: save: marshal: %w", err)
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(s.dir, tmpPattern)
	if err != nil {
		return fmt.Errorf("store: save: create temp: %w", err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after a successful rename
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: save: chmod: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: save: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: save: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: save: close: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, stateFile)); err != nil {
		return fmt.Errorf("store: save: rename: %w", err)
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("store: save: open dir: %w: %w", ErrNotDurable, err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("store: save: sync dir: %w: %w", ErrNotDurable, err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("store: save: close dir: %w: %w", ErrNotDurable, err)
	}
	return nil
}

// Quarantine reports where an unreadable state file was moved during Open and
// when, or "" and the zero time if nothing was quarantined.
func (s *Store) Quarantine() (path string, at time.Time) {
	return s.quarantinedPath, s.quarantinedAt
}

// Dir returns the state directory.
func (s *Store) Dir() string { return s.dir }

// Close releases the store lock.
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Unlock()
	if cerr := s.lock.Close(); err == nil {
		err = cerr
	}
	s.lock = nil
	if err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}
