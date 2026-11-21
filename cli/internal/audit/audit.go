// Package audit reconciles real multi-repo git diffs against bindings and
// produces the four buckets plus broken-binding diagnostics.
package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"reqboard/internal/astx"
	"reqboard/internal/gitx"
	"reqboard/internal/model"
	"reqboard/internal/pathmatch"
	"reqboard/internal/store"
)

// Entry is one changed symbol (or file-level change).
type Entry struct {
	Repo          string   `json:"repo"`
	File          string   `json:"file"`
	Symbol        string   `json:"symbol"`
	Kind          string   `json:"kind,omitempty"`
	Start         int      `json:"start,omitempty"`
	End           int      `json:"end,omitempty"`
	Added         int      `json:"added"`
	Deleted       int      `json:"deleted"`
	NodeIDs       []string `json:"node_ids,omitempty"`
	BindingIDs    []string `json:"binding_ids,omitempty"`
	Roles         []string `json:"roles,omitempty"`
	LeaseNodes    []string `json:"lease_nodes,omitempty"`
	LeaseSessions []string `json:"lease_sessions,omitempty"`
	Untracked     bool     `json:"untracked,omitempty"`
	HintPresent   *bool    `json:"hint_present,omitempty"`
	Correction    string   `json:"correction,omitempty"`
	AddedText     []string `json:"-"`
}

// BrokenBinding describes a binding whose target no longer exists.
type BrokenBinding struct {
	BindingID  string   `json:"binding_id"`
	NodeID     string   `json:"node_id"`
	Repo       string   `json:"repo"`
	File       string   `json:"file"`
	Symbol     string   `json:"symbol"`
	Reason     string   `json:"reason"`
	Available  []string `json:"available_symbols,omitempty"`
	Correction string   `json:"correction,omitempty"`
}

// NodeStat is the per-node +/- roll-up.
type NodeStat struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
	Files   int `json:"files"`
}

// RepoInfo reports per-repo collection status.
type RepoInfo struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	BaseRef      string `json:"base_ref"`
	ChangedFiles int    `json:"changed_files"`
	Error        string `json:"error,omitempty"`
}

// Report is the full audit result.
type Report struct {
	Attributed   []Entry             `json:"attributed"`
	ToDelete     []Entry             `json:"to_delete"`
	Leased       []Entry             `json:"leased"`
	Orphan       []Entry             `json:"orphan"`
	Broken       []BrokenBinding     `json:"broken_bindings"`
	NodeStats    map[string]NodeStat `json:"node_stats"`
	Repos        []RepoInfo          `json:"repos"`
	Ignored      []string            `json:"ignored_files,omitempty"`
	ChangedFiles int                 `json:"changed_files"`
	Violations   int                 `json:"violations"`
	Scope        string              `json:"scope,omitempty"`
	Filter       Filter              `json:"filter"`
}

// Filter is the effective diff filter echoed to the UI so it can render and
// round-trip the current configuration.
type Filter struct {
	ExcludeTests bool     `json:"exclude_tests"`
	IncludeGlobs []string `json:"include_globs"`
	IgnoreGlobs  []string `json:"ignore_globs"`
}

// EffectiveFilter resolves defaults for a config (nil slices become []).
func EffectiveFilter(cfg *model.Config) Filter {
	f := Filter{ExcludeTests: cfg.ShouldExcludeTests(), IncludeGlobs: []string{}, IgnoreGlobs: []string{}}
	if cfg != nil {
		if len(cfg.IncludeGlobs) > 0 {
			f.IncludeGlobs = append([]string{}, cfg.IncludeGlobs...)
		}
		if len(cfg.IgnoreGlobs) > 0 {
			f.IgnoreGlobs = append([]string{}, cfg.IgnoreGlobs...)
		}
	}
	return f
}

// defaultMockGlobs are the generated-mock patterns that used to swallow
// hand-written mocks. An explicit binding (role mock/impl) wins over them.
var defaultMockGlobs = []string{"**/*_mock.go", "**/mock_*.go"}

// explicitlyBound reports whether the exact repo file was explicitly bound,
// optionally restricted to the given roles.
func explicitlyBound(bindings *model.Bindings, repo, path string, roles ...string) bool {
	if bindings == nil {
		return false
	}
	for _, b := range bindings.Bindings {
		if b.Repo != repo || filepath.ToSlash(b.File) != path {
			continue
		}
		if len(roles) == 0 {
			return true
		}
		for _, role := range roles {
			if b.Role == role {
				return true
			}
		}
	}
	return false
}

