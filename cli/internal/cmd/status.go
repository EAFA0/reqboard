package cmd

import (
	"fmt"
	"sort"
	"strings"

	"reqboard/internal/audit"
	"reqboard/internal/model"
	"reqboard/internal/prd"
	"reqboard/internal/sched"
)

type prdBlockView struct {
	Anchor   string   `json:"anchor"`
	Kind     string   `json:"kind"`
	Level    int      `json:"level,omitempty"`
	Section  string   `json:"section"`
	Text     string   `json:"text"`
	Coverage string   `json:"coverage"` // covered | ignored | out_of_scope | unmapped | heading
	NodeIDs  []string `json:"node_ids,omitempty"`
}

func runStatus(a *Args, rest []string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
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
	doc := loadPRDDoc(ctx)
	rep := audit.Run(cfg, ctx.Root, dag, bindings, audit.Options{All: true})
	dispatchPlan := sched.Compute(dag, bindings, rep, sched.Options{
		MaxParallel: dag.Orchestrator.MaxParallel, SessionID: dag.Orchestrator.SessionID,
	})
	events, _ := ctx.Store.Events(20)

	refs := map[string][]string{}
	outOfScope := map[string]bool{}
	for _, n := range dag.Nodes {
		for _, r := range n.PRDRefs {
			refs[r] = append(refs[r], n.ID)
			if n.Scope == model.ScopeOutOfScope {
				outOfScope[r] = true
			}
		}
	}
	ignored := map[string]bool{}
	for _, r := range dag.IgnoredPRDRefs {
		ignored[r] = true
	}
	blocks := []prdBlockView{}
	cov := map[string]interface{}{}
	total, covered, ignoredN, outN, unmappedN := 0, 0, 0, 0, 0
	var unmapped []string
	if doc != nil {
		for _, b := range doc.Blocks {
			view := prdBlockView{Anchor: b.Anchor, Kind: b.Kind, Level: b.Level, Section: b.Section, Text: b.Text}
			view.NodeIDs = append([]string{}, refs[b.Anchor]...)
			sort.Strings(view.NodeIDs)
			switch {
			case b.Kind == prd.KindHeading:
				if ignored[b.Anchor] {
					view.Coverage = "ignored"
				} else {
					view.Coverage = "heading"
				}
			case len(view.NodeIDs) > 0 && !allOutOfScope(view.NodeIDs, dag):
				view.Coverage = "covered"
			case ignored[b.Anchor]:
				view.Coverage = "ignored"
			case len(view.NodeIDs) > 0 && outOfScope[b.Anchor]:
				view.Coverage = "out_of_scope"
			default:
				view.Coverage = "unmapped"
			}
			if b.Kind != prd.KindHeading {
				total++
				switch view.Coverage {
				case "covered":
					covered++
				case "ignored":
					ignoredN++
				case "out_of_scope":
					outN++
				default:
					unmappedN++
					unmapped = append(unmapped, b.Anchor)
				}
			}
			blocks = append(blocks, view)
		}
	}
	percent := 0.0
	if total > 0 {
		percent = float64(covered+ignoredN+outN) / float64(total) * 100
	}
	cov["total"] = total
	cov["covered"] = covered
	cov["ignored"] = ignoredN
	cov["out_of_scope"] = outN
	cov["unmapped"] = unmappedN
	cov["unmapped_anchors"] = unmapped
	cov["percent"] = percent

	statusCount := map[string]int{}
	stale, holds := 0, 0
	for _, n := range dag.Nodes {
		statusCount[n.Status]++
		if n.Stale {
			stale++
		}
		if n.Hold {
			holds++
		}
	}

	orchState := dag.Orchestrator.State
	if dispatchPlan.Status == "QUIESCENT" {
		orchState = "quiescent"
	} else if dispatchPlan.Status == "READY" || len(dispatchPlan.Active) > 0 {
		orchState = "running"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[REQBOARD STATUS] %s  @ %s\n", dag.RequirementName, ctx.Root))
	sb.WriteString(fmt.Sprintf("  Orchestrator: %s (session=%s, max_parallel=%d)\n", orchState, dag.Orchestrator.SessionID, dag.Orchestrator.MaxParallel))
	sb.WriteString(fmt.Sprintf("  Nodes: %d total | done %d | in_progress %d | pending %d | implemented %d | reviewed %d | proposed %d | deprecated %d\n",
		len(dag.Nodes), statusCount[model.StatusDone], statusCount[model.StatusInProgress], statusCount[model.StatusPending],
		statusCount[model.StatusImplemented], statusCount[model.StatusReviewed], statusCount[model.StatusProposed], statusCount[model.StatusDeprecated]))
	sb.WriteString(fmt.Sprintf("  stale=%d  hold=%d  edges=%d\n", stale, holds, len(dag.Edges)))
	sb.WriteString(fmt.Sprintf("  PRD coverage: %.1f%% (%d/%d 段，忽略 %d，红线 %d)，Unmapped %d\n", percent, covered+ignoredN+outN, total, ignoredN, outN, unmappedN))
	sb.WriteString(fmt.Sprintf("  Diff: attributed=%d orphan=%d to_delete=%d leased=%d broken=%d (changed_files=%d)\n",
		len(rep.Attributed), len(rep.Orphan), len(rep.ToDelete), len(rep.Leased), len(rep.Broken), rep.ChangedFiles))
	sb.WriteString(fmt.Sprintf("  Diff filter: exclude_tests=%t include_globs=%d ignore_globs=%d ignored_files=%d\n",
		rep.Filter.ExcludeTests, len(rep.Filter.IncludeGlobs), len(rep.Filter.IgnoreGlobs), len(rep.Ignored)))
	if len(rep.Orphan) > 0 {
		sb.WriteString("  [ORPHAN] ")
		var parts []string
		for i, x := range rep.Orphan {
			if i >= 3 {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, fmt.Sprintf("%s:%s::%s", x.Repo, x.File, x.Symbol))
		}
		sb.WriteString(strings.Join(parts, ", ") + "\n")
	}
	if len(rep.ToDelete) > 0 {
		sb.WriteString("  [TO-DELETE] ")
		var parts []string
		for i, x := range rep.ToDelete {
			if i >= 3 {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, fmt.Sprintf("%s:%s::%s", x.Repo, x.File, x.Symbol))
		}
		sb.WriteString(strings.Join(parts, ", ") + "\n")
	}
	if dispatchPlan.Status == "QUIESCENT" {
		sb.WriteString(fmt.Sprintf("  Dispatch: QUIESCENT (waiting_approval=%d, waiting_review=%d, waiting_external=%d, waiting_verification=%d, circuit_broken=%d, out_of_scope=%d)\n",
			len(dispatchPlan.Blocked.WaitingApproval), len(dispatchPlan.Blocked.WaitingReview),
			len(dispatchPlan.Blocked.WaitingExternal), len(dispatchPlan.Blocked.WaitingVerification),
			len(dispatchPlan.Blocked.CircuitBroken), len(dispatchPlan.Blocked.OutOfScope)))
	} else {
		sb.WriteString(fmt.Sprintf("  Dispatch: %s (items=%d, deferred=%d, active=%d, waiting_external=%d)\n",
			dispatchPlan.Status, len(dispatchPlan.Items), len(dispatchPlan.Deferred),
			len(dispatchPlan.Active), len(dispatchPlan.Blocked.WaitingExternal)))
	}
	if len(events) > 0 {
		sb.WriteString("  最近事件:\n")
		start := len(events) - 5
		if start < 0 {
			start = 0
		}
		for _, ev := range events[start:] {
			sb.WriteString(fmt.Sprintf("    - %s [%s] %s %s\n", ev.TS, ev.Actor, ev.Action, ev.Detail))
		}
	}

	data := map[string]interface{}{
		"ok":               true,
		"root":             ctx.Root,
		"requirement_name": dag.RequirementName,
		"generated_at":     model.Now(),
		"config":           cfg,
		"dag":              dag,
		"prd": map[string]interface{}{
			"blocks": blocks, "coverage": cov, "ignored": dag.IgnoredPRDRefs,
		},
		"bindings": bindings.Bindings,
		"leases":   bindings.Leases,
		"check": map[string]interface{}{
			"changed_files": rep.ChangedFiles,
			"violations":    rep.Violations,
			"buckets": map[string]interface{}{
				"attributed":      rep.Attributed,
				"orphan":          rep.Orphan,
				"to_delete":       rep.ToDelete,
				"leased":          rep.Leased,
				"broken_bindings": rep.Broken,
			},
			"node_stats":    rep.NodeStats,
			"repos":         rep.Repos,
			"ignored_files": rep.Ignored,
		},
		"filter": rep.Filter,
		"orchestrator": map[string]interface{}{
			"session_id":       dag.Orchestrator.SessionID,
			"state":            orchState,
			"max_parallel":     dag.Orchestrator.MaxParallel,
			"last_dispatch_at": dag.Orchestrator.LastDispatchAt,
		},
		"dispatch": dispatchPlan,
		"events":   events,
	}
	ctx.Emit(sb.String(), data)
	return nil
}

func allOutOfScope(ids []string, dag *model.DAG) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		n := dag.Node(id)
		if n == nil || n.Scope != model.ScopeOutOfScope {
			return false
		}
	}
	return true
}
