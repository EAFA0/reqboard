package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"reqboard/internal/model"
	"reqboard/internal/plan"
	"reqboard/internal/prd"
)

func runPRD(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return &Error{Code: 2, Msg: "usage: reqboard prd init|sync|ignore ...", Text: "用法: reqboard prd init <src.md> | prd sync <new.md> | prd ignore <p:x> [<p:y>...]"}
	}
	switch pos[0] {
	case "init":
		if len(pos) < 2 {
			return fail("usage: reqboard prd init <src.md>")
		}
		return prdInit(a, pos[1])
	case "sync":
		if len(pos) < 2 {
			return fail("usage: reqboard prd sync <new.md>")
		}
		return prdSync(a, pos[1])
	case "ignore", "unignore":
		if len(pos) < 2 {
			return fail("usage: reqboard prd ignore <p:x> [<p:y>...] [--undo]")
		}
		undo := pos[0] == "unignore" || a.Bool("undo")
		return prdIgnoreMany(a, pos[1:], undo)
	}
	return &Error{Code: 2, Msg: "unknown prd subcommand " + pos[0], Text: "用法: reqboard prd init|sync|ignore"}
}

func prdInit(a *Args, srcPath string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return fail("read %s: %v", srcPath, err)
	}
	doc, perr := prd.Parse(src)
	if perr != nil {
		return fail("parse PRD: %v", perr)
	}
	return ctx.WithLock(func() *Error {
		if !a.Bool("force") {
			if old, err := ctx.Store.ReadFile("prd.md"); err == nil && strings.TrimSpace(string(old)) != "" {
				return &Error{Code: 1, Msg: "prd.md already has content", Text: "[REQBOARD BLOCKED] .reqboard/prd.md 已有内容；重新初始化会重建锚点并打断 dag.json 引用。\n  -> 增量更新请用 `reqboard prd sync <new.md>`；确需重建请加 --force。\n"}
			}
		}
		if err := ctx.Store.WriteFile("prd.md", doc.Render()); err != nil {
			return fail("write prd.md: %v", err)
		}
		_ = ctx.Store.AppendEvent(ctx.event("prd.init", "", fmt.Sprintf("anchored %d blocks", len(doc.Blocks))))
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD PRD INIT] %s -> .reqboard/prd.md\n", srcPath))
		sb.WriteString(fmt.Sprintf("  块数: %d（标题 %d）\n", len(doc.Blocks), len(doc.Heading)))
		sb.WriteString("  锚点示例:\n")
		for i, b := range doc.Blocks {
			if i >= 5 {
				break
			}
			sb.WriteString(fmt.Sprintf("    <!-- @%s --> %s\n", b.Anchor, prd.Summary(b)))
		}
		sb.WriteString("  下一步: `reqboard plan import <plan.json>` 或 `reqboard node add ...`\n")
		data := map[string]interface{}{
			"ok":       true,
			"path":     ctx.Store.PRDPath(),
			"blocks":   len(doc.Blocks),
			"headings": len(doc.Heading),
			"anchors":  doc.Anchors(),
		}
		ctx.Emit(sb.String(), data)
		return nil
	})
}

func prdSync(a *Args, newPath string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	src, err := os.ReadFile(newPath)
	if err != nil {
		return fail("read %s: %v", newPath, err)
	}
	newDoc, perr := prd.Parse(src)
	if perr != nil {
		return fail("parse PRD: %v", perr)
	}
	return ctx.WithLock(func() *Error {
		oldRaw, err := ctx.Store.ReadFile("prd.md")
		if err != nil {
			return fail("read .reqboard/prd.md: %v", err)
		}
		oldDoc, perr := prd.Parse(oldRaw)
		if perr != nil {
			return fail("parse existing prd.md: %v", perr)
		}
		res := prd.Sync(oldDoc, newDoc)
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}

		reasonByAnchor := map[string]string{}
		for _, p := range res.Modified {
			reasonByAnchor[p.Old.Anchor] = fmt.Sprintf("PRD 段落 %s 已修改: %s", p.Old.Anchor, prd.Summary(p.New))
		}
		for _, b := range res.Deleted {
			reasonByAnchor[b.Anchor] = fmt.Sprintf("PRD 段落 %s 已删除，请确认是否 deprecated", b.Anchor)
		}
		affected := cascadeStale(dag, reasonByAnchor)

		if err := ctx.Store.WriteFile("prd.md", newDoc.Render()); err != nil {
			return fail("write prd.md: %v", err)
		}
		dag.Touch()
		if err := ctx.Store.SaveDAG(dag); err != nil {
			return fail("save dag.json: %v", err)
		}
		_ = ctx.Store.AppendEvent(ctx.event("prd.sync", "", fmt.Sprintf("modified=%d added=%d deleted=%d stale=%d", len(res.Modified), len(res.Added), len(res.Deleted), len(affected))))

		unmapped := unmappedAnchors(dag, newDoc)
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD PRD SYNC] %s\n", newPath))
		sb.WriteString(fmt.Sprintf("  未变 %d 段，修改 %d 段，新增 %d 段，删除 %d 段\n", res.Unchanged, len(res.Modified), len(res.Added), len(res.Deleted)))
		if len(res.Modified) > 0 {
			sb.WriteString("\n  修改段落（关联节点及下游已级联 stale）:\n")
			for _, p := range res.Modified {
				sb.WriteString(fmt.Sprintf("    - %s  %s\n", p.New.Anchor, prd.Summary(p.New)))
			}
		}
		if len(res.Added) > 0 {
			sb.WriteString("\n  新增段落（Unmapped PRD，需建节点或忽略）:\n")
			for _, b := range res.Added {
				if b.Kind == prd.KindHeading {
					continue
				}
				sb.WriteString(fmt.Sprintf("    - %s  %s\n", b.Anchor, prd.Summary(b)))
			}
		}
		if len(res.Deleted) > 0 {
			sb.WriteString("\n  删除段落（请确认是否 deprecated 并清理残骸）:\n")
			for _, b := range res.Deleted {
				sb.WriteString(fmt.Sprintf("    - %s  %s\n", b.Anchor, prd.Summary(b)))
			}
		}
		if len(affected) > 0 {
			sb.WriteString("\n  已标 stale 节点:\n")
			ids := sortedKeys(affected)
			for _, id := range ids {
				sb.WriteString(fmt.Sprintf("    - %s: %s\n", id, affected[id]))
			}
		}
		if len(unmapped) > 0 {
			sb.WriteString("\n  Unmapped PRD 清单: " + strings.Join(unmapped, ", ") + "\n")
		}
		sb.WriteString("\n  下一步: 对修改段对应节点重新核对实现；新增段用 `reqboard node add --prd-ref <p:x>` 或 `reqboard prd ignore <p:x>`。\n")

		modified := []map[string]interface{}{}
		for _, p := range res.Modified {
			modified = append(modified, map[string]interface{}{
				"anchor": p.New.Anchor, "old_text": p.Old.Text, "new_text": p.New.Text,
			})
		}
		added := []map[string]interface{}{}
		for _, b := range res.Added {
			added = append(added, map[string]interface{}{"anchor": b.Anchor, "text": b.Text, "kind": b.Kind})
		}
		deleted := []map[string]interface{}{}
		for _, b := range res.Deleted {
			deleted = append(deleted, map[string]interface{}{"anchor": b.Anchor, "text": b.Text, "kind": b.Kind})
		}
		coverage := plan.ComputeCoverage(dag, newDoc)
		data := map[string]interface{}{
			"ok":          true,
			"unchanged":   res.Unchanged,
			"modified":    modified,
			"added":       added,
			"deleted":     deleted,
			"stale_nodes": affected,
			"unmapped":    unmapped,
			"coverage":    coverage,
		}
		ctx.Emit(sb.String(), data)
		return nil
	})
}