func isDefaultMockGlob(glob string) bool {
	glob = strings.TrimSpace(glob)
	for _, g := range defaultMockGlobs {
		if glob == g {
			return true
		}
	}
	return false
}

// shouldIgnore reports whether a changed file is filtered out of the audit.
// exclude_tests applies globally (even to previously bound test files) so unit
// tests never pollute diff stats or review lists unless opted back in. Hand-
// written mocks explicitly bound as mock/impl still win over default mock globs.
func shouldIgnore(cfg *model.Config, bindings *model.Bindings, repo, path string) bool {
	boundMockOrImpl := explicitlyBound(bindings, repo, path, model.RoleMock, model.RoleImpl)
	if cfg.ShouldExcludeTests() && pathmatch.MatchAny(model.DefaultTestGlobs, path) {
		return true
	}
	for _, glob := range cfg.IgnoreGlobs {
		if !pathmatch.Match(glob, path) {
			continue
		}
		if isDefaultMockGlob(glob) && boundMockOrImpl {
			continue
		}
		return true
	}
	if len(cfg.IncludeGlobs) > 0 && !matchInclude(cfg.IncludeGlobs, repo, path) {
		return true
	}
	return false
}

// shouldIgnoreBinding reports whether a stored binding is hidden by the active
// filter (so toggling exclude_tests or include/exclude never triggers false
// Broken Binding violations).
func shouldIgnoreBinding(cfg *model.Config, bindings *model.Bindings, b model.Binding) bool {
	p := filepath.ToSlash(b.File)
	if cfg.ShouldExcludeTests() && (b.Role == model.RoleTest || pathmatch.MatchAny(model.DefaultTestGlobs, p)) {
		return true
	}
	return shouldIgnore(cfg, bindings, b.Repo, p)
}

// matchInclude matches path-style globs and repo-qualified `repo:glob` entries.
func matchInclude(globs []string, repo, path string) bool {
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if i := strings.Index(g, ":"); i >= 0 {
			if g[:i] == repo && pathmatch.Match(g[i+1:], path) {
				return true
			}
			continue
		}
		if pathmatch.Match(g, path) {
			return true
		}
	}
	return false
}

// Options scopes the audit.
type Options struct {
	NodeID string // "" means --all
	All    bool
}

