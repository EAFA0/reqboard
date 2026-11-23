package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"reqboard/internal/model"
	"reqboard/internal/prd"
)

func runNode(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return &Error{Code: 2, Msg: "usage: reqboard node list|add|update|deprecate|delete|approve|reject|review|fail ..."}
	}
	switch pos[0] {
	case "list", "ls":
		return nodeList(a)
	case "add":
		return nodeAdd(a, pos[1:])
	case "update":
		if len(pos) < 2 {
			return fail("usage: reqboard node update <id> [flags]")
		}
		return nodeUpdate(a, pos[1])
	case "deprecate":
		if len(pos) < 2 {
			return fail("usage: reqboard node deprecate <id> [--reason ...]")
		}
		return nodeDeprecate(a, pos[1])
	case "delete", "rm":
		if len(pos) < 2 {
			return fail("usage: reqboard node delete <id> [--force]")
		}
		return nodeDelete(a, pos[1])
	case "approve":
		if len(pos) < 2 {
			return fail("usage: reqboard node approve <id>")
		}
		return nodeApprove(a, pos[1])
	case "reject":
		if len(pos) < 2 {
			return fail("usage: reqboard node reject <id> [--reason ...]")
		}
		return nodeReject(a, pos[1])
	case "review":
		if len(pos) < 2 {
			return fail("usage: reqboard node review <id> --pass | --request-changes --note ...")
		}
		return nodeReview(a, pos[1])
	case "fail":
		if len(pos) < 2 {
			return fail("usage: reqboard node fail <id> --error <msg>")
		}
		return nodeFail(a, pos[1])
	}
	return &Error{Code: 2, Msg: "unknown node subcommand " + pos[0]}
}

func nodeList(a *Args) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	dag, derr := ctx.Store.LoadDAG()
	if derr != nil {
		return fail("%v", derr)
	}
	bindings, berr := ctx.Store.LoadBindings()
	if berr != nil {
		return fail("%v", berr)
	}
	wantScope := strings.TrimSpace(a.Str("scope"))
	wantStatus := strings.TrimSpace(a.Str("status"))
	onlyStale := a.Bool("stale")

	bindCount := map[string]int{}
	for _, b := range bindings.Bindings {
		bindCount[b.NodeID]++
	}
	type item struct {
		ID         string   `json:"id"`
		Scope      string   `json:"scope"`
		Status     string   `json:"status"`
		Stale      bool     `json:"stale"`
		Hold       bool     `json:"hold"`
		PRDCount   int      `json:"prd_refs_count"`
		BindCount  int      `json:"bindings_count"`
		OpenNotes  int      `json:"open_notes"`
		PendingMks int      `json:"pending_mocks"`
		Tags       []string `json:"tags,omitempty"`
		Title      string   `json:"title"`
	}
	var rows []item
	var sb strings.Builder
	for _, n := range dag.Nodes {
		if wantScope != "" && n.Scope != wantScope {
			continue
		}
		if wantStatus != "" && n.Status != wantStatus {
			continue
		}
		if onlyStale && !n.Stale {
			continue
		}
		row := item{
			ID:         n.ID,
			Scope:      n.Scope,
			Status:     n.Status,
			Stale:      n.Stale,
			Hold:       n.Hold,
			PRDCount:   len(n.PRDRefs),
			BindCount:  bindCount[n.ID],
			OpenNotes:  len(n.OpenNotes()),
			PendingMks: len(n.PendingMocks()),
			Tags:       n.Tags,
			Title:      n.Title,
		}
		rows = append(rows, row)
		flags := ""
		if row.Stale {
			flags += " stale"
		}
		if row.Hold {
			flags += " hold"
		}
		if row.OpenNotes > 0 {
			flags += fmt.Sprintf(" notes=%d", row.OpenNotes)
		}
		if row.PendingMks > 0 {
			flags += fmt.Sprintf(" mocks=%d", row.PendingMks)
		}
		sb.WriteString(fmt.Sprintf("%-28s | %-12s | %-17s | prd=%-3d bind=%-3d%s | %s\n",
			row.ID, row.Scope, row.Status, row.PRDCount, row.BindCount, flags, row.Title))
	}
	header := fmt.Sprintf("[REQBOARD NODES] %d/%d 节点\n", len(rows), len(dag.Nodes))
	ctx.Emit(header+sb.String(), map[string]interface{}{"ok": true, "total": len(dag.Nodes), "count": len(rows), "nodes": rows})
	return nil
}

