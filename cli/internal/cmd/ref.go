package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/model"
)

func runRef(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return &Error{Code: 2, Msg: "usage: reqboard ref add|remove ...", Text: refUsage()}
	}
	switch pos[0] {
	case "add":
		if len(pos) < 2 {
			return fail("usage: reqboard ref add <node_id> --title \"...\" [--kind url|meeting|decision|chat] [--url \"https://...\"] [--quote \"...\"]")
		}
		return refAdd(a, pos[1])
	case "remove", "rm":
		if len(pos) < 3 {
			return fail("usage: reqboard ref remove <node_id> <ref_id>")
		}
		return refRemove(a, pos[1], pos[2])
	}
	return &Error{Code: 2, Msg: "unknown ref subcommand " + pos[0], Text: refUsage()}
}

func refUsage() string {
	return `用法:
  reqboard ref add <node_id> --title "..." [--kind url|meeting|decision|chat] [--url "https://..."] [--quote "..."]
  reqboard ref remove <node_id> <ref_id>
`
}

func refAdd(a *Args, nodeID string) *Error {
	title := strings.TrimSpace(a.Str("title"))
	if title == "" {
		return fail("--title is required")
	}
	kind := a.Str("kind")
	if kind == "" {
		kind = model.RefDecision
	}
	if !model.ValidRefKind(kind) {
		return fail("invalid --kind %q (url|meeting|decision|chat)", kind)
	}
	url := strings.TrimSpace(a.Str("url"))
	if url != "" && !isHTTPURL(url) {
		return fail("invalid --url %q (only http(s) links are accepted)", url)
	}
	quote := strings.TrimSpace(a.Str("quote"))
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
		for _, r := range n.Refs {
			used[r.ID] = true
		}
		id := model.NewSequentialID("rf", used)
		ref := model.ExternalRef{ID: id, Kind: kind, Title: title, URL: url, Quote: quote, AddedAt: model.Now()}
		n.Refs = append(n.Refs, ref)
		n.UpdatedAt = model.Now()
		if err := ctx.SaveDAG(dag, "ref.add", nodeID, fmt.Sprintf("%s [%s] %s", id, kind, title)); err != nil {
			return err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD REF ADD] %s -> %s  %s\n", nodeID, id, title))
		sb.WriteString(fmt.Sprintf("  kind=%s\n", kind))
		if url != "" {
			sb.WriteString("  url: " + url + "\n")
		}
		if quote != "" {
			sb.WriteString("  quote: " + quote + "\n")
		}
		sb.WriteString("  `reqboard claim " + nodeID + "` 会自动注入该依据。\n")
		ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "node_id": nodeID, "ref": ref})
		return nil
	})
}

func refRemove(a *Args, nodeID, refID string) *Error {
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
		idx := -1
		for i := range n.Refs {
			if n.Refs[i].ID == refID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fail("ref %q not found on node %s", refID, nodeID)
		}
		removed := n.Refs[idx]
		n.Refs = append(n.Refs[:idx], n.Refs[idx+1:]...)
		n.UpdatedAt = model.Now()
		if err := ctx.SaveDAG(dag, "ref.remove", nodeID, fmt.Sprintf("%s %s", refID, removed.Title)); err != nil {
			return err
		}
		ctx.Emit(fmt.Sprintf("[REQBOARD REF REMOVE] %s - %s  %s\n", nodeID, refID, removed.Title),
			map[string]interface{}{"ok": true, "node_id": nodeID, "removed": removed})
		return nil
	})
}