// Run audits all configured repos.
func Run(cfg *model.Config, root string, dag *model.DAG, bindings *model.Bindings, opts Options) *Report {
	rep := &Report{
		Attributed: []Entry{},
		ToDelete:   []Entry{},
		Leased:     []Entry{},
		Orphan:     []Entry{},
		Broken:     []BrokenBinding{},
		NodeStats:  map[string]NodeStat{},
		Repos:      []RepoInfo{},
		Ignored:    []string{},
		Filter:     EffectiveFilter(cfg),
	}
	if opts.NodeID != "" {
		rep.Scope = opts.NodeID
	} else {
		rep.Scope = "all"
	}

	var node *model.Node
	if opts.NodeID != "" {
		node = dag.Node(opts.NodeID)
	}

	attr := map[string]*Entry{}
	del := map[string]*Entry{}
	attrOrder := []string{}
	delOrder := []string{}

	for _, repo := range store.SortedRepos(cfg.Repos) {
		info := RepoInfo{Name: repo.Name, BaseRef: repo.BaseRef}
		repoPath := store.RepoPath(root, repo)
		info.Path = repoPath
		if _, err := os.Stat(repoPath); err != nil {
			info.Error = "repo path not found: " + repoPath
			rep.Repos = append(rep.Repos, info)
			continue
		}
		base := repo.BaseRef
		if base == "" {
			base = "HEAD"
			info.BaseRef = base
		}
		// Three-dot diff: compare against the common ancestor so a base branch
		// that advanced ahead of the feature branch never shows up as reverse
		// deletions.
		base = gitx.ResolveBase(repoPath, base)
		files, err := gitx.Collect(repoPath, base)
		if err != nil {
			info.Error = err.Error()
			rep.Repos = append(rep.Repos, info)
			continue
		}
		for _, fd := range files {
			p := filepath.ToSlash(fd.Path)
			if shouldIgnore(cfg, bindings, repo.Name, p) {
				rep.Ignored = append(rep.Ignored, repo.Name+":"+p)
				continue
			}
			info.ChangedFiles++
			rep.ChangedFiles++
			entries := projectFile(repo.Name, repoPath, base, fd)
			for _, e := range entries {
				classify(&e, dag, bindings, node, opts, rep, attr, del, &attrOrder, &delOrder)
			}
		}
		rep.Repos = append(rep.Repos, info)
	}

	rep.Attributed = collect(attr, attrOrder)
	rep.ToDelete = collect(del, delOrder)
	sortEntries(rep.Attributed)
	sortEntries(rep.ToDelete)
	sortEntries(rep.Leased)
	sortEntries(rep.Orphan)

	for _, e := range rep.Attributed {
		for _, id := range e.NodeIDs {
			st := rep.NodeStats[id]
			st.Added += e.Added
			st.Deleted += e.Deleted
			st.Files++
			rep.NodeStats[id] = st
		}
	}
	// Deprecated nodes still show their residual diff so the board can render
	// the To-Delete debt on the node card.
	for _, e := range rep.ToDelete {
		for _, id := range e.NodeIDs {
			st := rep.NodeStats[id]
			st.Added += e.Added
			st.Deleted += e.Deleted
			st.Files++
			rep.NodeStats[id] = st
		}
	}

	// Broken bindings are evaluated against the current tree, skipping bindings
	// hidden by the active diff filter (e.g. exclude_tests=true).
	for _, b := range bindings.Bindings {
		if opts.NodeID != "" && b.NodeID != opts.NodeID {
			continue
		}
		if shouldIgnoreBinding(cfg, bindings, b) {
			continue
		}
		if bb := checkBinding(cfg, root, b, dag); bb != nil {
			rep.Broken = append(rep.Broken, *bb)
		}
	}
	sort.SliceStable(rep.Broken, func(i, j int) bool {
		if rep.Broken[i].Repo != rep.Broken[j].Repo {
			return rep.Broken[i].Repo < rep.Broken[j].Repo
		}
		if rep.Broken[i].File != rep.Broken[j].File {
			return rep.Broken[i].File < rep.Broken[j].File
		}
		return rep.Broken[i].Symbol < rep.Broken[j].Symbol
	})

	repoErrors := 0
	for _, r := range rep.Repos {
		if r.Error != "" {
			repoErrors++
		}
	}
	rep.Violations = len(rep.Orphan) + len(rep.ToDelete) + len(rep.Broken) + repoErrors
	return rep
}

func collect(m map[string]*Entry, order []string) []Entry {
	out := make([]Entry, 0, len(order))
	for _, k := range order {
		if e, ok := m[k]; ok {
			out = append(out, *e)
		}
	}
	return out
}

func projectFile(repo, repoPath, base string, fd gitx.FileDiff) []Entry {
	path := filepath.ToSlash(fd.Path)
	abs := filepath.Join(repoPath, filepath.FromSlash(path))
	var src []byte
	var err error
	if fd.Status == "deleted" {
		src, err = gitx.ShowFile(repoPath, base, path)
	} else {
		src, err = os.ReadFile(abs)
	}
	if err != nil {
		return []Entry{{
			Repo: repo, File: path, Symbol: "(file)", Kind: "file",
			Added: fd.Added, Deleted: fd.Deleted, Untracked: fd.Untracked,
			Correction: "无法读取文件内容，请确认仓库路径与 base_ref",
		}}
	}
	if fd.Binary {
		return []Entry{{
			Repo: repo, File: path, Symbol: "(binary)", Kind: "file",
			Untracked: fd.Untracked,
		}}
	}
	syms, _ := astx.Symbols(path, src)
	hunks := fd.Hunks
	if fd.Untracked {
		lines := strings.Split(strings.TrimSuffix(string(src), "\n"), "\n")
		if len(src) == 0 {
			lines = nil
		}
		h := gitx.Hunk{NewStart: 1, NewCount: len(lines), Lines: make([]string, 0, len(lines))}
		for _, l := range lines {
			h.Lines = append(h.Lines, "+"+l)
		}
		hunks = []gitx.Hunk{h}
	}
	if len(hunks) == 0 {
		return []Entry{{
			Repo: repo, File: path, Symbol: "(file)", Kind: "file",
			Added: fd.Added, Deleted: fd.Deleted, Untracked: fd.Untracked,
		}}
	}
	hits := astx.Project(syms, hunks)
	out := make([]Entry, 0, len(hits))
	for _, h := range hits {
		out = append(out, Entry{
			Repo: repo, File: path, Symbol: h.Symbol.Name, Kind: h.Symbol.Kind,
			Start: h.Symbol.Start, End: h.Symbol.End,
			Added: h.Added, Deleted: h.Deleted,
			Untracked: fd.Untracked,
			AddedText: h.AddedLines,
		})
	}
	if len(out) == 0 {
		out = append(out, Entry{
			Repo: repo, File: path, Symbol: "(file)", Kind: "file",
			Added: fd.Added, Deleted: fd.Deleted, Untracked: fd.Untracked,
		})
	}
	return out
}

