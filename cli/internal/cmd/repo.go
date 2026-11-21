package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"reqboard/internal/gitx"
	"reqboard/internal/model"
)

func runRepo(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return &Error{Code: 2, Msg: "usage: reqboard repo list|add|remove ...", Text: "用法: reqboard repo list | repo add --name <n> --path <dir> [--base <ref>] | repo remove <name> [--force]"}
	}
	switch pos[0] {
	case "list":
		return repoList(a)
	case "add":
		return repoAdd(a, pos[1:])
	case "remove", "rm":
		if len(pos) < 2 {
			return fail("usage: reqboard repo remove <name> [--force]")
		}
		return repoRemove(a, pos[1])
	}
	return &Error{Code: 2, Msg: "unknown repo subcommand " + pos[0], Text: "用法: reqboard repo list|add|remove"}
}

func repoList(a *Args) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	cfg, err := ctx.Store.LoadConfig()
	if err != nil {
		return fail("%v", err)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[REQBOARD REPO] %s 共 %d 个子仓\n", ctx.Root, len(cfg.Repos)))
	if len(cfg.Repos) == 0 {
		sb.WriteString("  （无；规划模式可用 `reqboard repo add --name <n> --path <dir>` 挂载）\n")
	}
	for _, r := range cfg.Repos {
		sb.WriteString(fmt.Sprintf("  - %s -> %s (base: %s)\n", r.Name, r.Path, r.BaseRef))
	}
	ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "root": ctx.Root, "repos": cfg.Repos})
	return nil
}

func repoAdd(a *Args, pos []string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	name := strings.TrimSpace(a.Str("name"))
	path := strings.TrimSpace(a.Str("path"))
	base := strings.TrimSpace(a.Str("base"))
	if len(pos) > 0 {
		// Also accept `repo add name=path:base`.
		spec := pos[0]
		parsed, err := parseRepoSpec(ctx.Root, spec)
		if err != nil {
			return fail("%v", err)
		}
		if name == "" {
			name = parsed.Name
		}
		if path == "" {
			path = parsed.Path
		}
		if base == "" {
			base = parsed.BaseRef
		}
	}
	if path == "" {
		return fail("usage: reqboard repo add --name <n> --path <dir> [--base <ref>]")
	}
	spec := path
	if base != "" {
		spec = path + ":" + base
	}
	if name != "" {
		spec = name + "=" + spec
	}
	repo, err := parseRepoSpec(ctx.Root, spec)
	if err != nil {
		return fail("%v", err)
	}
	abs := repo.Path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(ctx.Root, abs)
	}
	if !gitx.IsRepo(abs) {
		return fail("not a git repository: %s", abs)
	}
	return ctx.WithLock(func() *Error {
		cfg, cerr := ctx.Store.LoadConfig()
		if cerr != nil {
			return fail("%v", cerr)
		}
		for _, r := range cfg.Repos {
			if r.Name == repo.Name {
				return fail("repo %q already configured (%s)", repo.Name, r.Path)
			}
		}
		cfg.Repos = append(cfg.Repos, repo)
		if err := ctx.Store.SaveConfig(cfg); err != nil {
			return fail("save config.json: %v", err)
		}
		_ = ctx.Store.AppendEvent(ctx.event("repo.add", "", fmt.Sprintf("%s -> %s (base: %s)", repo.Name, repo.Path, repo.BaseRef)))
		ctx.Emit(fmt.Sprintf("[REQBOARD REPO] 已挂载 %s -> %s (base: %s)\n", repo.Name, repo.Path, repo.BaseRef),
			map[string]interface{}{"ok": true, "repo": repo, "repos": cfg.Repos})
		return nil
	})
}

func repoRemove(a *Args, name string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		cfg, cerr := ctx.Store.LoadConfig()
		if cerr != nil {
			return fail("%v", cerr)
		}
		idx := -1
		for i, r := range cfg.Repos {
			if r.Name == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fail("repo %q not configured", name)
		}
		bindings, berr := ctx.Store.LoadBindings()
		if berr != nil {
			return fail("%v", berr)
		}
		var refs []string
		for _, b := range bindings.Bindings {
			if b.Repo == name {
				refs = append(refs, b.ID+"("+b.NodeID+")")
			}
		}
		if len(refs) > 0 && !a.Bool("force") {
			return &Error{Code: 1, Msg: "repo still has bindings", Text: fmt.Sprintf(
				"[REQBOARD BLOCKED] 子仓 %s 仍有 %d 条绑定：%s\n  -> 先 `reqboard unbind <binding-id>`，或确认后加 `--force`（绑定会变成 Broken Binding）。\n",
				name, len(refs), strings.Join(refs, ", "))}
		}
		removed := cfg.Repos[idx]
		cfg.Repos = append(cfg.Repos[:idx], cfg.Repos[idx+1:]...)
		if cfg.Repos == nil {
			cfg.Repos = []model.Repo{}
		}
		if err := ctx.Store.SaveConfig(cfg); err != nil {
			return fail("save config.json: %v", err)
		}
		_ = ctx.Store.AppendEvent(ctx.event("repo.remove", "", removed.Name))
		ctx.Emit(fmt.Sprintf("[REQBOARD REPO] 已移除 %s（剩余 %d 个子仓）\n", name, len(cfg.Repos)),
			map[string]interface{}{"ok": true, "removed": removed, "repos": cfg.Repos, "forced": len(refs) > 0})
		return nil
	})
}
