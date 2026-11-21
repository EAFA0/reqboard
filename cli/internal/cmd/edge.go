package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/model"
)

func runEdge(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return &Error{Code: 2, Msg: "usage: reqboard edge add|update|remove ..."}
	}
	switch pos[0] {
	case "add":
		return edgeAdd(a)
	case "update":
		if len(pos) < 2 {
			return fail("usage: reqboard edge update <edge-id> [--kind ...] [--note ...]")
		}
		return edgeUpdate(a, pos[1])
	case "remove", "rm", "delete":
		if len(pos) < 2 {
			return fail("usage: reqboard edge remove <edge-id>")
		}
		return edgeRemove(a, pos[1])
	}
	return &Error{Code: 2, Msg: "unknown edge subcommand " + pos[0]}
}

func edgeAdd(a *Args) *Error {
	from, to := a.Str("from"), a.Str("to")
	if from == "" || to == "" {
		return fail("--from and --to are required")
	}
	kind := a.Str("kind")
	if kind == "" {
		kind = model.EdgeHard
	}
	if !model.ValidEdgeKind(kind) {
		return fail("invalid --kind %q (hard|mocked|stale|discovered)", kind)
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
		if _, ne := findNodeOrFail(dag, from); ne != nil {
			return ne
		}
		if _, ne := findNodeOrFail(dag, to); ne != nil {
			return ne
		}
		if from == to {
			return fail("self edge is not allowed")
		}
		for _, e := range dag.Edges {
			if e.From == from && e.To == to {
				return fail("edge already exists: %s (%s -> %s, %s); use `reqboard edge update %s --kind ...`", e.ID, from, to, e.Kind, e.ID)
			}
		}
		if reachable(dag, to, from) {
			return &Error{Code: 1, Msg: "edge would create a cycle", Text: fmt.Sprintf("[REQBOARD BLOCKED] 添加 %s -> %s 会形成环（%s 已可达 %s）。\n  -> DAG 必须无环；请调整依赖方向。\n", from, to, to, from)}
		}
		id := a.Str("id")
		used := dag.EdgeIDSet()
		if id == "" {
			id = model.NewID("ed", used, from, to, kind, model.Now())
		} else if used[id] {
			return fail("edge id %q already exists", id)
		}
		edge := &model.Edge{ID: id, From: from, To: to, Kind: kind, Note: a.Str("note"), UpdatedBy: ctx.Actor, UpdatedAt: model.Now()}
		dag.AddEdge(edge)
		if err := ctx.SaveDAG(dag, "edge.add", to, fmt.Sprintf("%s -> %s (%s)", from, to, kind)); err != nil {
			return err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD EDGE ADD] %s  %s -> %s  kind=%s\n", id, from, to, kind))
		switch kind {
		case model.EdgeMocked:
			sb.WriteString("  [MOCK] 该链路已标记 mocked：下游 done 将被锁死，claim 会自动注入此备注。\n")
			if edge.Note != "" {
				sb.WriteString("  备注: " + edge.Note + "\n")
			}
		case model.EdgeStale:
			sb.WriteString("  [STALE] 该契约待重核，会阻塞下游 done。\n")
		case model.EdgeDiscovered:
			sb.WriteString("  [DISCOVERED] 非阻断溯源边。\n")
		}
		ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "edge": edge})
		return nil
	})
}

func edgeUpdate(a *Args, id string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		edge := dag.Edge(id)
		if edge == nil {
			return fail("edge not found: %s", id)
		}
		var changed []string
		oldKind := edge.Kind
		if a.Has("kind") {
			k := a.Str("kind")
			if !model.ValidEdgeKind(k) {
				return fail("invalid --kind %q", k)
			}
			edge.Kind = k
			changed = append(changed, "kind="+k)
		}
		if a.Has("note") {
			edge.Note = a.Str("note")
			changed = append(changed, "note")
		}
		if len(changed) == 0 {
			return fail("no update flags provided")
		}
		edge.UpdatedBy = ctx.Actor
		edge.UpdatedAt = model.Now()
		var staleNodes []string
		if oldKind == model.EdgeMocked && edge.Kind == model.EdgeHard {
			if target := dag.Node(edge.To); target != nil {
				reason := fmt.Sprintf("入边 %s 刚从 mocked 切回 hard，需拆 Mock 并重核 %s 的对接契约", edge.ID, edge.From)
				target.SetStale(reason)
				target.UpdatedAt = model.Now()
				staleNodes = append(staleNodes, target.ID)
			}
		}
		if err := ctx.SaveDAG(dag, "edge.update", edge.To, strings.Join(changed, ", ")); err != nil {
			return err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD EDGE UPDATE] %s  %s -> %s  kind=%s\n", id, edge.From, edge.To, edge.Kind))
		if len(staleNodes) > 0 {
			sb.WriteString("  [AUTO STALE] 已把下游节点标 stale: " + strings.Join(staleNodes, ", ") + "\n")
			sb.WriteString("  -> 拆完 Mock 后运行 `reqboard check --node " + edge.To + "` 与 `reqboard done " + edge.To + "`。\n")
		}
		if edge.Note != "" {
			sb.WriteString("  备注: " + edge.Note + "\n")
		}
		ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "edge": edge, "stale_nodes": staleNodes})
		return nil
	})
}

func edgeRemove(a *Args, id string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		edge := dag.Edge(id)
		if edge == nil {
			return fail("edge not found: %s", id)
		}
		dag.RemoveEdge(id)
		if err := ctx.SaveDAG(dag, "edge.remove", edge.To, fmt.Sprintf("%s -> %s", edge.From, edge.To)); err != nil {
			return err
		}
		ctx.Emit(fmt.Sprintf("[REQBOARD EDGE REMOVE] %s  %s -> %s 已删除\n", id, edge.From, edge.To),
			map[string]interface{}{"ok": true, "edge_id": id})
		return nil
	})
}

func reachable(dag *model.DAG, start, goal string) bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == goal {
			return true
		}
		for _, e := range dag.Edges {
			if e.From == cur && !seen[e.To] {
				seen[e.To] = true
				queue = append(queue, e.To)
			}
		}
	}
	return false
}
