// Package store owns every read/write of the .reqboard directory.
// Writes are serialized with flock and committed with temp-file + atomic rename.
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"reqboard/internal/model"
)

// DirName is the metadata directory inside the requirement worktree.
const DirName = ".reqboard"

// Store is rooted at the requirement worktree (the directory that contains
// .reqboard).
type Store struct {
	Root string
}

// Open returns a store for an existing .reqboard workspace.
func Open(root string) (*Store, error) {
	s := &Store{Root: root}
	if !s.Exists() {
		return nil, fmt.Errorf("no %s directory under %s (run `reqboard init --root %s` first)", DirName, root, root)
	}
	return s, nil
}

// Exists reports whether .reqboard exists under the root.
func (s *Store) Exists() bool {
	st, err := os.Stat(s.Dir())
	return err == nil && st.IsDir()
}

// Dir returns the .reqboard directory.
func (s *Store) Dir() string { return filepath.Join(s.Root, DirName) }

// Path returns a file inside .reqboard.
func (s *Store) Path(name string) string { return filepath.Join(s.Dir(), name) }

// ConfigPath is config.json.
func (s *Store) ConfigPath() string { return s.Path("config.json") }

// PRDPath is prd.md.
func (s *Store) PRDPath() string { return s.Path("prd.md") }

// DAGPath is dag.json.
func (s *Store) DAGPath() string { return s.Path("dag.json") }

// BindingsPath is bindings.json.
func (s *Store) BindingsPath() string { return s.Path("bindings.json") }

// EventsPath is events.jsonl.
func (s *Store) EventsPath() string { return s.Path("events.jsonl") }

// Init creates the workspace files.
func Init(root, name string, repos []model.Repo) (*Store, error) {
	if repos == nil {
		repos = []model.Repo{}
	}
	s := &Store{Root: root}
	if s.Exists() {
		return nil, fmt.Errorf("%s already exists under %s", DirName, root)
	}
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		return nil, err
	}
	unlock, err := s.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Re-check under the lock: another process may have initialized first.
	if _, err := os.Stat(s.ConfigPath()); err == nil {
		return nil, fmt.Errorf("%s already exists under %s", DirName, root)
	}
	cfg := &model.Config{
		SchemaVersion:   model.SchemaVersion,
		RequirementName: name,
		Root:            root,
		Repos:           repos,
		IgnoreGlobs:     append([]string{}, model.DefaultIgnoreGlobs...),
		CreatedAt:       model.Now(),
	}
	if err := s.SaveConfig(cfg); err != nil {
		return nil, err
	}
	if err := s.WriteFile("prd.md", []byte("")); err != nil {
		return nil, err
	}
	if err := s.SaveDAG(model.NewDAG(name)); err != nil {
		return nil, err
	}
	if err := s.SaveBindings(model.NewBindings()); err != nil {
		return nil, err
	}
	if err := s.WriteFile("events.jsonl", []byte("")); err != nil {
		return nil, err
	}
	_ = s.AppendEvent(Event{Actor: "cli", Action: "workspace.init", Detail: "initialized " + name})
	return s, nil
}

// Find walks up from start looking for a .reqboard directory.
func Find(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	cur := abs
	for {
		if st, err := os.Stat(filepath.Join(cur, DirName)); err == nil && st.IsDir() {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no %s found walking up from %s", DirName, abs)
		}
		cur = parent
	}
}