func nodeAdd(a *Args, rest []string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	title := strings.TrimSpace(a.Str("title"))
	if title == "" {
		return fail("--title is required")
	}
	scope := a.Str("scope")
	if scope == "" {
		scope = model.ScopeMine
	}
	if !model.ValidScope(scope) {
		return fail("invalid --scope %q (mine|collaborator|external|todo|out_of_scope)", scope)
	}
	status := a.Str("status")
	if status == "" {
		status = model.StatusPending
	}
	if !model.ValidStatus(status) {
		return fail("invalid --status %q", status)
	}
	if status == model.StatusDone {
		return fail("不能直接用 node add 创建 done 节点；请走 `reqboard done <id>` 硬门禁")
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		refs := a.Strs("prd-ref")
		if err := validateRefs(ctx, dag, refs, a.Bool("allow-unknown-refs")); err != nil {
			return err
		}
		id := a.Str("id")
		used := dag.NodeIDSet()
		if id == "" {
			id = model.NewID("rb", used, title, model.Now())
		} else if used[id] {
			return fail("node id %q already exists", id)
		}
		pos := model.Position{X: 100, Y: 100}
		if a.Has("x") || a.Has("y") {
			pos.X = floatArg(a, "x", 100)
			pos.Y = floatArg(a, "y", 100)
		} else if len(dag.Nodes) > 0 {
			maxX := 0.0
			for _, n := range dag.Nodes {
				if n.Position.X > maxX {
					maxX = n.Position.X
				}
			}
			pos = model.Position{X: maxX + 300, Y: 100}
		}
		n := &model.Node{
			ID: id, Title: title, Description: a.Str("description"),
			Scope: scope, Status: status,
			PRDRefs:     dedupStrings(refs),
			Tags:        dedupStrings(a.Strs("tag")),
			SelfMocks:   []model.SelfMock{},
			ReviewNotes: []model.ReviewNote{},
			Ops:         []model.OpItem{},
			SessionID:   a.Str("session"),
			Position:    pos,
			UpdatedAt:   model.Now(),
		}
		if ctx.Actor == "human" {
			n.HumanVersion = 1
		}
		dag.AddNode(n)
		if err := ctx.SaveDAG(dag, "node.add", id, title); err != nil {
			return err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD NODE ADD] %s  %s\n", id, title))
		sb.WriteString(fmt.Sprintf("  scope=%s status=%s prd_refs=%s\n", scope, status, strings.Join(n.PRDRefs, ",")))
		sb.WriteString("  下一步: `reqboard claim " + id + "` 获取 PRD 原文与门禁规则后再改代码。\n")
		ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "node": n})
		return nil
	})
}

