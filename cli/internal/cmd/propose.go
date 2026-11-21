package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/model"
)

func runPropose(a *Args, rest []string) *Error {
	from := a.Str("from")
	title := strings.TrimSpace(a.Str("title"))
	reason := strings.TrimSpace(a.Str("reason"))
	refs := a.Strs("prd-ref")
	if from == "" || title == "" || reason == "" || len(refs) == 0 {
		return fail("--from, --prd-ref, --title and --reason are all required")
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
		parent, ne := findNodeOrFail(dag, from)
		if ne != nil {
			return ne
		}
		if err := validateRefs(ctx, dag, refs, a.Bool("allow-unknown-refs")); err != nil {
			return err
		}
		used := dag.NodeIDSet()
		id := model.NewID("rb", used, "propose", from, title, model.Now())
		pos := model.Position{X: parent.Position.X + 300, Y: parent.Position.Y + 120}
		n := &model.Node{
			ID: id, Title: title, Description: reason,
			Scope: model.ScopeMine, Status: model.StatusProposed,
			PRDRefs: dedupStrings(refs), Tags: []string{"discovered"},
			SelfMocks: []model.SelfMock{}, ReviewNotes: []model.ReviewNote{},
			Position: pos, UpdatedAt: model.Now(),
		}
		dag.AddNode(n)
		edgeIDs := dag.EdgeIDSet()
		edgeID := model.NewID("ed", edgeIDs, from, id, "discovered", model.Now())
		dag.AddEdge(&model.Edge{
			ID: edgeID, From: from, To: id, Kind: model.EdgeDiscovered,
			Note: reason, UpdatedBy: ctx.Actor, UpdatedAt: model.Now(),
		})
		if err := ctx.SaveDAG(dag, "propose", id, reason); err != nil {
			return err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD PROPOSE] 已创建 proposed 节点 %s：%s\n", id, title))
		sb.WriteString(fmt.Sprintf("  discovered 边: %s (%s -> %s)，非阻断溯源\n", edgeID, from, id))
		sb.WriteString("  原因: " + reason + "\n")
		sb.WriteString("\n  [HARD RULE] 节点已提交待 owner 审批，请勿在审批前修改该部分代码。\n")
		sb.WriteString("  -> owner 在看板点 [批准] 或执行 `reqboard node approve " + id + "`；驳回则 `reqboard node reject " + id + "`。\n")
		ctx.Emit(sb.String(), map[string]interface{}{"ok": true, "node": n, "edge_id": edgeID, "awaiting_approval": true})
		return nil
	})
}
