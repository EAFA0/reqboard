package gate

import (
	"strings"
	"testing"

	"reqboard/internal/audit"
	"reqboard/internal/model"
)

func baseDAG() (*model.DAG, *model.Bindings) {
	d := model.NewDAG("req")
	a := &model.Node{ID: "rb-a", Title: "上游", Scope: model.ScopeMine, Status: model.StatusDone}
	b := &model.Node{ID: "rb-b", Title: "下游", Scope: model.ScopeMine, Status: model.StatusReviewed}
	d.AddNode(a)
	d.AddNode(b)
	d.AddEdge(&model.Edge{ID: "ed-1", From: "rb-a", To: "rb-b", Kind: model.EdgeHard})
	return d, model.NewBindings()
}

func codes(vs []Violation) map[string]bool {
	m := map[string]bool{}
	for _, v := range vs {
		m[v.Code] = true
	}
	return m
}

func TestAllGatesPass(t *testing.T) {
	d, b := baseDAG()
	if vs := Evaluate(d, b, &audit.Report{}, "rb-b"); len(vs) != 0 {
		t.Fatalf("expected pass, got %+v", vs)
	}
}

func TestGate1UpstreamNotDone(t *testing.T) {
	d, b := baseDAG()
	d.Node("rb-a").Status = model.StatusInProgress
	vs := Evaluate(d, b, &audit.Report{}, "rb-b")
	if !codes(vs)["upstream_not_done"] {
		t.Fatalf("violations = %+v", vs)
	}
}

func TestGate2MockDebt(t *testing.T) {
	d, b := baseDAG()
	d.Edges[0].Kind = model.EdgeMocked
	d.Edges[0].Note = "AuthService 未确认"
	n := d.Node("rb-b")
	n.SelfMocks = []model.SelfMock{{ID: "mk-01", Desc: "先 mock 库存", Resolved: false}}
	b.Bindings = []model.Binding{{ID: "bd-1", NodeID: "rb-b", Repo: "api", File: "svc/x.go", Symbol: "X", Role: model.RoleMock}}
	vs := Evaluate(d, b, &audit.Report{}, "rb-b")
	c := codes(vs)
	for _, want := range []string{"mocked_edge", "self_mock", "mock_binding"} {
		if !c[want] {
			t.Fatalf("missing %s in %+v", want, vs)
		}
	}
}

func TestGate3StaleAndOpenNotes(t *testing.T) {
	d, b := baseDAG()
	n := d.Node("rb-b")
	n.Stale = true
	n.StaleReason = "PRD 改了"
	n.ReviewNotes = []model.ReviewNote{{ID: "rv-01", Status: "open", Comment: "改成排除语义"}}
	vs := Evaluate(d, b, &audit.Report{}, "rb-b")
	c := codes(vs)
	if !c["stale_node"] || !c["open_review_note"] {
		t.Fatalf("violations = %+v", vs)
	}
	// Resolved notes do not block.
	n.ReviewNotes[0].Status = "resolved"
	vs = Evaluate(d, b, &audit.Report{}, "rb-b")
	if codes(vs)["open_review_note"] {
		t.Fatalf("resolved note still blocks: %+v", vs)
	}
}

func TestGate4CodeCleanliness(t *testing.T) {
	d, b := baseDAG()
	b.Bindings = []model.Binding{{ID: "bd-1", NodeID: "rb-b", Repo: "api", File: "svc/x.go", Symbol: "X", Role: model.RoleImpl}}
	rep := &audit.Report{
		Scope:    "all",
		Broken:   []audit.BrokenBinding{{BindingID: "bd-9", NodeID: "rb-b", Repo: "api", File: "svc/x.go", Symbol: "Gone", Reason: "symbol not found"}},
		Orphan:   []audit.Entry{{Repo: "api", File: "svc/x.go", Symbol: "Sneaky", Added: 3}},
		ToDelete: []audit.Entry{{Repo: "api", File: "svc/x.go", Symbol: "Old", NodeIDs: []string{"rb-b"}}},
	}
	vs := Evaluate(d, b, rep, "rb-b")
	c := codes(vs)
	for _, want := range []string{"broken_binding", "orphan_diff", "to_delete"} {
		if !c[want] {
			t.Fatalf("missing %s in %+v", want, vs)
		}
	}
	if !strings.Contains(Format("rb-b", "下游", vs), "[REQBOARD BLOCKED]") {
		t.Fatal("format header missing")
	}
}

func TestGateIgnoresOtherNodesDebris(t *testing.T) {
	d, b := baseDAG()
	rep := &audit.Report{
		Scope:    "all",
		ToDelete: []audit.Entry{{Repo: "api", File: "svc/x.go", Symbol: "Old", NodeIDs: []string{"rb-other"}}},
		Broken:   []audit.BrokenBinding{{BindingID: "bd-9", NodeID: "rb-other"}},
	}
	if vs := Evaluate(d, b, rep, "rb-b"); len(vs) != 0 {
		t.Fatalf("other node's debris must not block: %+v", vs)
	}
}