func nodeUpdate(a *Args, id string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
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
		var changed []string
		if a.Has("title") {
			t := strings.TrimSpace(a.Str("title"))
			if t == "" {
				return fail("--title cannot be empty")
			}
			n.Title = t
			changed = append(changed, "title")
		}
		if a.Has("description") {
			n.Description = a.Str("description")
			changed = append(changed, "description")
		}
		if a.Has("scope") {
			s := a.Str("scope")
			if !model.ValidScope(s) {
				return fail("invalid --scope %q", s)
			}
			n.Scope = s
			changed = append(changed, "scope")
		}
		if a.Has("status") {
			s := a.Str("status")
			if !model.ValidStatus(s) {
				return fail("invalid --status %q", s)
			}
			if s == model.StatusDone {
				// Prerequisite nodes (external / collaborator / todo) have no
				// our-side code to gate; recording readiness is the whole point.
				if !model.IsPrerequisiteScope(n.Scope) {
					return fail("不能直接改 status=done；请用 `reqboard done <id>`（人类强覆盖用 `--force-human --actor human`）")
				}
				n.Status = model.StatusDone
				n.Stale = false
				n.StaleReason = ""
				changed = append(changed, "status=done")
			} else if s == model.StatusDeprecated {
				return fail("请使用 `reqboard node deprecate <id>`，以便保留历史 bindings 做残骸检测")
			} else {
				if n.Status == model.StatusDone && s != model.StatusDone && !model.IsPrerequisiteScope(n.Scope) && ctx.Actor != "human" {
					return &Error{
						Code: 1,
						Msg:  "only a human can reopen a done node",
						Text: fmt.Sprintf("[REQBOARD BLOCKED] 节点 %s 已 done，非人类不能通过 node update 降级重开（-> %s）。\n  -> 如需返工请人类在看板点「打回 + 写批注」或执行 `reqboard node review %s --request-changes --note \"...\" --actor human`。\n", id, s, id),
					}
				}
				n.Status = s
				changed = append(changed, "status")
			}
		}
		if a.Has("hold") {
			n.Hold = parseBool(a.Str("hold"), true)
			changed = append(changed, fmt.Sprintf("hold=%v", n.Hold))
		}
		if a.Has("stale") {
			want := parseBool(a.Str("stale"), true)
			if !want && ctx.Actor != "human" {
				return &Error{Code: 1, Msg: "only a human can clear stale", Text: "[REQBOARD BLOCKED] 只有人类可以清除 stale 标记。\n  -> 请人类在看板点击，或 `reqboard node update " + id + " --stale=false --actor human`。\n"}
			}
			if want {
				n.SetStale(a.Str("reason"))
			} else {
				n.Stale = false
				n.StaleReason = ""
			}
			changed = append(changed, fmt.Sprintf("stale=%v", want))
		}
		if a.Has("tag") {
			n.Tags = dedupStrings(a.Strs("tag"))
			changed = append(changed, "tags")
		}
		if a.Has("prd-ref") {
			refs := a.Strs("prd-ref")
			if err := validateRefs(ctx, dag, refs, a.Bool("allow-unknown-refs")); err != nil {
				return err
			}
			n.PRDRefs = dedupStrings(refs)
			changed = append(changed, "prd_refs")
		}
		if a.Has("x") || a.Has("y") {
			n.Position.X = floatArg(a, "x", n.Position.X)
			n.Position.Y = floatArg(a, "y", n.Position.Y)
			changed = append(changed, "position")
		}
		if a.Has("session") {
			n.SessionID = a.Str("session")
			changed = append(changed, "session")
		}
		if a.Bool("reset-attempts") {
			n.Attempts = 0
			n.LastError = ""
			changed = append(changed, "attempts=0")
		}
		external := isExternalScope(n.Scope)
		prereq := model.IsPrerequisiteScope(n.Scope)
		unmock := a.Bool("unmock-downstream")
		if unmock && !prereq {
			return fail("--unmock-downstream 仅适用于 external/collaborator/todo 节点")
		}
		mocked := mockedOutEdges(dag, id)
		var switched, stale []string
		if unmock {
			if n.Status != model.StatusDone {
				return fail("--unmock-downstream 需要先把前置依赖标记 done")
			}
			switched, stale = applyUnmockDownstream(dag, n, ctx.Actor)
			changed = append(changed, fmt.Sprintf("unmock_downstream=%d", len(switched)))
		}
		if len(changed) == 0 {
			return fail("no update flags provided")
		}
		var unlocked []string
		if prereq && n.Status == model.StatusDone {
			for _, ed := range dag.OutEdges(id) {
				if !model.BlockingEdge(ed.Kind) {
					continue
				}
				target := dag.Node(ed.To)
				if target == nil || target.Status == model.StatusDone || target.Status == model.StatusDeprecated {
					continue
				}
				if allUpstreamDone(dag, ed.To) {
					unlocked = append(unlocked, ed.To)
				}
			}
		}
		n.UpdatedAt = model.Now()
		if ctx.Actor == "human" {
			n.HumanVersion++
		}
		if err := ctx.SaveDAG(dag, "node.update", id, strings.Join(changed, ", ")); err != nil {
			return err
		}
		if n.Status != model.StatusInProgress {
			releaseNodeLeases(ctx, id, "node.update")
		}
		if len(switched) > 0 {
			_ = ctx.Store.AppendEvent(ctx.event("external.unmock_downstream", id,
				fmt.Sprintf("edges=%s stale=%s", strings.Join(switched, ","), strings.Join(stale, ","))))
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD NODE UPDATE] %s %s\n  已更新: %s\n", id, n.Title, strings.Join(changed, ", ")))
		if prereq && n.Status == model.StatusDone && (a.Has("status") || unmock) {
			if unmock {
				writeUnmockResult(&sb, switched, stale)
			} else {
				writeMockedEdgeHint(&sb, id, mocked)
			}
		}
		if len(unlocked) > 0 {
			sb.WriteString("  解锁下游: " + strings.Join(unlocked, ", ") + "\n")
		}
		ctx.Emit(sb.String(), map[string]interface{}{
			"ok": true, "node": n, "changed": changed, "external": external,
			"todo": n.Scope == model.ScopeTodo, "prerequisite": prereq, "unlocked": unlocked,
			"mocked_edges": mocked, "unmocked_edges": switched, "stale_nodes": stale,
		})
		return nil
	})
}

