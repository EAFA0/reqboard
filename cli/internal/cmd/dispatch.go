package cmd

import (
	"fmt"
	"strings"

	"reqboard/internal/audit"
	"reqboard/internal/model"
	"reqboard/internal/sched"
)

func runDispatch(a *Args, rest []string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	maxParallel := a.Int("max-parallel", 0)
	session := a.Str("session")
	return ctx.WithLock(func() *Error {
		cfg, cerr := ctx.Store.LoadConfig()
		if cerr != nil {
			return fail("%v", cerr)
		}
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		bindings, berr := ctx.Store.LoadBindings()
		if berr != nil {
			return fail("%v", berr)
		}
		if session == "" {
			session = dag.Orchestrator.SessionID
		}
		if session == "" {
			session = model.NewID("goal", nil, model.Now())
		}
		rep := audit.Run(cfg, ctx.Root, dag, bindings, audit.Options{All: true})
		plan := sched.Compute(dag, bindings, rep, sched.Options{MaxParallel: maxParallel, SessionID: session})
		dag.Orchestrator.SessionID = session
		dag.Orchestrator.MaxParallel = plan.MaxParallel
		dag.Orchestrator.LastDispatchAt = model.Now()
		if plan.Status == "QUIESCENT" {
			dag.Orchestrator.State = "quiescent"
		} else {
			dag.Orchestrator.State = "running"
		}
		dag.Touch()
		if err := ctx.Store.SaveDAGNoUndo(dag); err != nil {
			return fail("save dag.json: %v", err)
		}
		_ = ctx.Store.AppendEvent(ctx.event("dispatch", "", fmt.Sprintf("status=%s items=%d deferred=%d", plan.Status, len(plan.Items), len(plan.Deferred))))

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD DISPATCH] status=%s  max_parallel=%d  session=%s\n", plan.Status, plan.MaxParallel, plan.SessionID))
		if len(plan.Items) == 0 {
			sb.WriteString("  本波无可派发项。\n")
		}
		for _, it := range plan.Items {
			sb.WriteString(fmt.Sprintf("\n  P%d [%s] %s  %s\n", it.Priority, it.Kind, it.NodeID, it.Title))
			sb.WriteString("     reason: " + it.Reason + "\n")
			sb.WriteString("     run:    " + it.Command + "\n")
			if it.Instruction != "" {
				sb.WriteString("     note:   " + it.Instruction + "\n")
			}
			if len(it.LeaseKeys) > 0 {
				sb.WriteString("     lease:  " + strings.Join(it.LeaseKeys, ", ") + "\n")
			}
		}
		if len(plan.Deferred) > 0 {
			sb.WriteString("\n  延后到下一波:\n")
			for _, it := range plan.Deferred {
				sb.WriteString(fmt.Sprintf("    - P%d [%s] %s: %s\n", it.Priority, it.Kind, it.NodeID, it.DeferReason))
			}
		}
		if len(plan.Active) > 0 {
			sb.WriteString("\n  进行中 Worker:\n")
			for _, w := range plan.Active {
				sb.WriteString(fmt.Sprintf("    - %s (%s, session=%s)\n", w.NodeID, w.Status, w.SessionID))
			}
		}
		if plan.Status == "QUIESCENT" {
			sb.WriteString("\n  [QUIESCENT] 无合法可推进项，Orchestrator 应停机汇报，不要空转烧 token。\n")
			writeBlocked(&sb, "waiting_approval（待审批）", plan.Blocked.WaitingApproval)
			writeBlocked(&sb, "waiting_review（待人工 Review / 修复）", plan.Blocked.WaitingReview)
			writeBlocked(&sb, "waiting_external（外部/协作者/Mock 阻塞）", plan.Blocked.WaitingExternal)
			writeBlocked(&sb, "waiting_todo（DDL / 网关配置 / Feature Flag 前置）", plan.Blocked.WaitingTodo)
			writeBlocked(&sb, "waiting_verification（端到端/验收/范围节点）", plan.Blocked.WaitingVerification)
			writeBlocked(&sb, "circuit_broken（熔断需人工拍板）", plan.Blocked.CircuitBroken)
			writeBlocked(&sb, "out_of_scope（PRD 红线，本期不做）", plan.Blocked.OutOfScope)
		}
		data := map[string]interface{}{
			"ok": true, "status": plan.Status, "max_parallel": plan.MaxParallel,
			"session_id": plan.SessionID, "items": plan.Items, "deferred": plan.Deferred,
			"active": plan.Active, "blocked": plan.Blocked,
			"changed_files": rep.ChangedFiles,
		}
		ctx.Emit(sb.String(), data)
		return nil
	})
}

func writeBlocked(sb *strings.Builder, title string, bs []sched.Blocker) {
	if len(bs) == 0 {
		return
	}
	sb.WriteString("\n  " + title + ":\n")
	for _, b := range bs {
		sb.WriteString(fmt.Sprintf("    - %s %s: %s\n", b.NodeID, b.Title, b.Reason))
	}
}
