package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/audit"
	"reqboard/internal/model"
)

func runFilter(a *Args, rest []string) *Error {
	pos := a.Pos()
	sub := "show"
	if len(pos) > 0 {
		sub = pos[0]
	}
	switch sub {
	case "show":
		return filterShow(a)
	case "set":
		return filterSet(a)
	}
	return &Error{
		Code: 2, Msg: "unknown filter subcommand " + sub,
		Text: "用法: reqboard filter show [--json] | filter set [--include <comma-globs>] [--exclude <comma-globs>] [--exclude-tests=true|false] [--reset]",
	}
}

func filterShow(a *Args) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	cfg, err := ctx.Store.LoadConfig()
	if err != nil {
		return fail("%v", err)
	}
	f := audit.EffectiveFilter(cfg)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[REQBOARD FILTER] %s\n", ctx.Root))
	sb.WriteString(fmt.Sprintf("  exclude_tests: %t\n", f.ExcludeTests))
	sb.WriteString(fmt.Sprintf("  include_globs: %s\n", joinOrDash(f.IncludeGlobs)))
	sb.WriteString(fmt.Sprintf("  ignore_globs:  %s\n", joinOrDash(f.IgnoreGlobs)))
	ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "root": ctx.Root, "filter": f})
	return nil
}

func filterSet(a *Args) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	if !a.Bool("reset") && !a.Has("include") && !a.Has("exclude") && !a.Has("exclude-tests") {
		return fail("usage: reqboard filter set [--include <comma-globs>] [--exclude <comma-globs>] [--exclude-tests=true|false] [--reset]")
	}
	return ctx.WithLock(func() *Error {
		cfg, cerr := ctx.Store.LoadConfig()
		if cerr != nil {
			return fail("%v", cerr)
		}
		var changes []string
		if a.Bool("reset") {
			cfg.IncludeGlobs = nil
			cfg.IgnoreGlobs = append([]string{}, model.DefaultIgnoreGlobs...)
			cfg.ExcludeTests = nil
			changes = append(changes, "reset")
		} else {
			if a.Has("include") {
				cfg.IncludeGlobs = splitGlobs(a.Str("include"))
				changes = append(changes, "include="+joinOrDash(cfg.IncludeGlobs))
			}
			if a.Has("exclude") {
				globs := splitGlobs(a.Str("exclude"))
				if len(globs) == 0 {
					// An empty exclude list means "fall back to the defaults",
					// matching how config.json is loaded.
					globs = append([]string{}, model.DefaultIgnoreGlobs...)
				}
				cfg.IgnoreGlobs = globs
				changes = append(changes, "exclude="+joinOrDash(globs))
			}
			if a.Has("exclude-tests") {
				v := a.Bool("exclude-tests")
				cfg.ExcludeTests = &v
				changes = append(changes, fmt.Sprintf("exclude_tests=%t", v))
			}
		}
		if err := ctx.Store.SaveConfig(cfg); err != nil {
			return fail("save config.json: %v", err)
		}
		detail := strings.Join(changes, " ")
		_ = ctx.Store.AppendEvent(ctx.event("filter.update", "", detail))
		f := audit.EffectiveFilter(cfg)
		ctx.Emit(fmt.Sprintf("[REQBOARD FILTER] 已更新：%s\n  exclude_tests: %t\n  include_globs: %s\n  ignore_globs:  %s\n",
			detail, f.ExcludeTests, joinOrDash(f.IncludeGlobs), joinOrDash(f.IgnoreGlobs)),
			map[string]interface{}{"ok": true, "root": ctx.Root, "filter": f, "changes": detail})
		return nil
	})
}

func splitGlobs(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if g := strings.TrimSpace(part); g != "" {
			out = append(out, g)
		}
	}
	return out
}

func joinOrDash(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}