func nodeDeprecate(a *Args, id string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
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
		n.Status = model.StatusDeprecated
		n.UpdatedAt = model.Now()
		reason := a.Str("reason")
		bindings, _ := ctx.Store.LoadBindings()
		if bindings != nil {
			bindings.RemoveLeases(id)
			if err := ctx.SaveBindings(bindings, "node.deprecate", id, reason); err != nil {
				return err
			}
		}
		if err := ctx.SaveDAG(dag, "node.deprecate", id, reason); err != nil {
			return err
		}
		kept := 0
		if bindings != nil {
			kept = len(bindings.BindingsForNode(id))
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD NODE DEPRECATE] %s %s\n", id, n.Title))
		sb.WriteString(fmt.Sprintf("  已设为 deprecated；保留历史 bindings %d 条用于 To-Delete 残骸检测（不静默清空）。\n", kept))
		sb.WriteString("  -> 清理残留代码后 `reqboard check --all` 会自动消红；代码已删净可 `reqboard unbind <binding-id>`。\n")
		if reason != "" {
			sb.WriteString("  原因: " + reason + "\n")
		}
		ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "node_id": id, "status": n.Status, "kept_bindings": kept})
		return nil
	})
}

// nodeDelete removes a mis-created node for real. A node with bindings is
// refused unless --force is passed, so accidental deletion of code ownership
// stays recoverable via `node deprecate` (To-Delete debris tracking).
func nodeDelete(a *Args, id string) *Error {
	force := a.Bool("force")
	ctx, e := NewCtx(a)
	if e != nil {
		return e
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
		bindings, berr := ctx.Store.LoadBindings()
		if berr != nil {
			return fail("%v", berr)
		}
		owned := bindings.BindingsForNode(id)
		if len(owned) > 0 && !force {
			var refs []string
			for _, b := range owned {
				refs = append(refs, fmt.Sprintf("%s(%s:%s::%s)", b.ID, b.Repo, b.File, b.Symbol))
			}
			return &Error{
				Code: 1, Msg: "node still has bindings",
				Text: fmt.Sprintf("[REQBOARD BLOCKED] 节点 %s 仍有 %d 条 bindings：%s\n  -> 若只是需求砍掉/方案推翻，请用 `reqboard node deprecate %s --reason \"...\"` 保留 To-Delete 残骸追踪；\n  -> 若确认是误建节点，加 `--force` 连同 bindings 与关联边一并删除。\n",
					id, len(owned), strings.Join(refs, ", "), id),
			}
		}
		removedBindings := 0
		if len(owned) > 0 || bindings.LeaseFor(id) != nil {
			var kept []model.Binding
			for _, b := range bindings.Bindings {
				if b.NodeID == id {
					removedBindings++
					continue
				}
				kept = append(kept, b)
			}
			bindings.Bindings = kept
			bindings.RemoveLeases(id)
			if err := ctx.SaveBindings(bindings, "node.delete.bindings", id, fmt.Sprintf("removed %d bindings", removedBindings)); err != nil {
				return err
			}
		}
		removedEdges := 0
		var keptEdges []*model.Edge
		for _, ed := range dag.Edges {
			if ed.From == id || ed.To == id {
				removedEdges++
				continue
			}
			keptEdges = append(keptEdges, ed)
		}
		dag.Edges = keptEdges
		var keptNodes []*model.Node
		for _, x := range dag.Nodes {
			if x.ID == id {
				continue
			}
			keptNodes = append(keptNodes, x)
		}
		dag.Nodes = keptNodes
		detail := fmt.Sprintf("removed node, %d edges, %d bindings", removedEdges, removedBindings)
		if err := ctx.SaveDAG(dag, "node.delete", id, detail); err != nil {
			return err
		}
		ctx.Emit(fmt.Sprintf("[REQBOARD NODE DELETE] %s %s\n  已删除节点、%d 条关联边、%d 条 bindings；可用 `reqboard undo` 一键还原。\n",
			id, n.Title, removedEdges, removedBindings),
			map[string]interface{}{
				"ok": true, "node_id": id, "title": n.Title,
				"removed_edges": removedEdges, "removed_bindings": removedBindings, "forced": force,
			})
		return nil
	})
}

