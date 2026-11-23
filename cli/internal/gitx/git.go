// Package gitx runs real git diffs and exposes hunks for attribution.
package gitx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Hunk is one unified diff hunk.
type Hunk struct {
	OldStart int      `json:"old_start"`
	OldCount int      `json:"old_count"`
	NewStart int      `json:"new_start"`
	NewCount int      `json:"new_count"`
	Header   string   `json:"header"`
	Lines    []string `json:"lines,omitempty"`
}

// FileDiff is the change set of one file relative to the base ref.
type FileDiff struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"` // added | modified | deleted | untracked
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Binary    bool   `json:"binary,omitempty"`
	Untracked bool   `json:"untracked,omitempty"`
	Hunks     []Hunk `json:"hunks,omitempty"`
}

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// MergeBase returns the common ancestor of ref and HEAD. Diffing against this
// SHA is the three-dot semantics: it includes committed branch changes plus
// uncommitted working-tree changes, without reporting base-branch-only commits
// as reverse deletions.
func MergeBase(repoPath, ref string) (string, error) {
	out, err := run(repoPath, "merge-base", ref, "HEAD")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("git merge-base %s HEAD returned no ancestor", ref)
	}
	return sha, nil
}

// ResolveBase returns the merge-base SHA for baseRef, falling back to the
// original ref when merge-base cannot be computed (detached/odd histories).
func ResolveBase(repoPath, baseRef string) string {
	if strings.TrimSpace(baseRef) == "" {
		return "HEAD"
	}
	if sha, err := MergeBase(repoPath, baseRef); err == nil && sha != "" {
		return sha
	}
	return baseRef
}

// Collect returns all working-tree changes of a repo against baseRef, including
// untracked files. Generated files are NOT filtered here (the audit layer does
// that so callers can see the raw set).
func Collect(repoPath, baseRef string) ([]FileDiff, error) {
	if baseRef == "" {
		baseRef = "HEAD"
	}
	base := ResolveBase(repoPath, baseRef)
	out, err := run(repoPath, "diff", "--unified=0", "--no-color", "--no-ext-diff", "--no-prefix", base, "--")
	if err != nil {
		return nil, fmt.Errorf("git diff in %s: %w", repoPath, err)
	}
	files := parseDiff(out)

	untracked, err := run(repoPath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %w", repoPath, err)
	}
	for _, p := range strings.Split(untracked, "\x00") {
		if p == "" {
			continue
		}
		abs := filepath.Join(repoPath, p)
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if bytes.IndexByte(data, 0) >= 0 {
			files = append(files, FileDiff{Path: p, Status: "untracked", Untracked: true, Binary: true})
			continue
		}
		n := countLines(data)
		files = append(files, FileDiff{Path: p, Status: "untracked", Untracked: true, Added: n})
	}
	return files, nil
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte("\n"))
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func parseDiff(out string) []FileDiff {
	var files []FileDiff
	var cur *FileDiff
	var hunk *Hunk
	finalizeHunk := func() {
		if cur != nil && hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
			hunk = nil
		}
	}
	finalizeFile := func() {
		finalizeHunk()
		if cur == nil {
			return
		}
		if cur.Path == "" {
			cur.Path = cur.OldPath
		}
		if cur.Path == "" {
			cur = nil
			return
		}
		if cur.Status == "" {
			if cur.OldPath == "" || cur.OldPath == cur.Path {
				cur.Status = "modified"
			}
		}
		files = append(files, *cur)
		cur = nil
	}

	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			finalizeFile()
			cur = &FileDiff{Hunks: []Hunk{}}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
		case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
			cur.Binary = true
		case strings.HasPrefix(line, "--- "):
			p := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			if p != "/dev/null" {
				cur.OldPath = cleanPath(p)
			}
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if p == "/dev/null" {
				cur.Status = "deleted"
			} else {
				cur.Path = cleanPath(p)
			}
		case strings.HasPrefix(line, "@@ "):
			finalizeHunk()
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			os, _ := strconv.Atoi(m[1])
			oc := 1
			if m[2] != "" {
				oc, _ = strconv.Atoi(m[2])
			}
			ns, _ := strconv.Atoi(m[3])
			nc := 1
			if m[4] != "" {
				nc, _ = strconv.Atoi(m[4])
			}
			hunk = &Hunk{OldStart: os, OldCount: oc, NewStart: ns, NewCount: nc, Header: line, Lines: []string{}}
		case hunk != nil:
			hunk.Lines = append(hunk.Lines, line)
			if strings.HasPrefix(line, "+") {
				cur.Added++
			} else if strings.HasPrefix(line, "-") {
				cur.Deleted++
			}
		}
	}
	finalizeFile()
	return files
}

