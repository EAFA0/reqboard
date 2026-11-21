package cmd

import (
	"fmt"
	"os"
	"strings"

	"reqboard/internal/model"
	"reqboard/internal/plan"
	"reqboard/internal/prd"
)

func runPlan(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 || pos[0] != "import" {
		return &Error{Code: 2, Msg: "usage: reqboard plan import <plan.json>", Text: "用法: reqboard plan import <plan.json> [--replace] [--no-layout] [--allow-unknown-refs]"}
	}
	if len(pos) < 2 {
		return fail("usage: reqboard plan import <plan.json>")
	}
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	pf, err := plan.Load(pos[1])
	if err != nil {
		return fail("%v", err)
	}
	var doc *prd.Document
	if raw, rerr := ctx.Store.ReadFile("prd.md"); rerr == nil && strings.TrimSpace(string(raw)) != "" {
		doc, _ = prd.Parse(raw)
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		opts := plan.Options{
			Replace:          a.Bool("replace"),
			Layout:           !a.Bool("no-layout"),
			AllowUnknownRefs: a.Bool("allow-unknown-refs"),
		}
		res, ierr := plan.ImportWithSources(dag, doc, loadSourceDocs(ctx, dag), pf, opts)
		if ierr != nil {
			return &Error{Code: 1, Msg: ierr.Error(), Text: "[REQBOARD BLOCKED] plan 校验失败: " + ierr.Error() + "\n  -> 修复 plan.json 后重试；DAG 未被修改。\n"}
		}
		if pf.RequirementName != "" {
			dag.RequirementName = pf.RequirementName
		}
		if err := ctx.SaveDAG(dag, "plan.import", "", fmt.Sprintf("nodes=%d edges=%d", len(dag.Nodes), len(dag.Edges))); err != nil {
			return err
		}
		var sb strings.Builder
		sb.WriteString("[REQBOARD PLAN IMPORT]\n")
		sb.WriteString(fmt.Sprintf("  节点: +%d 更新 %d，边: +%d（总计 %d 节点 / %d 边）\n",
			len(res.AddedNodes), len(res.UpdatedNodes), len(res.AddedEdges), len(dag.Nodes), len(dag.Edges)))
		if res.Layout {
			sb.WriteString("  已按自然拓扑深度（ASAP）计算初始坐标；milestone/e2e 钉最右终列。\n")
		}
		sb.WriteString(fmt.Sprintf("  PRD 覆盖率: %.1f%%（%d/%d 段，忽略 %d）\n",
			res.Coverage.Percent, res.Coverage.Covered, res.Coverage.Total, res.Coverage.Ignored))
		if len(res.Coverage.Unmapped) > 0 {
			sb.WriteString("  Unmapped PRD: " + strings.Join(res.Coverage.Unmapped, ", ") + "\n")
			sb.WriteString("  -> 用 `reqboard node add --title ... --prd-ref <p:x>` 建节点，或 `reqboard prd ignore <p:x>`。\n")
		}
		for _, w := range res.Warnings {
			sb.WriteString("  [warn] " + w + "\n")
		}
		sb.WriteString("\n  下一步: `reqboard status` 查看看板，或 `reqboard dispatch` 开始推进。\n")
		ctx.Emit(sb.String(), map[string]interface{}{
			"ok": true, "added_nodes": res.AddedNodes, "updated_nodes": res.UpdatedNodes,
			"added_edges": res.AddedEdges, "warnings": res.Warnings,
			"coverage": res.Coverage, "layout": res.Layout,
			"nodes": len(dag.Nodes), "edges": len(dag.Edges),
		})
		return nil
	})
}

func runLayout(a *Args, rest []string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		includeExternal := a.Bool("include-external")
		plan.LayoutWith(dag, includeExternal)
		detail := "auto left-to-right layout"
		if includeExternal {
			detail = "full graph layout (external sources in column 0)"
		}
		if err := ctx.SaveDAG(dag, "dag.layout", "", detail); err != nil {
			return err
		}
		mainN, parkedN, sourceN, redlineN := 0, 0, 0, 0
		for _, n := range dag.Nodes {
			switch {
			case model.IsPrerequisiteScope(n.Scope):
				sourceN++
			case n.Scope == model.ScopeOutOfScope:
				redlineN++
			}
			if n.Scope == model.ScopeMine || n.Status == model.StatusProposed {
				mainN++
			} else {
				parkedN++
			}
		}
		var sb strings.Builder
		if includeExternal {
			sb.WriteString(fmt.Sprintf("[REQBOARD LAYOUT] 全图模式：%d 个 external/collaborator/todo 前置节点排入最左侧 Column 0，%d 个我方节点沿左到右自然拓扑展开；%d 个红线节点收纳到底部停靠区。\n",
				sourceN, mainN, redlineN))
		} else {
			sb.WriteString(fmt.Sprintf("[REQBOARD LAYOUT] 主画布 %d 个节点已按自然拓扑深度排布（仅 milestone/e2e 钉最右）；%d 个外部/协作者/TODO/红线节点收纳到独立停靠区（不占主画布列宽）。\n", mainN, parkedN))
		}
		layoutName := "left-thin-right-thick"
		if includeExternal {
			layoutName = "full-graph-external-column-0"
		}
		ctx.Emit(sb.String(), map[string]interface{}{
			"ok": true, "nodes": len(dag.Nodes), "main_nodes": mainN, "parked_nodes": parkedN,
			"external_nodes": sourceN, "redline_nodes": redlineN,
			"layout": layoutName, "include_external": includeExternal,
		})
		return nil
	})
}

func findNodeOrFail(dag *model.DAG, id string) (*model.Node, *Error) {
	n := dag.Node(id)
	if n == nil {
		var ids []string
		for _, x := range dag.Nodes {
			ids = append(ids, x.ID)
		}
		return nil, &Error{Code: 1, Msg: "node not found: " + id, Text: fmt.Sprintf("[REQBOARD ERROR] 节点 %s 不存在。可用节点: %s\n", id, strings.Join(ids, ", "))}
	}
	return n, nil
}

func mustReadFile(path string) ([]byte, *Error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fail("read %s: %v", path, err)
	}
	return data, nil
}