func nodeApprove(a *Args, id string) *Error {
	return transitionNode(a, id, model.StatusProposed, model.StatusPending, "node.approve", "批准")
}

func nodeReject(a *Args, id string) *Error {
	return transitionNode(a, id, model.StatusProposed, model.StatusDeprecated, "node.reject", "驳回")
}

func transitionNode(a *Args, id, from, to, action, verb string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
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
		if n.Status != from {
			return fail("node %s is %s, only %s nodes can be %sd", id, n.Status, from, verb)
		}
		n.Status = to
		n.UpdatedAt = model.Now()
		if ctx.Actor == "human" {
			n.HumanVersion++
		}
		detail := a.Str("reason")
		if err := ctx.SaveDAG(dag, action, id, detail); err != nil {
			return err
		}
		msg := fmt.Sprintf("[REQBOARD NODE %s] %s %s -> %s\n", strings.ToUpper(action[len("node."):]), id, n.Title, to)
		if to == model.StatusPending {
			msg += "  下一步: `reqboard claim " + id + "` 开始实施。\n"
		} else {
			msg += "  节点已保留在 DAG 中（deprecated），历史 bindings 不会被清理。\n"
		}
		ctx.Emit(msg, map[string]interface{}{"ok": true, "node_id": id, "status": to})
		return nil
	})
}

func nodeReview(a *Args, id string) *Error {
	pass := a.Bool("pass")
	request := a.Bool("request-changes")
	if pass == request {
		return fail("必须且只能指定 --pass 或 --request-changes")
	}
	ctx, e := NewCtx(a)
	if e != nil {
		return e
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
		if n.Status == model.StatusDeprecated {
			return fail("cannot review a deprecated node")
		}
		used := map[string]bool{}
		for _, r := range n.ReviewNotes {
			used[r.ID] = true
		}
		var added []string
		var addedText []string
		if request {
			notes := a.Strs("note")
			if len(notes) == 0 {
				notes = []string{"请按 PRD 修正后重新提交 Review"}
			}
			target := a.Str("target")
			for _, c := range notes {
				addedText = append(addedText, c)
				noteID := model.NewSequentialID("rv", used)
				used[noteID] = true
				n.ReviewNotes = append(n.ReviewNotes, model.ReviewNote{
					ID: noteID, Author: ctx.Actor, Status: "open", Target: target,
					Comment: c, CreatedAt: model.Now(),
				})
				added = append(added, noteID)
			}
			n.Status = model.StatusChangesRequested
		} else {
			if ctx.Actor != "human" {
				var humanNotes []string
				for _, r := range n.ReviewNotes {
					if r.Status == "open" && r.Author == "human" {
						humanNotes = append(humanNotes, r.ID)
					}
				}
				if len(humanNotes) > 0 {
					ids := strings.Join(humanNotes, ", ")
					return &Error{
						Code: 1,
						Msg:  "human review note requires human pass",
						Text: fmt.Sprintf(
							"[REQBOARD BLOCKED] 节点 %s 存在人类写下的未闭环 Review 批注（%s），只能由人类审核通过，禁止 Agent 自审放行。\n  -> Agent 修复后请执行 `reqboard node update %s --status implemented` 释放租约并回报，由人类在看板点「Reviewed 通过」或执行 `reqboard node review %s --pass --actor human`。\n",
							id, ids, id, id,
						),
						Data: map[string]interface{}{"ok": false, "node_id": id, "human_open_notes": humanNotes},
					}
				}
			}
			for i := range n.ReviewNotes {
				if n.ReviewNotes[i].Status == "open" {
					n.ReviewNotes[i].Status = "resolved"
					n.ReviewNotes[i].ResolvedAt = model.Now()
				}
			}
			n.Status = model.StatusReviewed
		}
		n.UpdatedAt = model.Now()
		if ctx.Actor == "human" {
			n.HumanVersion++
		}
		action := "node.review.pass"
		if request {
			action = "node.review.changes_requested"
		}
		if err := ctx.SaveDAG(dag, action, id, strings.Join(added, ",")); err != nil {
			return err
		}
		var sb strings.Builder
		if request {
			sb.WriteString(fmt.Sprintf("[REQBOARD REVIEW] %s 打回 -> changes_requested\n", id))
			for i, nid := range added {
				sb.WriteString(fmt.Sprintf("  - 批注 %s: %s\n", nid, addedText[i]))
			}
			sb.WriteString("  -> 下一轮 dispatch 会以 P0 派发修复；Agent 需 claim 后按批注修改。\n")
		} else {
			sb.WriteString(fmt.Sprintf("[REQBOARD REVIEW] %s 通过 -> reviewed（open 批注已自动关闭）\n", id))
			sb.WriteString("  下一步: `reqboard done " + id + "` 执行 4 道完结硬门禁。\n")
		}
		ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "node_id": id, "status": n.Status, "added_notes": added})
		return nil
	})
}

