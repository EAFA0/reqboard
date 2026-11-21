package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// UndoDirName is the snapshot directory inside .reqboard. It is runtime state:
// reqboard init writes a .reqboard/.gitignore so it never shows up as a code
// change.
const UndoDirName = ".undo"

// MaxUndoDepth is the number of undo steps kept on disk.
const MaxUndoDepth = 20

var snapshotFiles = []string{"dag.json", "bindings.json", "config.json"}

var snapshotSeq uint64

// Snapshot captures the three mutable state files before one logical write.
// A nil file body means the file did not exist at snapshot time.
type Snapshot struct {
	ID    string             `json:"id"`
	TS    string             `json:"ts"`
	Label string             `json:"label,omitempty"`
	Files map[string]*string `json:"files"`
}

// UndoDir returns the .reqboard/.undo directory.
func (s *Store) UndoDir() string { return filepath.Join(s.Dir(), UndoDirName) }

func (s *Store) undoStackDir() string { return filepath.Join(s.UndoDir(), "undo") }
func (s *Store) redoStackDir() string { return filepath.Join(s.UndoDir(), "redo") }

// UndoDepth reports how many undo steps are available.
func (s *Store) UndoDepth() int { return countSnapshots(s.undoStackDir()) }

// RedoDepth reports how many redo steps are available.
func (s *Store) RedoDepth() int { return countSnapshots(s.redoStackDir()) }

// ResetSnapshotTxn starts a fresh logical write: the next Save* call records
// the pre-state exactly once. CLI commands create a Store per invocation, so
// this is effectively per-command.
func (s *Store) ResetSnapshotTxn() {
	s.snapMu.Lock()
	s.snapTaken = false
	s.snapMu.Unlock()
}

// capturePreState records the current state once per logical write. It must be
// called before any of dag.json / bindings.json / config.json is replaced.
func (s *Store) capturePreState(label string) error {
	s.snapMu.Lock()
	defer s.snapMu.Unlock()
	if s.snapTaken {
		return nil
	}
	s.snapTaken = true
	if !s.anyStateFileExists() {
		return nil
	}
	snap, err := s.captureCurrent(label)
	if err != nil {
		return err
	}
	if err := s.pushSnapshot(s.undoStackDir(), snap); err != nil {
		return err
	}
	// A new action invalidates the redo branch.
	return clearSnapshots(s.redoStackDir())
}

func (s *Store) anyStateFileExists() bool {
	for _, name := range snapshotFiles {
		if _, err := os.Stat(s.Path(name)); err == nil {
			return true
		}
	}
	return false
}

func (s *Store) captureCurrent(label string) (*Snapshot, error) {
	now := time.Now()
	snap := &Snapshot{
		ID:    fmt.Sprintf("%020d-%06d", now.UnixNano(), atomic.AddUint64(&snapshotSeq, 1)%1000000),
		TS:    now.Format(time.RFC3339),
		Label: label,
		Files: map[string]*string{},
	}
	for _, name := range snapshotFiles {
		data, err := os.ReadFile(s.Path(name))
		if err != nil {
			if os.IsNotExist(err) {
				snap.Files[name] = nil
				continue
			}
			return nil, err
		}
		body := string(data)
		snap.Files[name] = &body
	}
	return snap, nil
}

func (s *Store) pushSnapshot(stackDir string, snap *Snapshot) error {
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	name := "snap-" + snap.ID + ".json"
	if err := s.atomicWrite(filepath.Join(stackDir, name), data); err != nil {
		return err
	}
	return pruneSnapshots(stackDir, MaxUndoDepth)
}

func snapshotFilesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "snap-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func countSnapshots(dir string) int { return len(snapshotFilesIn(dir)) }

func pruneSnapshots(dir string, max int) error {
	names := snapshotFilesIn(dir)
	if max <= 0 || len(names) <= max {
		return nil
	}
	for _, name := range names[:len(names)-max] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func clearSnapshots(dir string) error {
	for _, name := range snapshotFilesIn(dir) {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Store) latestSnapshot(dir string) (string, *Snapshot, error) {
	names := snapshotFilesIn(dir)
	if len(names) == 0 {
		return "", nil, nil
	}
	path := filepath.Join(dir, names[len(names)-1])
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return "", nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return path, &snap, nil
}

func (s *Store) restoreSnapshot(snap *Snapshot) error {
	for _, name := range snapshotFiles {
		body, ok := snap.Files[name]
		if !ok {
			continue
		}
		if body == nil {
			if err := os.Remove(s.Path(name)); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := s.atomicWrite(s.Path(name), []byte(*body)); err != nil {
			return err
		}
	}
	return nil
}

// Undo restores the latest pre-write snapshot and pushes the current state onto
// the redo stack. Callers must hold the workspace flock.
func (s *Store) Undo() (*Snapshot, int, int, error) {
	path, snap, err := s.latestSnapshot(s.undoStackDir())
	if err != nil {
		return nil, s.UndoDepth(), s.RedoDepth(), err
	}
	if snap == nil {
		return nil, s.UndoDepth(), s.RedoDepth(), fmt.Errorf("没有可撤销的操作")
	}
	current, err := s.captureCurrent("undo")
	if err != nil {
		return nil, s.UndoDepth(), s.RedoDepth(), err
	}
	if err := s.pushSnapshot(s.redoStackDir(), current); err != nil {
		return nil, s.UndoDepth(), s.RedoDepth(), err
	}
	if err := s.restoreSnapshot(snap); err != nil {
		return nil, s.UndoDepth(), s.RedoDepth(), err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return snap, s.UndoDepth(), s.RedoDepth(), err
	}
	return snap, s.UndoDepth(), s.RedoDepth(), nil
}

// Redo replays the latest undone snapshot and pushes the current state back on
// the undo stack. Callers must hold the workspace flock.
func (s *Store) Redo() (*Snapshot, int, int, error) {
	path, snap, err := s.latestSnapshot(s.redoStackDir())
	if err != nil {
		return nil, s.UndoDepth(), s.RedoDepth(), err
	}
	if snap == nil {
		return nil, s.UndoDepth(), s.RedoDepth(), fmt.Errorf("没有可重做的操作")
	}
	current, err := s.captureCurrent("redo")
	if err != nil {
		return nil, s.UndoDepth(), s.RedoDepth(), err
	}
	if err := s.pushSnapshot(s.undoStackDir(), current); err != nil {
		return nil, s.UndoDepth(), s.RedoDepth(), err
	}
	if err := s.restoreSnapshot(snap); err != nil {
		return nil, s.UndoDepth(), s.RedoDepth(), err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return snap, s.UndoDepth(), s.RedoDepth(), err
	}
	return snap, s.UndoDepth(), s.RedoDepth(), nil
}