func bindingMatches(b model.Binding, e Entry, addedLines []string) (bool, bool) {
	if b.Repo != e.Repo || filepath.ToSlash(b.File) != e.File {
		return false, false
	}
	if b.Granularity == model.GranularityFile || b.Symbol == "" || e.Symbol == "(file)" || e.Symbol == "(binary)" {
		return true, false
	}
	if b.Symbol == e.Symbol {
		return true, hintPresent(b, addedLines)
	}
	if strings.HasSuffix(e.Symbol, "."+b.Symbol) {
		return true, hintPresent(b, addedLines)
	}
	return false, false
}

func hintPresent(b model.Binding, addedLines []string) bool {
	if b.AnchorHint == "" {
		return false
	}
	for _, l := range addedLines {
		if strings.Contains(l, b.AnchorHint) {
			return true
		}
	}
	return false
}

func classify(e *Entry, dag *model.DAG, bindings *model.Bindings, scopeNode *model.Node, opts Options, rep *Report, attr, del map[string]*Entry, attrOrder, delOrder *[]string) {
	matchedAny := false
	for _, b := range bindings.Bindings {
		ok, hint := bindingMatches(b, *e, e.AddedText)
		if !ok {
			continue
		}
		matchedAny = true
		n := dag.Node(b.NodeID)
		if n == nil {
			continue
		}
		if n.Status == model.StatusDeprecated {
			if scopeNode != nil && n.ID != scopeNode.ID {
				continue
			}
			key := e.Repo + "|" + e.File + "|" + e.Symbol
			if _, exists := del[key]; !exists {
				cp := *e
				cp.Correction = fmt.Sprintf("删除 %s:%s::%s 的残留代码后运行 `reqboard unbind %s`（或改绑到有效节点）", e.Repo, e.File, e.Symbol, b.ID)
				del[key] = &cp
				*delOrder = append(*delOrder, key)
			}
			d := del[key]
			d.BindingIDs = appendUnique(d.BindingIDs, b.ID)
			d.NodeIDs = appendUnique(d.NodeIDs, b.NodeID)
			d.Roles = appendUnique(d.Roles, b.Role)
			continue
		}
		if scopeNode != nil && n.ID != scopeNode.ID {
			continue
		}
		key := e.Repo + "|" + e.File + "|" + e.Symbol
		if _, exists := attr[key]; !exists {
			cp := *e
			if b.Granularity == model.GranularityPartialSymbol {
				hp := hint
				cp.HintPresent = &hp
			}
			attr[key] = &cp
			*attrOrder = append(*attrOrder, key)
		}
		a := attr[key]
		a.BindingIDs = appendUnique(a.BindingIDs, b.ID)
		a.NodeIDs = appendUnique(a.NodeIDs, b.NodeID)
		a.Roles = appendUnique(a.Roles, b.Role)
	}

	if matchedAny {
		return
	}
	// No binding matched: check whether another worker holds a lease here.
	leasedNode, leasedSession := "", ""
	for _, l := range bindings.Leases {
		if scopeNode != nil && l.NodeID == scopeNode.ID {
			continue // own lease + unbound diff = orphan for this node
		}
		if ln := dag.Node(l.NodeID); ln == nil || ln.Status != model.StatusInProgress {
			continue // only active in_progress workers hold valid leases
		}
		for _, p := range l.Patterns {
			if leaseMatches(p, e.Repo, e.File) {
				leasedNode, leasedSession = l.NodeID, l.SessionID
				break
			}
		}
		if leasedNode != "" {
			break
		}
	}
	if leasedNode != "" {
		cp := *e
		cp.LeaseNodes = []string{leasedNode}
		cp.LeaseSessions = []string{leasedSession}
		rep.Leased = append(rep.Leased, cp)
		return
	}

	if scopeNode != nil && !InNodeScope(*e, scopeNode, bindings) {
		return
	}
	cp := *e
	cp.Correction = orphanCorrection(*e, scopeNode)
	rep.Orphan = append(rep.Orphan, cp)
}

