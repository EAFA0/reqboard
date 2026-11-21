package cmd

import (
	"fmt"
	"sort"
	"strings"

	"reqboard/internal/model"
	"reqboard/internal/prd"
)

func runClaim(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return fail("usage: reqboard claim <node-id> [--session <sid>] [--lease <repo:glob>...]")
	}
	id := pos[0]
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	session := a.Str("session")
	if session == "" {
		session = model.NewID("s", nil, model.Now())
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		n, ne := findNodeOrFail(dag, id)
		if ne != nil {
			return ne
		}
		doc := loadPRDDoc(ctx)
		blocked := func(reason, rule string) *Error {
			return &Error{Code: 1, Msg: reason, Text: fmt.Sprintf("[REQBOARD BLOCKED] 无法 claim %s (%s)：%s\n  -> %s\n", id, n.Title, reason, rule)}
		}
		switch n.Scope {
		case model.ScopeOutOfScope:
			return blocked("scope=out_of_scope 红线节点", "PRD 明确不在本期范围，禁止任何 claim/bind；如确需变更请人类先改 scope。")
		case model.ScopeExternal:
			return blocked("scope=external 外部依赖节点", "禁止 Agent 修改该部分代码；仅作为上游依赖与 mocked 边起点。")
		case model.ScopeCollaborator:
			return blocked("scope=collaborator 协作者负责节点", "禁止 Agent 修改该部分代码；请记录外部进度，不要 claim。")
		}
		if n.Status == model.StatusProposed {
			return blocked("节点仍为 proposed", "待 owner 在看板审批（[批准]）后再 claim；审批前请勿修改该部分代码。")
		}
		if n.Hold {
			return blocked("节点被人工 hold", "人类已暂停该节点自动推进；请等待解除 hold。")
		}
		if n.Status == model.StatusDeprecated {
			return blocked("节点已 deprecated", "需求已砍/方案已推翻；如需清理残骸请走 To-Delete 流程，不要 claim 新实现。")
		}
		if n.Status == model.StatusDone {
			return blocked("节点已 done", "已完成节点不应重新 claim；如需返工请人类先 Review 打回或重开节点。")
		}

		bindings, berr := ctx.Store.LoadBindings()
		if berr != nil {
			return fail("%v", berr)
		}
		patterns := a.Strs("lease")
		if len(patterns) == 0 {
			if old := bindings.LeaseFor(id); old != nil {
				patterns = old.Patterns
			}
		}
		if len(patterns) == 0 {
			for _, b := range bindings.BindingsForNode(id) {
				patterns = append(patterns, b.Repo+":"+b.File)
			}
		}
		var conflicts []string
		for _, l := range bindings.Leases {
			if l.NodeID == id {
				continue
			}
			for _, p := range patterns {
				for _, op := range l.Patterns {
					if p == op {
						conflicts = append(conflicts, fmt.Sprintf("%s 由 %s 持有", op, l.SessionID))
					}
				}
			}
		}
		bindings.RemoveLeases(id)
		bindings.Leases = append(bindings.Leases, model.Lease{
			NodeID: id, SessionID: session, Patterns: dedupStrings(patterns), ClaimedAt: model.Now(),
		})
		if err := ctx.SaveBindings(bindings, "claim", id, session); err != nil {
			return err
		}
		if n.Status != model.StatusChangesRequested {
			n.Status = model.StatusInProgress
		} else {
			n.Status = model.StatusInProgress
		}
		n.SessionID = session
		n.UpdatedAt = model.Now()
		if err := ctx.SaveDAG(dag, "claim", id, "session="+session); err != nil {
			return err
		}

		// Build the rule-injection payload.
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD CLAIM] %s  %s\n", n.ID, n.Title))
		sb.WriteString(fmt.Sprintf("  session: %s\n  scope: %s | status: %s | stale: %v\n", session, n.Scope, n.Status, n.Stale))
		if len(conflicts) > 0 {
			sb.WriteString("  [租约警告] 与活跃租约重叠：" + strings.Join(conflicts, "; ") + "\n")
		}
		if n.Description != "" {
			sb.WriteString("  描述: " + n.Description + "\n")
		}
		sb.WriteString("\n—— 锚定 PRD 原文（只改这些功能点覆盖的行为） ——\n")
		if doc == nil {
			sb.WriteString("  [WARN] .reqboard/prd.md 为空或缺失，请先 `reqboard prd init <src.md>`。\n")
		} else {
			set := doc.AnchorSet()
			if len(n.PRDRefs) == 0 {
				sb.WriteString("  [WARN] 本节点没有 prd_refs；请人类确认是否需要补锚点。\n")
			}
			for _, ref := range n.PRDRefs {
				b := set[ref]
				if b == nil {
					sb.WriteString(fmt.Sprintf("  [MISSING] %s（PRD 中不存在，可能已被 sync 删除）\n", ref))
					continue
				}
				sb.WriteString(fmt.Sprintf("  <!-- @%s -->\n", b.Anchor))
				for _, line := range strings.Split(b.Text, "\n") {
					sb.WriteString("  " + line + "\n")
				}
			}
		}
		sb.WriteString("\n—— 红线（out_of_scope，禁止触碰） ——\n")
		var redlines []*model.Node
		for _, x := range dag.Nodes {
			if x.Scope == model.ScopeOutOfScope {
				redlines = append(redlines, x)
			}
		}
		sort.SliceStable(redlines, func(i, j int) bool { return redlines[i].ID < redlines[j].ID })
		if len(redlines) == 0 {
			sb.WriteString("  （无）\n")
		}
		for _, x := range redlines {
			sb.WriteString(fmt.Sprintf("  - %s %s\n", x.ID, x.Title))
			if doc != nil {
				set := doc.AnchorSet()
				for _, ref := range x.PRDRefs {
					if b := set[ref]; b != nil {
						sb.WriteString("      " + prd.Summary(b) + "\n")
					}
				}
			}
		}
		sb.WriteString("\n—— 入边契约 ——\n")
		in := dag.InEdges(id)
		if len(in) == 0 {
			sb.WriteString("  （无入边）\n")
		}
		for _, ed := range in {
			from := dag.Node(ed.From)
			fromStatus := "missing"
			if from != nil {
				fromStatus = from.Status
			}
			line := fmt.Sprintf("  [%s] %s (%s) -> %s", ed.Kind, ed.From, fromStatus, id)
			if ed.Note != "" {
				line += "  备注: " + ed.Note
			}
			sb.WriteString(line + "\n")
			if ed.Kind == model.EdgeMocked || ed.Kind == model.EdgeStale {
				sb.WriteString("    [BLOCK] 该入边会阻塞 done，拆 Mock/重核后切回 hard。\n")
			}
		}
		if len(n.SelfMocks) > 0 {
			sb.WriteString("\n—— 本节点 Mock 债 ——\n")
			for _, m := range n.SelfMocks {
				state := "open"
				if m.Resolved {
					state = "resolved"
				}
				sb.WriteString(fmt.Sprintf("  - %s [%s] %s\n", m.ID, state, m.Desc))
			}
		}
		notes := n.OpenNotes()
		if len(notes) > 0 {
			sb.WriteString("\n—— 未关闭 Review 批注（必须逐条修复） ——\n")
			for _, r := range notes {
				sb.WriteString(fmt.Sprintf("  - %s target=%s: %s\n", r.ID, r.Target, r.Comment))
			}
		}
		humanEvents, _ := ctx.Store.EventsForNode(id, 3)
		var human []string
		for _, ev := range humanEvents {
			if ev.Actor == "human" {
				human = append(human, fmt.Sprintf("%s %s %s", ev.TS, ev.Action, ev.Detail))
			}
		}
		if n.HumanVersion > 0 || len(human) > 0 {
			sb.WriteString("\n—— [人工最新调整] 人类修改优先级最高，按最新口径实现 ——\n")
			if len(human) > 0 {
				for _, h := range human {
					sb.WriteString("  - " + h + "\n")
				}
			} else {
				sb.WriteString(fmt.Sprintf("  - human_version=%d（人类已修改过该节点，请重读 dag.json 最新字段）\n", n.HumanVersion))
			}
		}
		sb.WriteString("\n—— 实施规则（硬约束） ——\n")
		sb.WriteString("  1. 只改本节点 PRD 覆盖的行为；out_of_scope 一律不碰。\n")
		sb.WriteString("  2. 不要求按拓扑串行；上游未 ready 可用 mocked 边继续，但 mocked 入边存在时禁止 done。\n")
		sb.WriteString("  3. 每处改动必须绑定：`reqboard bind " + id + " --repo <r> --file <f> --symbol <s> --role impl|mock|test --why \"...\"`。\n")
		sb.WriteString("  4. 完成后：`reqboard check --node " + id + "` 必须干净，再 `reqboard done " + id + "`。\n")
		sb.WriteString("  5. 发现越界/遗漏：用 `reqboard propose --from " + id + " --prd-ref <p:x> --title ... --reason ...`，审批前不要改。\n")

		prdBlocks := []map[string]interface{}{}
		if doc != nil {
			set := doc.AnchorSet()
			for _, ref := range n.PRDRefs {
				if b := set[ref]; b != nil {
					prdBlocks = append(prdBlocks, map[string]interface{}{"anchor": b.Anchor, "text": b.Text, "section": b.Section})
				}
			}
		}
		data := map[string]interface{}{
			"ok": true, "node": n, "session_id": session,
			"prd_blocks": prdBlocks, "lease_patterns": patterns,
			"in_edges": in, "open_review_notes": notes,
			"human_events": human, "redlines": redlines,
			"lease_conflicts": conflicts,
		}
		ctx.Emit(sb.String(), data)
		return nil
	})
}

func loadPRDDoc(ctx *Ctx) *prd.Document {
	raw, err := ctx.Store.ReadFile("prd.md")
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return nil
	}
	doc, err := prd.Parse(raw)
	if err != nil {
		return nil
	}
	return doc
}
