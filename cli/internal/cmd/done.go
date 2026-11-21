package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/audit"
	"reqboard/internal/gate"
	"reqboard/internal/model"
)

func runDone(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return fail("usage: reqboard done <node-id> [--evidence <e>...] [--force-human]")
	}
	id := pos[0]
	force := a.Bool("force-human")
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	if force && ctx.Actor != "human" {
		return &Error{Code: 1, Msg: "force-human requires a human actor", Text: "[REQBOARD BLOCKED] --force-human 只允许人类使用。\n  -> 人类执行: reqboard done " + id + " --force-human --actor human\n"}
	}
	cfg, cerr := ctx.Store.LoadConfig()
	if cerr != nil {
		return fail("%v", cerr)
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
			return &Error{Code: 1, Msg: "cannot complete a deprecated node", Text: "[REQBOARD BLOCKED] 节点已 deprecated，不能标 done；请走 To-Delete 残骸清理流程。\n"}
		}
		prereq := model.IsPrerequisiteScope(n.Scope)
		external := isExternalScope(n.Scope)
		unmock := a.Bool("unmock-downstream")
		if unmock && !prereq {
			return fail("--unmock-downstream 仅适用于 external/collaborator/todo 节点")
		}
		if n.Status == model.StatusDone {
			if !prereq || !unmock {
				return fail("node %s is already done", id)
			}
			// Late unmock: the dependency was already marked done but the
			// mocked downstream contracts still need to be flipped to hard.
			mocked := mockedOutEdges(dag, id)
			switched, stale := applyUnmockDownstream(dag, n, ctx.Actor)
			n.UpdatedAt = model.Now()
			if err := ctx.SaveDAG(dag, "done.unmock", id, "already done"); err != nil {
				return err
			}
			if len(switched) > 0 {
				_ = ctx.Store.AppendEvent(ctx.event("external.unmock_downstream", id,
					fmt.Sprintf("edges=%s stale=%s", strings.Join(switched, ","), strings.Join(stale, ","))))
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("[REQBOARD DONE] %s  %s  ✔ (already done)\n", id, n.Title))
			writeUnmockResult(&sb, switched, stale)
			ctx.Emit(sb.String(), map[string]interface{}{
				"ok": true, "node_id": id, "status": n.Status, "external": true,
				"already_done": true, "mocked_edges": mocked,
				"unmocked_edges": switched, "stale_nodes": stale,
			})
			return nil
		}
		evidence := a.Strs("evidence")
		if !prereq {
			bindings, berr := ctx.Store.LoadBindings()
			if berr != nil {
				return fail("%v", berr)
			}
			if !force {
				rep := audit.Run(cfg, ctx.Root, dag, bindings, audit.Options{NodeID: id})
				vs := gate.Evaluate(dag, bindings, rep, id)
				if len(vs) > 0 {
					return &Error{
						Code: 1,
						Msg:  "done gates failed",
						Text: gate.Format(id, n.Title, vs),
						Data: map[string]interface{}{"node_id": id, "violations": vs, "check": rep},
					}
				}
			}
		}
		mocked := mockedOutEdges(dag, id)
		applyEvidence(&n.Evidence, evidence)
		n.Status = model.StatusDone
		n.Stale = false
		n.StaleReason = ""
		n.UpdatedAt = model.Now()
		var switched, stale []string
		if prereq && unmock {
			switched, stale = applyUnmockDownstream(dag, n, ctx.Actor)
		}
		if err := ctx.SaveDAG(dag, "done", id, strings.Join(evidence, "; ")); err != nil {
			return err
		}
		releaseNodeLeases(ctx, id, "done")
		if len(switched) > 0 {
			_ = ctx.Store.AppendEvent(ctx.event("external.unmock_downstream", id,
				fmt.Sprintf("edges=%s stale=%s", strings.Join(switched, ","), strings.Join(stale, ","))))
		}
		var unlocked []string
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
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD DONE] %s  %s  ✔\n", id, n.Title))
		if prereq {
			if n.Scope == model.ScopeTodo {
				sb.WriteString("  scope=todo：前置 TODO 节点无我方代码门禁，直接标记 done。\n")
			} else {
				sb.WriteString(fmt.Sprintf("  scope=%s：外部/协作者节点无我方代码门禁，直接标记 done。\n", n.Scope))
			}
			if unmock {
				writeUnmockResult(&sb, switched, stale)
			} else {
				writeMockedEdgeHint(&sb, id, mocked)
			}
		} else if force {
			sb.WriteString("  [HUMAN OVERRIDE] 人类使用 --force-human 强覆盖了 4 道完结门禁。\n")
		} else {
			sb.WriteString("  4 道门禁: 前置完结 / 零 Mock 债 / 非陈旧且无未闭环批注 / 代码干净 全部 PASS\n")
		}
		if len(evidence) > 0 {
			sb.WriteString("  evidence: " + strings.Join(evidence, ", ") + "\n")
		}
		if len(unlocked) > 0 {
			sb.WriteString("  解锁下游: " + strings.Join(unlocked, ", ") + "\n")
		}
		sb.WriteString("  下一步: `reqboard dispatch` 继续推进；全部完成后会返回 QUIESCENT。\n")
		ctx.Emit(sb.String(), map[string]interface{}{
			"ok": true, "node_id": id, "status": n.Status, "external": external,
			"todo": n.Scope == model.ScopeTodo, "prerequisite": prereq,
			"forced": force, "evidence": n.Evidence, "unlocked": unlocked,
			"mocked_edges": mocked, "unmocked_edges": switched, "stale_nodes": stale,
		})
		return nil
	})
}

