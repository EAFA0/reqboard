package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/model"
)

func runMock(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) < 2 {
		return &Error{Code: 2, Msg: "usage: reqboard mock add|resolve <node-id> --desc <d> [--edge <edge-id>]"}
	}
	action, id := pos[0], pos[1]
	switch action {
	case "add":
		return mockAdd(a, id)
	case "resolve", "remove", "clear":
		return mockResolve(a, id)
	}
	return &Error{Code: 2, Msg: "unknown mock subcommand " + action}
}

func mockAdd(a *Args, id string) *Error {
	desc := strings.TrimSpace(a.Str("desc"))
	if desc == "" {
		return fail("--desc is required")
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
			return fail("cannot add mock debt to a deprecated node")
		}
		if edgeID := a.Str("edge"); edgeID != "" {
			ed := dag.Edge(edgeID)
			if ed == nil {
				return fail("edge not found: %s", edgeID)
			}
			if ed.From != id && ed.To != id {
				return fail("edge %s is not connected to node %s", edgeID, id)
			}
			ed.Kind = model.EdgeMocked
			if ed.Note == "" {
				ed.Note = desc
			} else if !strings.Contains(ed.Note, desc) {
				ed.Note += "；" + desc
			}
			ed.UpdatedBy = ctx.Actor
			ed.UpdatedAt = model.Now()
			if err := ctx.SaveDAG(dag, "mock.add.edge", id, edgeID+": "+desc); err != nil {
				return err
			}
			msg := fmt.Sprintf("[REQBOARD MOCK] 边 %s 已切为 mocked\n  备注: %s\n  [BLOCK] 存在 mocked 入边时下游不能 done。\n", edgeID, ed.Note)
			ctx.Emit(msg, map[string]interface{}{"ok": true, "edge": ed})
			return nil
		}
		used := map[string]bool{}
		for _, m := range n.SelfMocks {
			used[m.ID] = true
		}
		mockID := model.NewSequentialID("mk", used)
		n.SelfMocks = append(n.SelfMocks, model.SelfMock{ID: mockID, Desc: desc, CreatedAt: model.Now()})
		if n.Status == model.StatusPending || n.Status == model.StatusInProgress || n.Status == model.StatusChangesRequested {
			n.Status = model.StatusMocked
		}
		n.UpdatedAt = model.Now()
		if err := ctx.SaveDAG(dag, "mock.add", id, mockID+": "+desc); err != nil {
			return err
		}
		msg := fmt.Sprintf("[REQBOARD MOCK] %s 新增 self mock %s\n  %s\n  [BLOCK] 未销 self_mocks 会阻塞 done；拆完后 `reqboard mock resolve %s --mock %s`。\n", id, mockID, desc, id, mockID)
		ctx.Emit(msg, map[string]interface{}{"ok": true, "mock": n.SelfMocks[len(n.SelfMocks)-1], "node_id": id})
		return nil
	})
}

func mockResolve(a *Args, id string) *Error {
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
		if edgeID := a.Str("edge"); edgeID != "" {
			ed := dag.Edge(edgeID)
			if ed == nil {
				return fail("edge not found: %s", edgeID)
			}
			if ed.From != id && ed.To != id {
				return fail("edge %s is not connected to node %s", edgeID, id)
			}
			ed.Kind = model.EdgeHard
			ed.UpdatedBy = ctx.Actor
			ed.UpdatedAt = model.Now()
			target := dag.Node(ed.To)
			if target != nil {
				target.SetStale(fmt.Sprintf("入边 %s 刚由 mocked 切回 hard，需拆 Mock 并重核 %s 的对接契约", ed.ID, ed.From))
				target.UpdatedAt = model.Now()
			}
			if err := ctx.SaveDAG(dag, "mock.resolve.edge", id, edgeID); err != nil {
				return err
			}
			msg := fmt.Sprintf("[REQBOARD MOCK] 边 %s 已切回 hard\n  [AUTO STALE] 下游 %s 已标 stale，请拆 Mock 并复检。\n", edgeID, ed.To)
			ctx.Emit(msg, map[string]interface{}{"ok": true, "edge": ed, "stale_node": ed.To})
			return nil
		}
		mockID := a.Str("mock")
		desc := strings.TrimSpace(a.Str("desc"))
		var pending []model.SelfMock
		targetIdx := -1
		var descMatches []int
		for i := range n.SelfMocks {
			if n.SelfMocks[i].Resolved {
				continue
			}
			pending = append(pending, n.SelfMocks[i])
			if mockID != "" && n.SelfMocks[i].ID == mockID {
				targetIdx = i
			}
			if desc != "" && strings.Contains(strings.ToLower(n.SelfMocks[i].Desc), strings.ToLower(desc)) {
				descMatches = append(descMatches, i)
			}
		}
		if targetIdx < 0 && desc != "" {
			switch len(descMatches) {
			case 1:
				targetIdx = descMatches[0]
			case 0:
				// fall through to the error below
			default:
				var ids []string
				for _, i := range descMatches {
					ids = append(ids, n.SelfMocks[i].ID+"("+n.SelfMocks[i].Desc+")")
				}
				return fail("multiple self mocks match --desc %q; use --mock <id>: %s", desc, strings.Join(ids, ", "))
			}
		}
		if targetIdx < 0 && mockID == "" && desc == "" && len(pending) == 1 {
			for i := range n.SelfMocks {
				if !n.SelfMocks[i].Resolved {
					targetIdx = i
				}
			}
		}
		if targetIdx < 0 {
			var ids []string
			for _, m := range pending {
				ids = append(ids, m.ID+"("+m.Desc+")")
			}
			if len(ids) == 0 {
				return fail("node %s has no unresolved self mocks", id)
			}
			return fail("cannot identify mock; use --mock <id> or --desc. Open: %s", strings.Join(ids, ", "))
		}
		target := &n.SelfMocks[targetIdx]
		target.Resolved = true
		target.ResolvedAt = model.Now()
		if len(n.PendingMocks()) == 0 && n.Status == model.StatusMocked {
			n.Status = model.StatusImplemented
		}
		n.UpdatedAt = model.Now()
		if err := ctx.SaveDAG(dag, "mock.resolve", id, target.ID+": "+target.Desc); err != nil {
			return err
		}
		msg := fmt.Sprintf("[REQBOARD MOCK] %s 已销账 %s\n  %s\n", id, target.ID, target.Desc)
		if len(n.PendingMocks()) == 0 {
			msg += "  self_mocks 已全部清空；继续 `reqboard check --node " + id + "` 与 `reqboard done " + id + "`。\n"
		}
		ctx.Emit(msg, map[string]interface{}{"ok": true, "node_id": id, "resolved_mock": target.ID, "remaining": len(n.PendingMocks())})
		return nil
	})
}
