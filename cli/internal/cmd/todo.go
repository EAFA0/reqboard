package cmd

import (
	"fmt"
	"sort"
	"strings"

	"reqboard/internal/model"
)

// runTodo manages first-class prerequisite TODO nodes (DDL / gateway config /
// feature flags). TODOs live outside the business DAG canvas but participate in
// done gating and, when tagged `block-start`, in dispatch startup gating.
func runTodo(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return &Error{
			Code: 2,
			Msg:  "usage: reqboard todo add|list ...",
			Text: "用法:\n  reqboard todo add \"<title>\" [--kind ddl|gateway|flag|other] [--blocks <n1,n2,...>] [--note \"...\"] [--tag block-start]\n  reqboard todo list [--json]\n",
		}
	}
	switch pos[0] {
	case "add":
		return todoAdd(a, pos[1:])
	case "list", "ls":
		return todoList(a)
	}
	return &Error{Code: 2, Msg: "unknown todo subcommand " + pos[0]}
}

type todoBlockRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Scope  string `json:"scope"`
	Status string `json:"status"`
	EdgeID string `json:"edge_id"`
	Kind   string `json:"kind"`
}

func todoAdd(a *Args, pos []string) *Error {
	title := strings.TrimSpace(strings.Join(pos, " "))
	if title == "" {
		title = strings.TrimSpace(a.Str("title"))
	}
	if title == "" {
		return fail("todo add requires a title: reqboard todo add \"<title>\" --kind ddl --blocks rb-a,rb-b")
	}
	kind := strings.TrimSpace(a.Str("kind"))
	if kind == "" {
		kind = model.OpOther
	}
	if !model.ValidOpKind(kind) {
		return fail("invalid --kind %q (ddl|gateway|flag|other)", kind)
	}
	note := strings.TrimSpace(a.Str("note"))
	blockArgs := a.Strs("blocks")
	var blockIDs []string
	for _, raw := range blockArgs {
		for _, part := range strings.Split(raw, ",") {
			if id := strings.TrimSpace(part); id != "" {
				blockIDs = append(blockIDs, id)
			}
		}
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
		used := dag.NodeIDSet()
		id := strings.TrimSpace(a.Str("id"))
		if id == "" {
			id = model.NewID("rb", used, title, model.Now())
		} else if used[id] {
			return fail("node id %q already exists", id)
		}
		used[id] = true

		tags := append([]string{kind}, a.Strs("tag")...)
		n := &model.Node{
			ID: id, Title: title, Description: note,
			Scope: model.ScopeTodo, Status: model.StatusPending,
			Tags:        dedupStrings(tags),
			PRDRefs:     []string{},
			SelfMocks:   []model.SelfMock{},
			ReviewNotes: []model.ReviewNote{},
			Ops:         []model.OpItem{},
			Position:    model.Position{X: 80, Y: 60},
			UpdatedAt:   model.Now(),
		}
		if ctx.Actor == "human" {
			n.HumanVersion = 1
		}

		var targets []*model.Node
		seenTargets := map[string]bool{}
		for _, ref := range blockIDs {
			target := dag.Node(ref)
			if target == nil {
				for _, x := range dag.Nodes {
					if x.Title == ref {
						target = x
						break
					}
				}
			}
			if target == nil {
				return fail("--blocks references unknown node %q", ref)
			}
			if target.Scope != model.ScopeMine {
				return fail("--blocks can only link our own nodes (scope=mine); %s is %s", target.ID, target.Scope)
			}
			if seenTargets[target.ID] {
				continue
			}
			seenTargets[target.ID] = true
			targets = append(targets, target)
		}

		dag.AddNode(n)
		edgeIDs := dag.EdgeIDSet()
		var edges []*model.Edge
		for _, target := range targets {
			edgeID := model.NewID("ed", edgeIDs, id, target.ID, model.EdgeHard)
			edgeIDs[edgeID] = true
			edge := &model.Edge{
				ID: edgeID, From: id, To: target.ID, Kind: model.EdgeHard,
				UpdatedBy: ctx.Actor, UpdatedAt: model.Now(),
			}
			dag.AddEdge(edge)
			edges = append(edges, edge)
		}
		if err := ctx.SaveDAG(dag, "todo.add", id, fmt.Sprintf("%s %s blocks=%d", kind, title, len(edges))); err != nil {
			return err
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD TODO ADD] %s  %s\n", id, title))
		sb.WriteString(fmt.Sprintf("  kind=%s  tags=%s\n", kind, strings.Join(n.Tags, ",")))
		if note != "" {
			sb.WriteString("  note: " + note + "\n")
		}
		if len(edges) == 0 {
			sb.WriteString("  独立需求级 TODO：未关联下游节点。\n")
		} else {
			sb.WriteString("  已创建 hard 阻塞边（完成前下游不能 done）：\n")
			for i, edge := range edges {
				sb.WriteString(fmt.Sprintf("    - %s -> %s (%s)\n", edge.ID, edge.To, targets[i].Title))
			}
		}
		sb.WriteString("  下一步: `reqboard todo list` 查看；完成后 `reqboard done " + id + "`。\n")
		ctx.Emit(sb.String(), map[string]interface{}{
			"ok": true, "node": n, "edges": edges, "blocks": targets,
		})
		return nil
	})
}

