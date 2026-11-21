package sched

import (
	"strings"
	"testing"

	"reqboard/internal/audit"
	"reqboard/internal/model"
	"reqboard/internal/testutil"
)

func TestPriorityAndOrdering(t *testing.T) {
	d := model.NewDAG("req")
	testutil.AddNode(d, "rb-a", "A", model.ScopeMine, model.StatusPending)
	testutil.AddNode(d, "rb-b", "B", model.ScopeMine, model.StatusPending)
	testutil.AddNode(d, "rb-c", "C", model.ScopeMine, model.StatusPending)
	testutil.AddNode(d, "rb-d", "D", model.ScopeMine, model.StatusChangesRequested)
	testutil.AddNode(d, "rb-e", "E", model.ScopeMine, model.StatusPending)
	d.Node("rb-e").Stale = true
	testutil.AddNode(d, "rb-f", "F", model.ScopeMine, model.StatusDeprecated)
	testutil.AddEdge(d, "ed-1", "rb-a", "rb-b", model.EdgeHard, "")
	testutil.AddEdge(d, "ed-2", "rb-a", "rb-c", model.EdgeHard, "")

	rep := &audit.Report{ToDelete: []audit.Entry{{Repo: "api", File: "x.go", Symbol: "Old", NodeIDs: []string{"rb-f"}}}}
	p := Compute(d, model.NewBindings(), rep, Options{MaxParallel: 10})
	if p.Status != "READY" {
		t.Fatalf("status = %s", p.Status)
	}
	var kinds []string
	for _, it := range p.Items {
		kinds = append(kinds, it.Kind)
	}
	want := []string{"cleanup_debris", "fix_review", "refresh_stale", "implement", "implement", "implement"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	// Left-first: rb-a (indegree 0) must be the first implement item.
	if p.Items[3].NodeID != "rb-a" {
		t.Fatalf("first implement = %s, want rb-a", p.Items[3].NodeID)
	}
}

func TestLeaseMutex(t *testing.T) {
	d := model.NewDAG("req")
	testutil.AddNode(d, "rb-a", "A", model.ScopeMine, model.StatusPending)
	testutil.AddNode(d, "rb-b", "B", model.ScopeMine, model.StatusPending)
	b := model.NewBindings()
	for i, id := range []string{"rb-a", "rb-b"} {
		b.Bindings = append(b.Bindings, model.Binding{
			ID: "bd-" + id, NodeID: id, Repo: "api", File: "svc/x.go", Symbol: "X" + string(rune('0'+i)),
			Granularity: "symbol", Role: "impl", Why: "x", BoundAt: model.Now(),
		})
	}
	p := Compute(d, b, &audit.Report{}, Options{MaxParallel: 4})
	if len(p.Items) != 1 || len(p.Deferred) != 1 {
		t.Fatalf("items=%+v deferred=%+v", p.Items, p.Deferred)
	}
	if p.Deferred[0].DeferReason == "" {
		t.Fatal("defer reason missing")
	}

	// An active lease from another node blocks both.
	b.Leases = []model.Lease{{NodeID: "rb-other", SessionID: "s-other", Patterns: []string{"api:svc/**"}}}
	p = Compute(d, b, &audit.Report{}, Options{MaxParallel: 4})
	if len(p.Items) != 0 || len(p.Deferred) != 2 {
		t.Fatalf("active lease should defer all: items=%+v deferred=%+v", p.Items, p.Deferred)
	}
}

func TestLeasePrefixConflict(t *testing.T) {
	d := model.NewDAG("req")
	testutil.AddNode(d, "rb-a", "A", model.ScopeMine, model.StatusPending)
	testutil.AddNode(d, "rb-b", "B", model.ScopeMine, model.StatusPending)
	testutil.AddNode(d, "rb-owner", "Owner", model.ScopeMine, model.StatusInProgress)
	b := model.NewBindings()
	b.Bindings = []model.Binding{{ID: "bd-a", NodeID: "rb-a", Repo: "api", File: "svc/sub/x.go", Symbol: "X", Granularity: "symbol", Role: "impl", Why: "x", BoundAt: model.Now()}}
	b.Leases = []model.Lease{{NodeID: "rb-owner", SessionID: "s-owner", Patterns: []string{"api:svc/**"}}}
	p := Compute(d, b, &audit.Report{}, Options{MaxParallel: 4})
	for _, it := range p.Items {
		if it.NodeID == "rb-a" {
			t.Fatalf("nested package under broad lease should be deferred: %+v", p.Items)
		}
	}
	found := false
	for _, it := range p.Deferred {
		if it.NodeID == "rb-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rb-a not deferred: %+v", p.Deferred)
	}
}

