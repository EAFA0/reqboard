package plan

import (
	"fmt"
	"strings"
	"testing"

	"reqboard/internal/model"
	"reqboard/internal/prd"
)

func doc(t *testing.T, md string) *prd.Document {
	t.Helper()
	d, err := prd.Parse([]byte(md))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestImportValidatesCycle(t *testing.T) {
	d := model.NewDAG("x")
	f := &File{Nodes: []Node{
		{ID: "a", Title: "A", PRDRefs: []string{"p:1-1"}, DependsOn: []string{"b"}},
		{ID: "b", Title: "B", PRDRefs: []string{"p:1-2"}, DependsOn: []string{"a"}},
	}}
	_, err := Import(d, doc(t, "## 1\n\n- a\n- b\n"), f, Options{Layout: true})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestImportDanglingEdge(t *testing.T) {
	d := model.NewDAG("x")
	f := &File{
		Nodes: []Node{{ID: "a", Title: "A", PRDRefs: []string{"p:1-1"}}},
		Edges: []Edge{{From: "a", To: "ghost"}},
	}
	_, err := Import(d, doc(t, "## 1\n\n- a\n"), f, Options{})
	if err == nil || !strings.Contains(err.Error(), "unknown to node") {
		t.Fatalf("expected dangling edge error, got %v", err)
	}
}

func TestImportLeafRequiresPRDRefs(t *testing.T) {
	d := model.NewDAG("x")
	f := &File{Nodes: []Node{
		{ID: "root", Title: "Root", PRDRefs: []string{"p:1-1"}},
		{ID: "leaf", Title: "Leaf"},
	}}
	f.Nodes[0].DependsOn = nil
	f.Edges = []Edge{{From: "root", To: "leaf"}}
	_, err := Import(d, doc(t, "## 1\n\n- a\n- b\n"), f, Options{})
	if err == nil || !strings.Contains(err.Error(), "prd_refs") {
		t.Fatalf("expected leaf prd_refs error, got %v", err)
	}

	// out_of_scope leaves are red lines and may omit prd_refs.
	d2 := model.NewDAG("x")
	f2 := &File{Nodes: []Node{
		{ID: "root", Title: "Root", PRDRefs: []string{"p:1-1"}},
		{ID: "red", Title: "Red", Scope: model.ScopeOutOfScope},
	}}
	f2.Edges = []Edge{{From: "root", To: "red"}}
	if _, err := Import(d2, doc(t, "## 1\n\n- a\n- b\n"), f2, Options{}); err != nil {
		t.Fatalf("out_of_scope leaf should be allowed: %v", err)
	}
}

func TestImportGeneratesIDsAndLayout(t *testing.T) {
	d := model.NewDAG("x")
	f := &File{Nodes: []Node{
		{Title: "基础规则", PRDRefs: []string{"p:1-1"}},
		{Title: "下游实现", PRDRefs: []string{"p:1-2"}, DependsOn: []string{"基础规则"}},
		{Title: "另一实现", PRDRefs: []string{"p:1-3"}, DependsOn: []string{"基础规则"}},
	}}
	res, err := Import(d, doc(t, "## 1\n\n- a\n- b\n- c\n"), f, Options{Layout: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AddedNodes) != 3 {
		t.Fatalf("added = %v", res.AddedNodes)
	}
	for _, n := range d.Nodes {
		if !strings.HasPrefix(n.ID, "rb-") || len(n.ID) < 7 {
			t.Fatalf("bad generated id %q", n.ID)
		}
	}
	byID := map[string]*model.Node{}
	for _, n := range d.Nodes {
		byID[n.ID] = n
	}
	if len(d.Edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(d.Edges))
	}
	src := byID[res.AddedNodes[0]]
	for _, n := range d.Nodes {
		if n.ID != src.ID && n.Position.X <= src.Position.X {
			t.Fatalf("downstream x %v <= source x %v", n.Position.X, src.Position.X)
		}
	}
	if d.Nodes[1].Position.Y == d.Nodes[2].Position.Y {
		t.Fatal("same-layer nodes should not overlap")
	}
	gap := d.Nodes[1].Position.Y - d.Nodes[2].Position.Y
	if gap < 0 {
		gap = -gap
	}
	if gap < 170 {
		t.Fatalf("same-layer vertical gap = %.0f, want >= 170 for long Chinese titles", gap)
	}
}

func TestImportUnknownPRDRef(t *testing.T) {
	f := &File{Nodes: []Node{{ID: "a", Title: "A", PRDRefs: []string{"p:9-9"}}}}
	d := model.NewDAG("x")
	_, err := Import(d, doc(t, "## 1\n\n- a\n"), f, Options{})
	if err == nil || !strings.Contains(err.Error(), "unknown PRD anchor") {
		t.Fatalf("expected unknown ref error, got %v", err)
	}
	d = model.NewDAG("x")
	res, err := Import(d, doc(t, "## 1\n\n- a\n"), f, Options{AllowUnknownRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected warning")
	}
}

func TestImportMergeAndReplace(t *testing.T) {
	d := model.NewDAG("x")
	md := doc(t, "## 1\n\n- a\n- b\n")
	f := &File{Nodes: []Node{{ID: "a", Title: "A", PRDRefs: []string{"p:1-1"}}}}
	if _, err := Import(d, md, f, Options{}); err != nil {
		t.Fatal(err)
	}
	f2 := &File{Nodes: []Node{{ID: "b", Title: "B", PRDRefs: []string{"p:1-2"}}}}
	res, err := Import(d, md, f2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Nodes) != 2 || len(res.AddedNodes) != 1 {
		t.Fatalf("merge failed: nodes=%d res=%+v", len(d.Nodes), res)
	}
	res, err = Import(d, md, f2, Options{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Nodes) != 1 || d.Nodes[0].ID != "b" {
		t.Fatalf("replace failed: %+v", d.Nodes)
	}
}

func TestCoverage(t *testing.T) {
	d := model.NewDAG("x")
	md := doc(t, "## 1\n\n- a\n- b\n\n## 2\n\n- c\n")
	Add := func(id, ref string) {
		d.AddNode(&model.Node{ID: id, Title: id, Scope: model.ScopeMine, Status: model.StatusPending, PRDRefs: []string{ref}})
	}
	Add("n1", "p:1-2")
	d.AddNode(&model.Node{ID: "red", Title: "red", Scope: model.ScopeOutOfScope, Status: model.StatusPending, PRDRefs: []string{"p:1-2"}})
	d.IgnoredPRDRefs = []string{"p:2-2"}
	c := ComputeCoverage(d, md)
	if c.Total != 3 || c.Covered != 1 || c.Ignored != 1 || len(c.Unmapped) != 1 {
		t.Fatalf("coverage = %+v", c)
	}
	if c.Percent < 66 || c.Percent > 67 {
		t.Fatalf("percent = %v", c.Percent)
	}
}

func TestLayoutParksNonMainNodesOutsideMainGrid(t *testing.T) {
	d := model.NewDAG("x")
	mk := func(id, title, scope, status string) *model.Node {
		n := &model.Node{ID: id, Title: title, Scope: scope, Status: status}
		d.AddNode(n)
		return n
	}
	a := mk("rb-a", "基础规则", model.ScopeMine, model.StatusPending)
	b := mk("rb-b", "下游实现", model.ScopeMine, model.StatusPending)
	ext := mk("rb-ext", "AuthService 接口", model.ScopeExternal, model.StatusPending)
	red := mk("rb-red", "退订（本期不做）", model.ScopeOutOfScope, model.StatusPending)
	for i := 0; i < 10; i++ {
		mk(fmt.Sprintf("rb-ext-%02d", i), fmt.Sprintf("外部依赖 %02d", i), model.ScopeExternal, model.StatusPending)
	}
	d.AddEdge(&model.Edge{ID: "ed-1", From: "rb-ext", To: "rb-a", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "ed-2", From: "rb-a", To: "rb-b", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "ed-3", From: "rb-red", To: "rb-b", Kind: model.EdgeHard})

	Layout(d)

	if a.Position.X != 80 || b.Position.X != 400 {
		t.Fatalf("main grid columns shifted by non-main nodes: a.x=%.0f b.x=%.0f", a.Position.X, b.Position.X)
	}
	if a.Position.Y <= ext.Position.Y || b.Position.Y <= red.Position.Y {
		t.Fatalf("main grid should start below the parked shelf: a.y=%.0f b.y=%.0f ext.y=%.0f red.y=%.0f",
			a.Position.Y, b.Position.Y, ext.Position.Y, red.Position.Y)
	}
	// Proposed nodes stay on the main canvas even when their scope is external.
	prop := mk("rb-prop", "待批外部", model.ScopeExternal, model.StatusProposed)
	Layout(d)
	if prop.Position.X < 80 || prop.Position.Y <= ext.Position.Y {
		t.Fatalf("proposed node should be laid out on the main grid: %+v", prop.Position)
	}
}
