package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"reqboard/internal/gitx"
	"reqboard/internal/model"
	"reqboard/internal/store"
)

func runInit(argv []string) *Error {
	a := ParseArgs(argv)
	root := a.Str("root")
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fail("getwd: %v", err)
		}
		root = wd
	}
	root = normalizeRoot(root)
	abs, err := filepath.Abs(root)
	if err != nil {
		return fail("resolve root: %v", err)
	}
	root = abs
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fail("root is not a directory: %s", root)
	}
	if _, err := os.Stat(filepath.Join(root, store.DirName)); err == nil {
		return &Error{Code: 1, Msg: "workspace already initialized", Text: fmt.Sprintf("[REQBOARD] %s 已存在，未做修改。", filepath.Join(root, store.DirName))}
	}
	name := a.Str("name")
	if name == "" {
		name = filepath.Base(root)
	}
	var repos []model.Repo
	for _, spec := range a.Strs("repo") {
		r, err := parseRepoSpec(root, spec)
		if err != nil {
			return fail("%v", err)
		}
		repos = append(repos, r)
	}
	if len(repos) == 0 {
		repos = scanRepos(root)
	}
	// A planning-only workspace is valid: the owner may review the DAG before
	// code repositories are mounted. `reqboard repo add` can attach them later.
	planningOnly := len(repos) == 0
	if repos == nil {
		repos = []model.Repo{}
	}
	s, err := store.Init(root, name, repos)
	if err != nil {
		return fail("%v", err)
	}
	cfg, _ := s.LoadConfig()
	data := map[string]interface{}{
		"ok":               true,
		"root":             root,
		"requirement_name": name,
		"repos":            cfg.Repos,
		"planning_only":    planningOnly,
		"files":            []string{".reqboard/config.json", ".reqboard/prd.md", ".reqboard/dag.json", ".reqboard/bindings.json", ".reqboard/events.jsonl"},
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[REQBOARD INIT] 需求 worktree: %s\n", root))
	sb.WriteString(fmt.Sprintf("  名称: %s\n", name))
	if planningOnly {
		sb.WriteString("  子仓: 未发现 git 子仓，已按规划模式初始化。\n")
		sb.WriteString("        后续用 `reqboard repo add --name <n> --path <dir> [--base <ref>]` 挂载代码仓。\n")
	} else {
		sb.WriteString("  子仓:\n")
		for _, r := range cfg.Repos {
			sb.WriteString(fmt.Sprintf("    - %s -> %s (base: %s)\n", r.Name, r.Path, r.BaseRef))
		}
	}
	sb.WriteString("  已创建: .reqboard/{config.json,prd.md,dag.json,bindings.json,events.jsonl}\n")
	sb.WriteString("  默认忽略: " + strings.Join(cfg.IgnoreGlobs, ", ") + "\n")
	sb.WriteString("\n下一步:\n")
	sb.WriteString("  1) reqboard prd init <src.md>       # 注入 PRD 锚点\n")
	sb.WriteString("  2) reqboard plan import plan.json   # 导入功能级 DAG\n")
	sb.WriteString("  3) reqboard status                  # 查看看板快照\n")
	ctx := &Ctx{Root: root, Store: s, JSON: a.Bool("json"), Actor: "cli"}
	ctx.Emit(sb.String(), data)
	return nil
}

func parseRepoSpec(root, spec string) (model.Repo, error) {
	var name, rest string
	if i := strings.Index(spec, "="); i >= 0 {
		name = strings.TrimSpace(spec[:i])
		rest = spec[i+1:]
	} else {
		rest = spec
	}
	path, base := rest, ""
	if i := strings.LastIndex(rest, ":"); i > 0 {
		path, base = rest[:i], rest[i+1:]
	}
	if path == "" {
		return model.Repo{}, fmt.Errorf("invalid --repo %q", spec)
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, path)
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return model.Repo{}, fmt.Errorf("repo path not found: %s", abs)
	}
	if name == "" {
		name = filepath.Base(strings.TrimRight(abs, "/"))
	}
	if base == "" {
		base = store.DetectBaseRef(abs)
	}
	rel := path
	if r, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(r, "..") {
		rel = r
	} else {
		rel = abs
	}
	return model.Repo{Name: name, Path: filepath.ToSlash(rel), BaseRef: base}, nil
}

func scanRepos(root string) []model.Repo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		dir := filepath.Join(root, name)
		if gitx.IsRepo(dir) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var repos []model.Repo
	for _, name := range names {
		dir := filepath.Join(root, name)
		repos = append(repos, model.Repo{Name: name, Path: name, BaseRef: store.DetectBaseRef(dir)})
	}
	if len(repos) == 0 && gitx.IsRepo(root) {
		repos = append(repos, model.Repo{Name: filepath.Base(root), Path: ".", BaseRef: store.DetectBaseRef(root)})
	}
	return repos
}