// Lock takes an exclusive flock on .reqboard/.lock. The returned func releases it.
func (s *Store) Lock() (func(), error) {
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.Path(".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// WithLock runs fn while holding the workspace lock.
func (s *Store) WithLock(fn func() error) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// WriteFile atomically writes a file inside .reqboard.
func (s *Store) WriteFile(name string, data []byte) error {
	return s.atomicWrite(s.Path(name), data)
}

func (s *Store) atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ReadFile reads a file inside .reqboard.
func (s *Store) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(s.Path(name))
}

// LoadConfig reads config.json.
func (s *Store) LoadConfig() (*model.Config, error) {
	var c model.Config
	if err := s.readJSON("config.json", &c); err != nil {
		return nil, err
	}
	if len(c.IgnoreGlobs) == 0 {
		c.IgnoreGlobs = append([]string{}, model.DefaultIgnoreGlobs...)
	}
	return &c, nil
}

// SaveConfig writes config.json.
func (s *Store) SaveConfig(c *model.Config) error {
	c.UpdatedAt = model.Now()
	return s.writeJSON("config.json", c)
}

// LoadDAG reads dag.json.
func (s *Store) LoadDAG() (*model.DAG, error) {
	var d model.DAG
	if err := s.readJSON("dag.json", &d); err != nil {
		return nil, err
	}
	if d.Nodes == nil {
		d.Nodes = []*model.Node{}
	}
	if d.Edges == nil {
		d.Edges = []*model.Edge{}
	}
	if d.IgnoredPRDRefs == nil {
		d.IgnoredPRDRefs = []string{}
	}
	return &d, nil
}

// SaveDAG writes dag.json.
func (s *Store) SaveDAG(d *model.DAG) error {
	d.SchemaVersion = model.SchemaVersion
	return s.writeJSON("dag.json", d)
}

// LoadBindings reads bindings.json.
func (s *Store) LoadBindings() (*model.Bindings, error) {
	var b model.Bindings
	if err := s.readJSON("bindings.json", &b); err != nil {
		return nil, err
	}
	if b.Leases == nil {
		b.Leases = []model.Lease{}
	}
	if b.Bindings == nil {
		b.Bindings = []model.Binding{}
	}
	return &b, nil
}

// SaveBindings writes bindings.json.
func (s *Store) SaveBindings(b *model.Bindings) error {
	b.SchemaVersion = model.SchemaVersion
	return s.writeJSON("bindings.json", b)
}

func (s *Store) readJSON(name string, v interface{}) error {
	data, err := os.ReadFile(s.Path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s is missing; run `reqboard init` or restore the workspace", name)
		}
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}
	return nil
}

func (s *Store) writeJSON(name string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return s.WriteFile(name, data)
}

// Event is one line in events.jsonl. It doubles as the hot-reload stream for
// the VS Code board.
type Event struct {
	TS     string                 `json:"ts"`
	Actor  string                 `json:"actor"`
	Action string                 `json:"action"`
	NodeID string                 `json:"node_id,omitempty"`
	EdgeID string                 `json:"edge_id,omitempty"`
	Detail string                 `json:"detail,omitempty"`
	Meta   map[string]interface{} `json:"meta,omitempty"`
}

// AppendEvent appends one event line. Callers should normally hold the lock.
func (s *Store) AppendEvent(e Event) error {
	if e.TS == "" {
		e.TS = model.Now()
	}
	if e.Actor == "" {
		e.Actor = "agent"
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.EventsPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// Events returns up to limit most recent events (limit <= 0 means all).
func (s *Store) Events(limit int) ([]Event, error) {
	f, err := os.Open(s.EventsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []Event{}, nil
		}
		return nil, err
	}
	defer f.Close()
	var all []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		all = append(all, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

// EventsForNode returns recent events touching a node (human edits are used for
// the [人工最新调整] injection on claim).
func (s *Store) EventsForNode(nodeID string, limit int) ([]Event, error) {
	all, err := s.Events(0)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range all {
		if e.NodeID == nodeID {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// RepoPath resolves a configured repo to an absolute filesystem path.
func RepoPath(root string, r model.Repo) string {
	if filepath.IsAbs(r.Path) {
		return r.Path
	}
	return filepath.Join(root, r.Path)
}

// SortedRepos returns repos sorted by name for deterministic output.
func SortedRepos(repos []model.Repo) []model.Repo {
	out := append([]model.Repo{}, repos...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DetectBaseRef guesses a sensible base ref for a repo.
func DetectBaseRef(repoPath string) string {
	for _, ref := range []string{"origin/master", "origin/main", "master", "main", "HEAD"} {
		if runGit(repoPath, "rev-parse", "--verify", "--quiet", ref) == nil {
			return ref
		}
	}
	return "HEAD"
}

func runGit(repoPath string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	return cmd.Run()
}