func todoList(a *Args) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	dag, derr := ctx.Store.LoadDAG()
	if derr != nil {
		return fail("%v", derr)
	}
	var todos []*model.Node
	for _, n := range dag.Nodes {
		if n.Scope == model.ScopeTodo {
			todos = append(todos, n)
		}
	}
	sort.SliceStable(todos, func(i, j int) bool { return todos[i].ID < todos[j].ID })

	done := 0
	items := make([]map[string]interface{}, 0, len(todos))
	var sb strings.Builder
	for _, n := range todos {
		if n.Status == model.StatusDone {
			done++
		}
		var blocks []todoBlockRef
		for _, edge := range dag.OutEdges(n.ID) {
			if !model.BlockingEdge(edge.Kind) {
				continue
			}
			ref := todoBlockRef{ID: edge.To, EdgeID: edge.ID, Kind: edge.Kind}
			if target := dag.Node(edge.To); target != nil {
				ref.Title, ref.Scope, ref.Status = target.Title, target.Scope, target.Status
			}
			blocks = append(blocks, ref)
		}
		items = append(items, map[string]interface{}{
			"id": n.ID, "title": n.Title, "kind": n.TodoKind(), "tags": n.Tags,
			"status": n.Status, "done": n.Status == model.StatusDone, "note": n.Description,
			"blocks": blocks,
		})
	}
	sb.WriteString(fmt.Sprintf("[REQBOARD TODO LIST] %d/%d done\n", done, len(todos)))
	if len(todos) == 0 {
		sb.WriteString("  （暂无 TODO；用 `reqboard todo add \"<title>\" --kind ddl --blocks <id,...>` 新建）\n")
	}
	for _, n := range todos {
		mark := "[ ]"
		if n.Status == model.StatusDone {
			mark = "[x]"
		}
		sb.WriteString(fmt.Sprintf("  %s %s  %s  %s\n", mark, n.ID, strings.ToUpper(n.TodoKind()), n.Title))
		if n.Description != "" {
			sb.WriteString("      note: " + n.Description + "\n")
		}
		var downstream []string
		for _, edge := range dag.OutEdges(n.ID) {
			if !model.BlockingEdge(edge.Kind) {
				continue
			}
			target := dag.Node(edge.To)
			if target == nil {
				downstream = append(downstream, edge.To+" (missing)")
				continue
			}
			downstream = append(downstream, fmt.Sprintf("%s (%s, %s)", target.ID, target.Title, target.Status))
		}
		if len(downstream) == 0 {
			sb.WriteString("      blocks: （独立需求级 TODO，无下游）\n")
		} else {
			sb.WriteString("      blocks: " + strings.Join(downstream, ", ") + "\n")
		}
	}
	ctx.Emit(sb.String(), map[string]interface{}{
		"ok": true, "total": len(todos), "done": done, "todos": items,
	})
	return nil
}