// releaseNodeLeases drops active leases for a node once it leaves in_progress
// (done / implemented / mocked / reviewed / pending / fail) so finished or
// failed workers never block subsequent dispatch waves or mask Orphan Diffs.
func releaseNodeLeases(ctx *Ctx, id, trigger string) {
	bindings, err := ctx.Store.LoadBindings()
	if err != nil || bindings == nil {
		return
	}
	before := len(bindings.Leases)
	bindings.RemoveLeases(id)
	if len(bindings.Leases) == before {
		return
	}
	_ = ctx.Store.SaveBindings(bindings)
	_ = ctx.Store.AppendEvent(ctx.event("lease.release", id, trigger))
}

func allUpstreamDone(dag *model.DAG, id string) bool {
	for _, ed := range dag.InEdges(id) {
		if !model.BlockingEdge(ed.Kind) {
			continue
		}
		from := dag.Node(ed.From)
		if from == nil || from.Status != model.StatusDone {
			return false
		}
	}
	return true
}

func applyEvidence(ev *model.Evidence, items []string) {
	for _, e := range items {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		switch {
		case strings.HasPrefix(e, "trace_id:"):
			ev.TraceIDs = appendUniqueStr(ev.TraceIDs, strings.TrimPrefix(e, "trace_id:"))
		case strings.HasPrefix(e, "logid:"):
			// `logid:` is accepted as a generic alias for trace IDs.
			ev.TraceIDs = appendUniqueStr(ev.TraceIDs, strings.TrimPrefix(e, "logid:"))
		case strings.HasPrefix(e, "test:"):
			ev.TestCases = appendUniqueStr(ev.TestCases, strings.TrimPrefix(e, "test:"))
		case strings.HasPrefix(e, "tc:"):
			ev.TestCases = appendUniqueStr(ev.TestCases, strings.TrimPrefix(e, "tc:"))
		default:
			ev.Extra = appendUniqueStr(ev.Extra, e)
		}
	}
}

func appendUniqueStr(in []string, s string) []string {
	for _, x := range in {
		if x == s {
			return in
		}
	}
	return append(in, s)
}
