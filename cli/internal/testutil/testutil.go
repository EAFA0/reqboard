// Package testutil builds throwaway multi-repo git workspaces for tests.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"reqboard/internal/model"
	"reqboard/internal/prd"
	"reqboard/internal/store"
)

// Git runs git in dir and fails the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// Write writes a file, creating parent directories.
func Write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// NewRepo creates a git repo on master with one initial commit.
func NewRepo(t *testing.T, root, name string, files map[string]string) model.Repo {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "init", "-q", "-b", "master")
	Git(t, dir, "config", "user.email", "test@example.com")
	Git(t, dir, "config", "user.name", "ReqBoard Test")
	for p, content := range files {
		Write(t, filepath.Join(dir, p), content)
	}
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "-m", "init")
	return model.Repo{Name: name, Path: name, BaseRef: "master"}
}

// Commit commits all changes in a repo.
func Commit(t *testing.T, dir, msg string) {
	t.Helper()
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "-m", msg)
}

// InitWorkspace initializes .reqboard under root.
func InitWorkspace(t *testing.T, root, name string, repos []model.Repo) *store.Store {
	t.Helper()
	s, err := store.Init(root, name, repos)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// WritePRD parses markdown, injects anchors and writes .reqboard/prd.md.
func WritePRD(t *testing.T, s *store.Store, markdown string) *prd.Document {
	t.Helper()
	doc, err := prd.Parse([]byte(markdown))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile("prd.md", doc.Render()); err != nil {
		t.Fatal(err)
	}
	return doc
}

// SaveDAG writes dag.json.
func SaveDAG(t *testing.T, s *store.Store, d *model.DAG) {
	t.Helper()
	if err := s.SaveDAG(d); err != nil {
		t.Fatal(err)
	}
}

// SaveBindings writes bindings.json.
func SaveBindings(t *testing.T, s *store.Store, b *model.Bindings) {
	t.Helper()
	if err := s.SaveBindings(b); err != nil {
		t.Fatal(err)
	}
}

// AddNode appends a node to a DAG.
func AddNode(d *model.DAG, id, title, scope, status string, refs ...string) *model.Node {
	n := &model.Node{
		ID: id, Title: title, Scope: scope, Status: status,
		PRDRefs: refs, SelfMocks: []model.SelfMock{}, ReviewNotes: []model.ReviewNote{},
		UpdatedAt: model.Now(),
	}
	d.AddNode(n)
	return n
}

// AddEdge appends an edge to a DAG.
func AddEdge(d *model.DAG, id, from, to, kind, note string) *model.Edge {
	e := &model.Edge{ID: id, From: from, To: to, Kind: kind, Note: note, UpdatedBy: "test", UpdatedAt: model.Now()}
	d.AddEdge(e)
	return e
}