func nodeFail(a *Args, id string) *Error {
	msg := strings.TrimSpace(a.Str("error"))
	if msg == "" {
		return fail("--error is required")
	}
	ctx, e := NewCtx(a)
	if e != nil {
		return e
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
		n.Attempts++
		n.LastError = msg
		if n.Status == model.StatusInProgress || n.Status == model.StatusMocked {
			n.Status = model.StatusPending
		}
		n.UpdatedAt = model.Now()
		if err := ctx.SaveDAG(dag, "node.fail", id, msg); err != nil {
			return err
		}
		releaseNodeLeases(ctx, id, "node.fail")
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD NODE FAIL] %s attempts=%d\n", id, n.Attempts))
		if n.Attempts >= 2 {
			sb.WriteString("  [CIRCUIT BREAKER] 已连续失败 >=2 次，移出自动调度队列，需人工介入（reset: `reqboard node update " + id + " --reset-attempts`）。\n")
		}
		ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "node_id": id, "attempts": n.Attempts, "circuit_broken": n.Attempts >= 2})
		return nil
	})
}

func validateRefs(ctx *Ctx, dag *model.DAG, refs []string, allowUnknown bool) *Error {
	var cleaned []string
	for _, r := range refs {
		if strings.TrimSpace(r) != "" {
			cleaned = append(cleaned, r)
		}
	}
	if len(cleaned) == 0 || allowUnknown {
		return nil
	}
	anchors := map[string]bool{}
	mainDoc := loadPRDDoc(ctx)
	hasMain := false
	if mainDoc != nil {
		hasMain = true
		for a := range mainDoc.AnchorSet() {
			anchors[a] = true
		}
	}
	sources := loadSourceDocs(ctx, dag)
	for _, doc := range sources {
		for a := range doc.AnchorSet() {
			anchors[a] = true
		}
	}
	if !hasMain && len(sources) == 0 {
		return nil
	}
	for _, r := range cleaned {
		if anchors[r] {
			continue
		}
		prefix := prd.PrefixOf(r)
		hint := "左栏查看真实锚点，或先运行 `reqboard prd init/sync`"
		if prefix != "" && prefix != prd.DefaultPrefix {
			if dag.Source(prefix) == nil {
				hint = fmt.Sprintf("补充来源 %q 未登记；先运行 `reqboard source add ... --id %s` 或检查拼写", prefix, prefix)
			} else {
				hint = fmt.Sprintf("补充来源 %q 中不存在该锚点；先运行 `reqboard source sync %s <new.md>` 或左栏查看", prefix, prefix)
			}
		}
		return &Error{Code: 1, Msg: "unknown anchor " + r, Text: fmt.Sprintf("[REQBOARD BLOCKED] 锚点 %q 不存在。\n  -> %s；确需跳过校验加 --allow-unknown-refs。\n", r, hint)}
	}
	return nil
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func parseBool(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	case "":
		return def
	}
	return def
}

func floatArg(a *Args, name string, def float64) float64 {
	s := a.Str(name)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}
