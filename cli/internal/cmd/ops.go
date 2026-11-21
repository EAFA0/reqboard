package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/model"
)

func runOps(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return &Error{Code: 2, Msg: "usage: reqboard ops add|toggle|remove ...", Text: "用法: reqboard ops add <node_id> --kind ddl|gateway|flag|other --title \"...\" [--note \"...\"] | ops toggle <node_id> <op_id> | ops remove <node_id> <op_id>"}
	}
	switch pos[0] {
	case "add":
		if len(pos) < 2 {
			return fail("usage: reqboard ops add <node_id> --kind ddl|gateway|flag|other --title \"...\" [--note \"...\"]")
		}
		return opsAdd(a, pos[1])
	case "toggle":
		if len(pos) < 3 {
			return fail("usage: reqboard ops toggle <node_id> <op_id>")
		}
		return opsToggle(a, pos[1], pos[2])
	case "remove", "rm":
		if len(pos) < 3 {
			return fail("usage: reqboard ops remove <node_id> <op_id>")
		}
		return opsRemove(a, pos[1], pos[2])
	}
	return &Error{Code: 2, Msg: "unknown ops subcommand " + pos[0]}
}

func opsAdd(a *Args, nodeID string) *Error {
	kind := strings.TrimSpace(a.Str("kind"))
	if kind == "" {
		kind = model.OpOther
	}
	if !model.ValidOpKind(kind) {
		return fail("invalid --kind %q (ddl|gateway|flag|other)", kind)
	}
	title := strings.TrimSpace(a.Str("title"))
	if title == "" {
		return fail("--title is required")
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
		n, ne := findNodeOrFail(dag, nodeID)
		if ne != nil {
			return ne
		}
		used := map[string]bool{}
		for _, op := range n.Ops {
			used[op.ID] = true
		}
		op := model.OpItem{
			ID: model.NewSequentialID("op", used), Kind: kind,
			Title: title, Note: strings.TrimSpace(a.Str("note")),
		}
		n.Ops = append(n.Ops, op)
		n.UpdatedAt = model.Now()
		if ctx.Actor == "human" {
			n.HumanVersion++
		}
		if err := ctx.SaveDAG(dag, "ops.add", nodeID, fmt.Sprintf("%s %s: %s", op.ID, kind, title)); err != nil {
			return err
		}
		ctx.Emit(fmt.Sprintf("[REQBOARD OPS] %s 已新增 %s [%s] %s\n", nodeID, op.ID, kind, title),
			map[string]interface{}{"ok": true, "node_id": nodeID, "op": op})
		return nil
	})
}

func opsToggle(a *Args, nodeID, opID string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		n, ne := findNodeOrFail(dag, nodeID)
		if ne != nil {
			return ne
		}
		op := findOp(n, opID)
		if op == nil {
			return fail("op %s not found on node %s", opID, nodeID)
		}
		op.Done = !op.Done
		n.UpdatedAt = model.Now()
		if ctx.Actor == "human" {
			n.HumanVersion++
		}
		state := "未完成"
		if op.Done {
			state = "已完成"
		}
		if err := ctx.SaveDAG(dag, "ops.toggle", nodeID, fmt.Sprintf("%s done=%t", opID, op.Done)); err != nil {
			return err
		}
		ctx.Emit(fmt.Sprintf("[REQBOARD OPS] %s %s 已标记为%s\n", nodeID, opID, state),
			map[string]interface{}{"ok": true, "node_id": nodeID, "op": op})
		return nil
	})
}

func opsRemove(a *Args, nodeID, opID string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		n, ne := findNodeOrFail(dag, nodeID)
		if ne != nil {
			return ne
		}
		var kept []model.OpItem
		var removed *model.OpItem
		for i := range n.Ops {
			if n.Ops[i].ID == opID {
				op := n.Ops[i]
				removed = &op
				continue
			}
			kept = append(kept, n.Ops[i])
		}
		if removed == nil {
			return fail("op %s not found on node %s", opID, nodeID)
		}
		n.Ops = kept
		n.UpdatedAt = model.Now()
		if ctx.Actor == "human" {
			n.HumanVersion++
		}
		if err := ctx.SaveDAG(dag, "ops.remove", nodeID, fmt.Sprintf("%s %s", opID, removed.Title)); err != nil {
			return err
		}
		ctx.Emit(fmt.Sprintf("[REQBOARD OPS] %s 已移除 %s [%s] %s\n", nodeID, opID, removed.Kind, removed.Title),
			map[string]interface{}{"ok": true, "node_id": nodeID, "op": removed})
		return nil
	})
}

func findOp(n *model.Node, opID string) *model.OpItem {
	if n == nil {
		return nil
	}
	for i := range n.Ops {
		if n.Ops[i].ID == opID {
			return &n.Ops[i]
		}
	}
	return nil
}