func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	if strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			p = u
		}
	}
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return filepath.ToSlash(p)
}

func run(repoPath string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%v: %s", err, msg)
		}
		return "", err
	}
	return string(out), nil
}

// ShowFile returns the content of a file at a ref.
func ShowFile(repoPath, ref, path string) ([]byte, error) {
	out, err := run(repoPath, "show", ref+":"+path)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// DiffFile returns hunks for one file at a chosen context size.
func DiffFile(repoPath, baseRef, path string, context int) ([]Hunk, error) {
	if context < 0 {
		context = 0
	}
	base := ResolveBase(repoPath, baseRef)
	out, err := run(repoPath, "diff", fmt.Sprintf("--unified=%d", context), "--no-color", "--no-ext-diff", "--no-prefix", base, "--", path)
	if err != nil {
		return nil, err
	}
	files := parseDiff(out)
	if len(files) == 0 {
		return []Hunk{}, nil
	}
	return files[0].Hunks, nil
}

// CollectFile returns a FileDiff for one file with contextLines of unified
// context, falling back to untracked file contents or clean working-tree
// contents when no diff hunks exist.
func CollectFile(repoPath, baseRef, relPath string, contextLines int) (*FileDiff, string, error) {
	if contextLines < 0 {
		contextLines = 3
	}
	if baseRef == "" {
		baseRef = "HEAD"
	}
	base := ResolveBase(repoPath, baseRef)
	out, err := run(repoPath, "diff", fmt.Sprintf("--unified=%d", contextLines), "--no-color", "--no-ext-diff", "--no-prefix", base, "--", relPath)
	if err != nil {
		return nil, base, err
	}
	files := parseDiff(out)
	if len(files) > 0 {
		fd := files[0]
		return &fd, base, nil
	}
	// File may be untracked or unchanged relative to merge-base.
	abs := filepath.Join(repoPath, filepath.FromSlash(relPath))
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		return &FileDiff{Path: relPath, Status: "clean", Hunks: []Hunk{}}, base, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return &FileDiff{Path: relPath, Status: "untracked", Binary: true}, base, nil
	}
	// Check if file is untracked by git.
	untrackedOut, _ := run(repoPath, "ls-files", "--others", "--exclude-standard", "--", relPath)
	isUntracked := strings.TrimSpace(untrackedOut) != ""
	rawLines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(data) == 0 {
		rawLines = nil
	}
	fd := &FileDiff{Path: relPath, Status: "clean", Hunks: []Hunk{}}
	if isUntracked {
		fd.Status = "untracked"
		fd.Untracked = true
		fd.Added = len(rawLines)
		hLines := make([]string, 0, len(rawLines))
		for _, l := range rawLines {
			hLines = append(hLines, "+"+l)
		}
		if len(hLines) > 0 {
			fd.Hunks = append(fd.Hunks, Hunk{
				OldStart: 0, OldCount: 0, NewStart: 1, NewCount: len(hLines),
				Header: fmt.Sprintf("@@ -0,0 +1,%d @@ (untracked new file)", len(hLines)),
				Lines:  hLines,
			})
		}
		return fd, base, nil
	}
	// Unchanged working file: return full file as context hunk so the viewer can still inspect it.
	if len(rawLines) > 0 {
		hLines := make([]string, 0, len(rawLines))
		for _, l := range rawLines {
			hLines = append(hLines, " "+l)
		}
		fd.Hunks = append(fd.Hunks, Hunk{
			OldStart: 1, OldCount: len(hLines), NewStart: 1, NewCount: len(hLines),
			Header: fmt.Sprintf("@@ -1,%d +1,%d @@ (no diff vs base)", len(hLines), len(hLines)),
			Lines:  hLines,
		})
	}
	return fd, base, nil
}

// IsRepo reports whether path is a git worktree.
func IsRepo(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	if err == nil {
		return true
	}
	// worktrees / submodules may use a .git file; git rev-parse is authoritative.
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-dir")
	return cmd.Run() == nil
}
