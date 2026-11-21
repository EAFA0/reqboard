// Package gate implements the four hard gates for `reqboard done`.
package gate

import (
	"fmt"
	"strings"

	"reqboard/internal/audit"
	"reqboard/internal/model"
)

// Violation is one failed gate item with a concrete correction command.
type Violation struct {
	Gate       int    `json:"gate"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Correction string `json:"correction,omitempty"`
}

// Evaluate checks the four done gates for a node.
func Evaluate(dag *model.DAG, bindings *model.Bindings, rep *audit.Report, nodeID string) []Violation {
	var out []Violation
	n := dag.Node(nodeID)
	if n == nil {
		return []Violation{{Gate: 0, Code: "node_missing", Message: "node not found: " + nodeID}}
	}

	// Gate 1: every blocking upstream must be done.
	for _, e := range dag.InEdges(nodeID) {
		if !model.BlockingEdge(e.Kind) {
			continue
		}
		from := dag.Node(e.From)
		if from == nil {
			out = append(out, Violation{
				Gate: 1, Code: "upstream_missing",
				Message:    fmt.Sprintf("入边 %s 指向不存在的上游节点 %s", e.ID, e.From),
				Correction: "修复 dag.json 中的悬空边或 `reqboard edge remove " + e.ID + "`",
			})
			continue
		}
		if from.Status != model.StatusDone {
			msg := fmt.Sprintf("入边 %s (%s -> %s) 的上游节点 %s 仍为 %s", e.ID, from.ID, nodeID, from.ID, from.Status)
			correction := fmt.Sprintf("先推进上游节点：`reqboard claim %s`，完成后 `reqboard done %s`", from.ID, from.ID)
			if from.Scope == model.ScopeTodo {
				msg = fmt.Sprintf("前置 TODO 未完成：入边 %s (%s -> %s) 的 TODO 节点 %s (%s) 仍为 %s", e.ID, from.ID, nodeID, from.ID, from.Title, from.Status)
				correction = fmt.Sprintf("先完成前置 TODO：`reqboard done %s`（`reqboard todo list` 可查看全部 TODO 与阻塞关系）", from.ID)
			}
			out = append(out, Violation{
				Gate: 1, Code: "upstream_not_done",
				Message:    msg,
				Correction: correction,
			})
		}
	}

	// Gate 2: zero mock debt.
	for _, e := range dag.InEdges(nodeID) {
		if e.Kind == model.EdgeMocked {
			msg := fmt.Sprintf("入边 %s (%s -> %s) 仍处于 mocked 状态", e.ID, e.From, nodeID)
			if e.Note != "" {
				msg += "：" + e.Note
			}
			out = append(out, Violation{
				Gate: 2, Code: "mocked_edge", Message: msg,
				Correction: fmt.Sprintf("拆 Mock 后 `reqboard edge update %s --kind hard`（会自动把 %s 标 stale 复检）", e.ID, nodeID),
			})
		}
		if e.Kind == model.EdgeStale {
			out = append(out, Violation{
				Gate: 2, Code: "stale_edge",
				Message:    fmt.Sprintf("入边 %s (%s -> %s) 处于 stale 状态，契约待重核", e.ID, e.From, nodeID),
				Correction: fmt.Sprintf("重核契约后 `reqboard edge update %s --kind hard`", e.ID),
			})
		}
	}
	for _, m := range n.PendingMocks() {
		out = append(out, Violation{
			Gate: 2, Code: "self_mock",
			Message:    fmt.Sprintf("节点自身 Mock 债未销账：%s (%s)", m.ID, m.Desc),
			Correction: fmt.Sprintf("`reqboard mock resolve %s --mock %s`", nodeID, m.ID),
		})
	}
	for _, b := range bindings.BindingsForNode(nodeID) {
		if b.Role == model.RoleMock {
			out = append(out, Violation{
				Gate: 2, Code: "mock_binding",
				Message:    fmt.Sprintf("存在未清理的 mock 绑定 %s: %s:%s::%s", b.ID, b.Repo, b.File, b.Symbol),
				Correction: fmt.Sprintf("删除 Mock 代码后 `reqboard unbind %s`，或改绑为 impl：`reqboard bind %s --repo %s --file %s --symbol %s --role impl --why \"...\"`", b.ID, nodeID, b.Repo, b.File, b.Symbol),
			})
		}
	}

	// Gate 3: not stale, no open review notes.
	if n.Stale {
		reason := n.StaleReason
		if reason == "" {
			reason = "上游或 PRD 变更"
		}
		out = append(out, Violation{
			Gate: 3, Code: "stale_node",
			Message:    fmt.Sprintf("节点处于 stale 状态：%s", reason),
			Correction: fmt.Sprintf("重新核对 PRD 与实现后由人类清除：`reqboard node update %s --stale=false --actor human`", nodeID),
		})
	}
	for _, note := range n.OpenNotes() {
		correction := fmt.Sprintf("修复后由 Review 人执行 `reqboard node review %s --pass`（自动关闭批注）", nodeID)
		if note.Author == "human" {
			correction = fmt.Sprintf("修复后保留为 implemented，由人类在看板点「Reviewed 通过」或执行 `reqboard node review %s --pass --actor human`（禁止 Agent 自审关闭人类批注）", nodeID)
		}
		out = append(out, Violation{
			Gate: 3, Code: "open_review_note",
			Message:    fmt.Sprintf("存在未闭环 Review 批注 %s：%s", note.ID, note.Comment),
			Correction: correction,
		})
	}

	// Gate 4: code cleanliness for this node.
	if rep != nil {
		for _, b := range rep.Broken {
			if b.NodeID != nodeID {
				continue
			}
			msg := fmt.Sprintf("Broken Binding %s: %s:%s::%s（%s）", b.BindingID, b.Repo, b.File, b.Symbol, b.Reason)
			out = append(out, Violation{Gate: 4, Code: "broken_binding", Message: msg, Correction: b.Correction})
		}
		for _, e := range rep.Orphan {
			if rep.Scope != nodeID && !audit.InNodeScope(e, n, bindings) {
				continue
			}
			out = append(out, Violation{
				Gate: 4, Code: "orphan_diff",
				Message:    fmt.Sprintf("发现未归因改动（疑似越界/过度设计）：%s:%s::%s (+%d/-%d)", e.Repo, e.File, e.Symbol, e.Added, e.Deleted),
				Correction: e.Correction,
			})
		}
		for _, e := range rep.ToDelete {
			if !contains(e.NodeIDs, nodeID) {
				continue
			}
			out = append(out, Violation{
				Gate: 4, Code: "to_delete",
				Message:    fmt.Sprintf("存在待清理残骸：%s:%s::%s (+%d/-%d)", e.Repo, e.File, e.Symbol, e.Added, e.Deleted),
				Correction: e.Correction,
			})
		}
	}
	return out
}

// AllPassed reports whether there are no violations.
func AllPassed(vs []Violation) bool { return len(vs) == 0 }

// Format renders violations in the [REQBOARD BLOCKED] terminal style.
func Format(nodeID, title string, vs []Violation) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[REQBOARD BLOCKED] 无法将 %s (%s) 标记为 done：\n", nodeID, title))
	for i, v := range vs {
		sb.WriteString(fmt.Sprintf("  %d. [G%d %s] %s\n", i+1, v.Gate, v.Code, v.Message))
		if v.Correction != "" {
			sb.WriteString("     -> " + v.Correction + "\n")
		}
	}
	return sb.String()
}

func contains(in []string, s string) bool {
	for _, x := range in {
		if x == s {
			return true
		}
	}
	return false
}
