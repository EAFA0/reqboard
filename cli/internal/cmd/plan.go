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
		res, ierr := plan.Import(dag, doc, pf, opts)
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
			sb.WriteString("  已按“左细右粗”分层计算初始坐标。\n")
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
		plan.Layout(dag)
		if err := ctx.SaveDAG(dag, "dag.layout", "", "auto left-to-right layout"); err != nil {
			return err
		}
		mainN, parkedN := 0, 0
		for _, n := range dag.Nodes {
			if n.Scope == model.ScopeMine || n.Status == model.StatusProposed {
				mainN++
			} else {
				parkedN++
			}
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD LAYOUT] 主画布 %d 个节点已按左细右粗紧凑排布；%d 个外部/红线节点收纳到独立停靠区（不占主画布列宽）。\n", mainN, parkedN))
		ctx.Emit(sb.String(), map[string]interface{}{
			"ok": true, "nodes": len(dag.Nodes), "main_nodes": mainN, "parked_nodes": parkedN,
			"layout": "left-thin-right-thick",
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