func orphanCorrection(e Entry, n *model.Node) string {
	from := "<node>"
	if n != nil {
		from = n.ID
	}
	return fmt.Sprintf("若属越界改动：`git -C <repo> checkout -- %s`；若确属本需求遗漏：`reqboard propose --from %s --prd-ref <p:x> --title \"%s\" --reason \"...\"`", e.File, from, e.Symbol)
}

func InNodeScope(e Entry, n *model.Node, bindings *model.Bindings) bool {
	for _, b := range bindings.Bindings {
		if b.NodeID != n.ID {
			continue
		}
		if b.Repo == e.Repo && filepath.ToSlash(b.File) == e.File {
			return true
		}
	}
	if l := bindings.LeaseFor(n.ID); l != nil {
		for _, p := range l.Patterns {
			if leaseMatches(p, e.Repo, e.File) {
				return true
			}
		}
	}
	return false
}

func leaseMatches(pattern, repo, file string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	parts := strings.SplitN(pattern, ":", 2)
	if len(parts) == 1 {
		return pathmatch.Match(parts[0], file)
	}
	if parts[0] != "*" && parts[0] != repo {
		return false
	}
	return pathmatch.Match(parts[1], file)
}

func checkBinding(cfg *model.Config, root string, b model.Binding, dag *model.DAG) *BrokenBinding {
	if dag.Node(b.NodeID) == nil {
		return &BrokenBinding{
			BindingID: b.ID, NodeID: b.NodeID, Repo: b.Repo, File: b.File, Symbol: b.Symbol,
			Reason:     "node no longer exists",
			Correction: fmt.Sprintf("`reqboard unbind %s` 或重新绑定到有效节点", b.ID),
		}
	}
	var repo *model.Repo
	for i := range cfg.Repos {
		if cfg.Repos[i].Name == b.Repo {
			repo = &cfg.Repos[i]
			break
		}
	}
	if repo == nil {
		return &BrokenBinding{
			BindingID: b.ID, NodeID: b.NodeID, Repo: b.Repo, File: b.File, Symbol: b.Symbol,
			Reason:     "repo not configured in config.json",
			Correction: "检查 config.json 的 repos 或 `reqboard unbind " + b.ID + "`",
		}
	}
	repoPath := store.RepoPath(root, *repo)
	abs := b.File
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(repoPath, filepath.FromSlash(filepath.ToSlash(b.File)))
	}
	if _, err := os.Stat(abs); err != nil {
		return &BrokenBinding{
			BindingID: b.ID, NodeID: b.NodeID, Repo: b.Repo, File: b.File, Symbol: b.Symbol,
			Reason:     "file not found: " + abs,
			Correction: fmt.Sprintf("`reqboard unbind %s` 或修正 --file", b.ID),
		}
	}
	if !strings.EqualFold(filepath.Ext(b.File), ".go") || b.Symbol == "" {
		return nil
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		return &BrokenBinding{BindingID: b.ID, NodeID: b.NodeID, Repo: b.Repo, File: b.File, Symbol: b.Symbol, Reason: err.Error()}
	}
	syms, perr := astx.ParseGo(b.File, src)
	if perr != nil {
		return &BrokenBinding{
			BindingID: b.ID, NodeID: b.NodeID, Repo: b.Repo, File: b.File, Symbol: b.Symbol,
			Reason:     "cannot parse Go file: " + perr.Error(),
			Correction: fmt.Sprintf("修复语法错误或 `reqboard unbind %s`", b.ID),
		}
	}
	if sym, names := astx.FindSymbol(syms, b.Symbol); sym == nil {
		return &BrokenBinding{
			BindingID: b.ID, NodeID: b.NodeID, Repo: b.Repo, File: b.File, Symbol: b.Symbol,
			Reason:     "symbol not found in file",
			Available:  names,
			Correction: fmt.Sprintf("`reqboard bind %s --repo %s --file %s --symbol <真实符号> --role %s --why \"...\"` 或 `reqboard unbind %s`", b.NodeID, b.Repo, b.File, b.Role, b.ID),
		}
	}
	return nil
}

func appendUnique(in []string, s string) []string {
	if s == "" {
		return in
	}
	for _, x := range in {
		if x == s {
			return in
		}
	}
	return append(in, s)
}

func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].Repo != es[j].Repo {
			return es[i].Repo < es[j].Repo
		}
		if es[i].File != es[j].File {
			return es[i].File < es[j].File
		}
		if es[i].Start != es[j].Start {
			return es[i].Start < es[j].Start
		}
		return es[i].Symbol < es[j].Symbol
	})
}