func TestCircuitBreaker(t *testing.T) {
	d := model.NewDAG("req")
	n := testutil.AddNode(d, "rb-a", "A", model.ScopeMine, model.StatusPending)
	n.Attempts = 2
	n.LastError = "permission denied"
	p := Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 4})
	if p.Status != "QUIESCENT" {
		t.Fatalf("status = %s", p.Status)
	}
	if len(p.Blocked.CircuitBroken) != 1 || p.Blocked.CircuitBroken[0].NodeID != "rb-a" {
		t.Fatalf("circuit = %+v", p.Blocked.CircuitBroken)
	}
}

func TestQuiescentBlockers(t *testing.T) {
	d := model.NewDAG("req")
	testutil.AddNode(d, "rb-prop", "待批", model.ScopeMine, model.StatusProposed)
	testutil.AddNode(d, "rb-ext", "外部", model.ScopeExternal, model.StatusPending)
	i := testutil.AddNode(d, "rb-impl", "实现中", model.ScopeMine, model.StatusImplemented)
	i.ReviewNotes = []model.ReviewNote{{ID: "rv-1", Status: "open", Comment: "改"}}
	testutil.AddEdge(d, "ed-1", "rb-ext", "rb-impl", model.EdgeMocked, "AuthService 未确认")
	r := testutil.AddNode(d, "rb-rev", "待完结", model.ScopeMine, model.StatusReviewed)
	r.ReviewNotes = []model.ReviewNote{{ID: "rv-2", Status: "open", Comment: "x"}}
	h := testutil.AddNode(d, "rb-hold", "暂停", model.ScopeMine, model.StatusPending)
	h.Hold = true
	cb := testutil.AddNode(d, "rb-cb", "熔断", model.ScopeMine, model.StatusPending)
	cb.Attempts = 2
	cb.LastError = "env broken"
	testutil.AddNode(d, "rb-red", "本期不做", model.ScopeOutOfScope, model.StatusPending)

	p := Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 4})
	if p.Status != "QUIESCENT" {
		t.Fatalf("status = %s, items=%+v active=%+v", p.Status, p.Items, p.Active)
	}
	if len(p.Blocked.WaitingApproval) != 1 {
		t.Fatalf("approval = %+v", p.Blocked.WaitingApproval)
	}
	if len(p.Blocked.WaitingExternal) == 0 {
		t.Fatalf("external = %+v", p.Blocked.WaitingExternal)
	}
	if len(p.Blocked.WaitingReview) == 0 {
		t.Fatalf("review = %+v", p.Blocked.WaitingReview)
	}
	if len(p.Blocked.CircuitBroken) != 1 {
		t.Fatalf("circuit = %+v", p.Blocked.CircuitBroken)
	}
	if len(p.Blocked.OutOfScope) != 1 || p.Blocked.OutOfScope[0].NodeID != "rb-red" {
		t.Fatalf("out_of_scope = %+v", p.Blocked.OutOfScope)
	}
	for _, b := range p.Blocked.WaitingReview {
		if b.NodeID == "rb-red" {
			t.Fatalf("out_of_scope node leaked into waiting_review: %+v", p.Blocked.WaitingReview)
		}
	}
}

func TestMaxParallelAndActive(t *testing.T) {
	d := model.NewDAG("req")
	for _, id := range []string{"rb-a", "rb-b", "rb-c"} {
		testutil.AddNode(d, id, id, model.ScopeMine, model.StatusPending)
	}
	p := Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 2})
	if len(p.Items) != 2 || len(p.Deferred) != 1 {
		t.Fatalf("items=%+v deferred=%+v", p.Items, p.Deferred)
	}
	if p.Items[0].NodeID != "rb-a" || p.Items[1].NodeID != "rb-b" {
		t.Fatalf("items = %+v", p.Items)
	}

	active := testutil.AddNode(d, "rb-live", "Live", model.ScopeMine, model.StatusInProgress)
	active.SessionID = "s-1"
	d.Node("rb-a").Status = model.StatusDone
	d.Node("rb-b").Status = model.StatusDone
	d.Node("rb-c").Status = model.StatusDone
	p = Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 2})
	if p.Status != "RUNNING" || len(p.Active) != 1 || p.Active[0].NodeID != "rb-live" {
		t.Fatalf("active plan = %+v", p)
	}
}

