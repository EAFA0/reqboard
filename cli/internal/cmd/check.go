package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/audit"
)

func runCheck(a *Args, rest []string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	cfg, cerr := ctx.Store.LoadConfig()
	if cerr != nil {
		return fail("%v", cerr)
	}
	dag, derr := ctx.Store.LoadDAG()
	if derr != nil {
		return fail("%v", derr)
	}
	bindings, berr := ctx.Store.LoadBindings()
	if berr != nil {
		return fail("%v", berr)
	}
	opts := audit.Options{}
	if a.Has("node") {
		opts.NodeID = a.Str("node")
		if dag.Node(opts.NodeID) == nil {
			return fail("node not found: %s", opts.NodeID)
		}
	} else {
		opts.All = true
	}
	rep := audit.Run(cfg, ctx.Root, dag, bindings, opts)

	var sb strings.Builder
	scope := "all"
	if opts.NodeID != "" {
		scope = opts.NodeID
	}
	sb.WriteString(fmt.Sprintf("[REQBOARD CHECK] scope=%s  changed_files=%d  violations=%d\n", scope, rep.ChangedFiles, rep.Violations))
	sb.WriteString(fmt.Sprintf("  Attributed(已归因)=%d  Orphan Diff(孤儿)=%d  To-Delete(残骸)=%d  Leased(队友租约)=%d  Broken Binding=%d\n",
		len(rep.Attributed), len(rep.Orphan), len(rep.ToDelete), len(rep.Leased), len(rep.Broken)))
	if len(rep.Ignored) > 0 {
		sb.WriteString(fmt.Sprintf("  已过滤文件 %d 个（单测/生成代码/未命中 include）: %s\n", len(rep.Ignored), strings.Join(rep.Ignored, ", ")))
	}
	sb.WriteString(fmt.Sprintf("  Diff filter: exclude_tests=%t include_globs=%d ignore_globs=%d\n",
		rep.Filter.ExcludeTests, len(rep.Filter.IncludeGlobs), len(rep.Filter.IgnoreGlobs)))
	for _, r := range rep.Repos {
		if r.Error != "" {
			sb.WriteString(fmt.Sprintf("  [REPO ERROR] %s (%s): %s\n", r.Name, r.Path, r.Error))
		}
	}
	if len(rep.Orphan) > 0 {
		sb.WriteString("\n  [Orphan Diff] 未挂载到任何功能点的改动（疑似越界/过度设计）:\n")
		for _, x := range rep.Orphan {
			sb.WriteString(fmt.Sprintf("    - %s:%s :: %s (+%d/-%d)\n", x.Repo, x.File, x.Symbol, x.Added, x.Deleted))
			if x.Correction != "" {
				sb.WriteString("      -> " + x.Correction + "\n")
			}
		}
	}
	if len(rep.ToDelete) > 0 {
		sb.WriteString("\n  [To-Delete 残骸] deprecated 节点的历史绑定仍有改动:\n")
		for _, x := range rep.ToDelete {
			sb.WriteString(fmt.Sprintf("    - %s:%s :: %s (+%d/-%d) <- %s\n", x.Repo, x.File, x.Symbol, x.Added, x.Deleted, strings.Join(x.NodeIDs, ",")))
			if x.Correction != "" {
				sb.WriteString("      -> " + x.Correction + "\n")
			}
		}
	}
	if len(rep.Broken) > 0 {
		sb.WriteString("\n  [Broken Binding] 绑定的符号/文件已不存在:\n")
		for _, b := range rep.Broken {
			sb.WriteString(fmt.Sprintf("    - %s %s: %s:%s::%s  (%s)\n", b.BindingID, b.NodeID, b.Repo, b.File, b.Symbol, b.Reason))
			if len(b.Available) > 0 {
				sb.WriteString("      真实符号: " + strings.Join(b.Available, ", ") + "\n")
			}
			if b.Correction != "" {
				sb.WriteString("      -> " + b.Correction + "\n")
			}
		}
	}
	if len(rep.Leased) > 0 {
		sb.WriteString("\n  [Leased] 队友租约中的改动（--node 自检不误报）:\n")
		for _, x := range rep.Leased {
			sb.WriteString(fmt.Sprintf("    - %s:%s :: %s (+%d/-%d) <- lease %s\n", x.Repo, x.File, x.Symbol, x.Added, x.Deleted, strings.Join(x.LeaseSessions, ",")))
		}
	}
	if rep.Violations == 0 {
		sb.WriteString("\n  [OK] 无 Orphan Diff / To-Delete / Broken Binding，可继续完结流程。\n")
	} else {
		sb.WriteString(fmt.Sprintf("\n  [REQBOARD BLOCKED] 发现 %d 处违规，禁止标记 done。\n", rep.Violations))
		sb.WriteString("  -> 按上面每条纠偏命令处理后重跑 `reqboard check --all`。\n")
	}

	data := map[string]interface{}{
		"ok":            rep.Violations == 0,
		"scope":         scope,
		"changed_files": rep.ChangedFiles,
		"violations":    rep.Violations,
		"buckets": map[string]interface{}{
			"attributed":      rep.Attributed,
			"orphan":          rep.Orphan,
			"to_delete":       rep.ToDelete,
			"leased":          rep.Leased,
			"broken_bindings": rep.Broken,
		},
		"node_stats":    rep.NodeStats,
		"repos":         rep.Repos,
		"ignored_files": rep.Ignored,
		"filter":        rep.Filter,
	}
	ctx.Emit(sb.String(), data)
	if rep.Violations > 0 {
		return &Error{Code: 1, Silent: true}
	}
	return nil
}
