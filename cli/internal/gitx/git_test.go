package gitx

import (
	"path/filepath"
	"testing"

	"reqboard/internal/testutil"
)

func TestCollectUsesMergeBaseWhenBaseBranchAdvanced(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	testutil.Write(t, filepath.Join(repo, "a.txt"), "base\n")
	testutil.Git(t, repo, "init", "-q", "-b", "master")
	testutil.Git(t, repo, "config", "user.email", "test@example.com")
	testutil.Git(t, repo, "config", "user.name", "ReqBoard Test")
	testutil.Git(t, repo, "add", "-A")
	testutil.Git(t, repo, "commit", "-q", "-m", "base")

	testutil.Git(t, repo, "checkout", "-q", "-b", "feature")
	testutil.Write(t, filepath.Join(repo, "a.txt"), "base\nfeature\n")
	testutil.Git(t, repo, "add", "-A")
	testutil.Git(t, repo, "commit", "-q", "-m", "feature change")

	// The base branch advances after the feature branch was cut. A two-dot
	// diff against master would report b.txt as a reverse deletion.
	testutil.Git(t, repo, "checkout", "-q", "master")
	testutil.Write(t, filepath.Join(repo, "b.txt"), "master-only\n")
	testutil.Git(t, repo, "add", "-A")
	testutil.Git(t, repo, "commit", "-q", "-m", "master moved ahead")
	testutil.Git(t, repo, "checkout", "-q", "feature")

	// Uncommitted worktree changes must still be included.
	testutil.Write(t, filepath.Join(repo, "a.txt"), "base\nfeature\nworktree\n")

	files, err := Collect(repo, "master")
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range files {
		if fd.Path == "b.txt" {
			t.Fatalf("master-only commit leaked as a reverse deletion: %+v", fd)
		}
	}
	var a *FileDiff
	for i := range files {
		if files[i].Path == "a.txt" {
			a = &files[i]
		}
	}
	if a == nil {
		t.Fatalf("a.txt change missing: %+v", files)
	}
	if a.Added != 2 || a.Deleted != 0 {
		t.Fatalf("a.txt diff = +%d/-%d, want +2/-0 (feature commit + worktree)", a.Added, a.Deleted)
	}
}

func TestResolveBaseFallsBackGracefully(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	testutil.Write(t, filepath.Join(repo, "a.txt"), "base\n")
	testutil.Git(t, repo, "init", "-q", "-b", "master")
	testutil.Git(t, repo, "config", "user.email", "test@example.com")
	testutil.Git(t, repo, "config", "user.name", "ReqBoard Test")
	testutil.Git(t, repo, "add", "-A")
	testutil.Git(t, repo, "commit", "-q", "-m", "base")

	sha := ResolveBase(repo, "master")
	if len(sha) != 40 {
		t.Fatalf("ResolveBase(master) = %q, want a 40-char SHA", sha)
	}
	if got := ResolveBase(repo, "does-not-exist"); got != "does-not-exist" {
		t.Fatalf("ResolveBase fallback = %q, want the original ref", got)
	}
}
