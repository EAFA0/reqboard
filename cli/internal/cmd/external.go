package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/model"
)

// isExternalScope reports whether the node is owned by an external party or a
// collaborator. Those nodes have no code gates: we cannot verify their code,
// so marking them done only records that the dependency is ready.
func isExternalScope(scope string) bool {
	return scope == model.ScopeExternal || scope == model.ScopeCollaborator
}

// mockedEdgeRef is the compact view of a mocked out-edge used by CLI output and
// the VS Code Prerequisite Dock.
type mockedEdgeRef struct {
	ID    string `json:"id"`
	To    string `json:"to"`
	Title string `json:"title"`
	Note  string `json:"note,omitempty"`
}

// mockedOutEdges lists the still-mocked out-edges of an external node.
func mockedOutEdges(dag *model.DAG, id string) []mockedEdgeRef {
	var out []mockedEdgeRef
	for _, e := range dag.OutEdges(id) {
		if e.Kind != model.EdgeMocked {
			continue
		}
		ref := mockedEdgeRef{ID: e.ID, To: e.To, Note: e.Note}
		if target := dag.Node(e.To); target != nil {
			ref.Title = target.Title
		}
		out = append(out, ref)
	}
	return out
}

// applyUnmockDownstream flips every mocked out-edge of an external node back to
// hard and marks the mine downstream nodes stale: the real contract is ready,
// so the stub has to be replaced and the integration re-checked.
func applyUnmockDownstream(dag *model.DAG, n *model.Node, actor string) (switched []string, stale []string) {
	for _, e := range dag.OutEdges(n.ID) {
		if e.Kind != model.EdgeMocked {
			continue
		}
		e.Kind = model.EdgeHard
		e.UpdatedBy = actor
		e.UpdatedAt = model.Now()
		switched = append(switched, e.ID)
		target := dag.Node(e.To)
		if target == nil || target.Scope != model.ScopeMine {
			continue
		}
		reason := fmt.Sprintf(
			"外部依赖 %s (%s) 已就绪，需拆 Mock 切真联调（边 %s 已从 mocked 切回 hard）",
			n.ID, n.Title, e.ID,
		)
		target.SetStale(reason)
		target.UpdatedAt = model.Now()
		stale = append(stale, target.ID)
	}
	return switched, stale
}

// writeMockedEdgeHint tells the operator which downstream contracts are still
// mocked so they can decide whether to unmock them in one shot.
func writeMockedEdgeHint(sb *strings.Builder, id string, refs []mockedEdgeRef) {
	if len(refs) == 0 {
		return
	}
	sb.WriteString(fmt.Sprintf("  [MOCKED] %d 条 mocked 出边仍保留（下游可继续带 Mock 并行）：\n", len(refs)))
	for _, r := range refs {
		line := fmt.Sprintf("    - %s -> %s (%s)", r.ID, r.To, r.Title)
		if r.Note != "" {
			line += "：" + r.Note
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("  -> 外部依赖已就绪后，可执行 `reqboard done " + id + " --unmock-downstream` 一键切回 hard 并标下游 stale。\n")
}

// writeUnmockResult reports the one-shot unmock outcome.
func writeUnmockResult(sb *strings.Builder, switched, stale []string) {
	if len(switched) == 0 {
		sb.WriteString("  [UNMOCK] 没有需要切回的 mocked 出边。\n")
		return
	}
	sb.WriteString(fmt.Sprintf("  [UNMOCK] 已把 %d 条 mocked 出边切回 hard：%s\n", len(switched), strings.Join(switched, ", ")))
	if len(stale) > 0 {
		sb.WriteString("  [AUTO STALE] 下游节点已标 stale：" + strings.Join(stale, ", ") + "\n")
		sb.WriteString("  -> 请拆 Mock 并切真联调；完成后由人类清除 stale 再 done。\n")
	}
}