func TestAutoReviewAndAutoDone(t *testing.T) {
	d := model.NewDAG("req")
	testutil.AddNode(d, "rb-up", "Up", model.ScopeMine, model.StatusDone)
	testutil.AddNode(d, "rb-impl", "Impl", model.ScopeMine, model.StatusImplemented)
	rev := testutil.AddNode(d, "rb-rev", "Rev", model.ScopeMine, model.StatusReviewed)
	testutil.AddEdge(d, "ed-1", "rb-up", "rb-rev", model.EdgeHard, "")
	_ = rev
	p := Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 4})
	var kinds []string
	for _, it := range p.Items {
		kinds = append(kinds, it.Kind)
	}
	if len(kinds) != 2 || kinds[0] != "fast_review" || kinds[1] != "auto_done" {
		t.Fatalf("items = %+v", p.Items)
	}
}

func TestVerificationNodesNotDispatched(t *testing.T) {
	d := model.NewDAG("req")
	m := testutil.AddNode(d, "rb-e2e", "端到端验收", model.ScopeMine, model.StatusPending)
	m.Tags = []string{"milestone"}
	p := Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 4})
	if len(p.Items) != 0 {
		t.Fatalf("milestone should not be dispatched as implement: %+v", p.Items)
	}
	if len(p.Blocked.WaitingVerification) != 1 || p.Blocked.WaitingVerification[0].NodeID != "rb-e2e" {
		t.Fatalf("verification = %+v", p.Blocked.WaitingVerification)
	}
}

func TestExternalHardDependencyWaitsForDone(t *testing.T) {
	d := model.NewDAG("req")
	testutil.AddNode(d, "rb-ext", "AuthService 接口", model.ScopeExternal, model.StatusPending)
	testutil.AddNode(d, "rb-collab", "商品侧", model.ScopeCollaborator, model.StatusPending)
	testutil.AddNode(d, "rb-mine", "下单", model.ScopeMine, model.StatusPending)
	testutil.AddEdge(d, "ed-1", "rb-ext", "rb-mine", model.EdgeHard, "")
	testutil.AddEdge(d, "ed-2", "rb-collab", "rb-mine", model.EdgeHard, "")

	p := Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 4})
	for _, it := range p.Items {
		if it.NodeID == "rb-mine" {
			t.Fatalf("hard external dependency must not enter P1: %+v", p.Items)
		}
	}
	var mine *Blocker
	for i := range p.Blocked.WaitingExternal {
		if p.Blocked.WaitingExternal[i].NodeID == "rb-mine" {
			mine = &p.Blocked.WaitingExternal[i]
		}
	}
	if mine == nil {
		t.Fatalf("rb-mine not in waiting_external: %+v", p.Blocked.WaitingExternal)
	}
	for _, want := range []string{"等待外部节点 rb-ext (AuthService 接口)", "rb-collab (商品侧)", "改为 mocked"} {
		if !strings.Contains(mine.Reason, want) {
			t.Fatalf("reason missing %q: %s", want, mine.Reason)
		}
	}

	// Once the external dependencies are done the node becomes dispatchable.
	d.Node("rb-ext").Status = model.StatusDone
	d.Node("rb-collab").Status = model.StatusDone
	p = Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 4})
	if len(p.Items) != 1 || p.Items[0].NodeID != "rb-mine" || p.Items[0].Kind != "implement" {
		t.Fatalf("external done should unlock rb-mine: items=%+v", p.Items)
	}
}

func TestExternalMockedDependencyStillDispatches(t *testing.T) {
	d := model.NewDAG("req")
	testutil.AddNode(d, "rb-ext", "AuthService 接口", model.ScopeExternal, model.StatusPending)
	testutil.AddNode(d, "rb-mine", "下单", model.ScopeMine, model.StatusPending)
	testutil.AddEdge(d, "ed-1", "rb-ext", "rb-mine", model.EdgeMocked, "AuthService 先 mock")

	p := Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 4})
	found := false
	for _, it := range p.Items {
		if it.NodeID == "rb-mine" && it.Kind == "implement" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mocked external dependency should stay in P1: %+v", p.Items)
	}
	for _, b := range p.Blocked.WaitingExternal {
		if b.NodeID == "rb-mine" {
			t.Fatalf("mocked dependency leaked into waiting_external: %+v", p.Blocked.WaitingExternal)
		}
	}
}

func TestMineDependencyDoesNotBlockStart(t *testing.T) {
	d := model.NewDAG("req")
	testutil.AddNode(d, "rb-a", "A", model.ScopeMine, model.StatusPending)
	testutil.AddNode(d, "rb-b", "B", model.ScopeMine, model.StatusPending)
	testutil.AddEdge(d, "ed-1", "rb-a", "rb-b", model.EdgeHard, "")

	p := Compute(d, model.NewBindings(), &audit.Report{}, Options{MaxParallel: 4})
	if len(p.Items) != 2 {
		t.Fatalf("mine -> mine must not block start: items=%+v", p.Items)
	}
}