func prdIgnoreMany(a *Args, anchors []string, undo bool) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		raw, err := ctx.Store.ReadFile("prd.md")
		if err != nil {
			return fail("read prd.md: %v", err)
		}
		doc, perr := prd.Parse(raw)
		if perr != nil {
			return fail("parse prd.md: %v", perr)
		}
		var missing []string
		for _, anchor := range anchors {
			if doc.Anchor(anchor) == nil {
				missing = append(missing, anchor)
			}
		}
		if len(missing) > 0 {
			return fail("PRD anchor(s) not found: %s (use the p:x ids shown in the left panel)", strings.Join(missing, ", "))
		}
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		set := map[string]bool{}
		for _, r := range dag.IgnoredPRDRefs {
			set[r] = true
		}
		for _, anchor := range anchors {
			if undo {
				delete(set, anchor)
			} else {
				set[anchor] = true
			}
		}
		dag.IgnoredPRDRefs = sortedKeys(set)
		dag.Touch()
		if err := ctx.Store.SaveDAG(dag); err != nil {
			return fail("save dag.json: %v", err)
		}
		action := "prd.ignore"
		if undo {
			action = "prd.unignore"
		}
		_ = ctx.Store.AppendEvent(ctx.event(action, "", strings.Join(anchors, ",")))
		msg := fmt.Sprintf("[REQBOARD] 已忽略 %d 个 PRD 段落：%s（不再计入 Unmapped）。\n", len(anchors), strings.Join(anchors, ", "))
		if undo {
			msg = fmt.Sprintf("[REQBOARD] 已恢复 %d 个 PRD 段落的覆盖要求：%s。\n", len(anchors), strings.Join(anchors, ", "))
		}
		ctx.Emit(msg, map[string]interface{}{"ok": true, "anchors": anchors, "ignored": dag.IgnoredPRDRefs})
		return nil
	})
}

// cascadeStale marks nodes referencing the given anchors and all downstream
// nodes stale. Returns nodeID -> reason.
func cascadeStale(dag *model.DAG, reasonByAnchor map[string]string) map[string]string {
	affected := map[string]string{}
	var seeds []string
	for _, n := range dag.Nodes {
		for _, ref := range n.PRDRefs {
			if reason, ok := reasonByAnchor[ref]; ok {
				affected[n.ID] = reason
				seeds = append(seeds, n.ID)
				break
			}
		}
	}
	sort.Strings(seeds)
	for _, seed := range seeds {
		for _, id := range dag.Downstream(seed) {
			if _, ok := affected[id]; ok {
				continue
			}
			affected[id] = fmt.Sprintf("上游节点 %s 因 PRD 变更需重核", seed)
		}
	}
	for id, reason := range affected {
		if n := dag.Node(id); n != nil {
			n.SetStale(reason)
			n.UpdatedAt = model.Now()
		}
	}
	return affected
}

func unmappedAnchors(dag *model.DAG, doc *prd.Document) []string {
	refs := map[string]bool{}
	for _, n := range dag.Nodes {
		for _, r := range n.PRDRefs {
			refs[r] = true
		}
	}
	for _, r := range dag.IgnoredPRDRefs {
		refs[r] = true
	}
	out := []string{}
	for _, b := range doc.Blocks {
		if b.Kind == prd.KindHeading || refs[b.Anchor] {
			continue
		}
		out = append(out, b.Anchor)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
