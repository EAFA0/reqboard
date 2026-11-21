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

func TestImportAllowsTodoWithoutPRDRefs(t *testing.T) {
	d := model.NewDAG("x")
	f := &File{Nodes: []Node{
		{ID: "ddl", Title: "DDL: orders add confirmation_mode", Scope: model.ScopeTodo, Tags: []string{"ddl"}},
		{ID: "impl", Title: "创单校验", PRDRefs: []string{"p:1-1"}},
	}}
	f.Edges = []Edge{{From: "ddl", To: "impl"}}
	if _, err := Import(d, doc(t, "## 1\n\n- a\n"), f, Options{}); err != nil {
		t.Fatalf("todo without prd_refs should be allowed: %v", err)
	}
	if d.Node("ddl").Scope != model.ScopeTodo {
		t.Fatalf("todo scope not preserved: %+v", d.Node("ddl"))
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

func TestLayoutKeepsNaturalDepthAndPinsMilestones(t *testing.T) {
	d := model.NewDAG("x")
	mk := func(id string, tags ...string) *model.Node {
		n := &model.Node{ID: id, Title: id, Scope: model.ScopeMine, Status: model.StatusPending, Tags: tags}
		d.AddNode(n)
		return n
	}
	a0, _, a2, a3 := mk("a0"), mk("a1"), mk("a2"), mk("a3")
	b0, b1 := mk("b0"), mk("b1")
	iso := mk("iso")
	ms := mk("ms", "milestone", "e2e")

	d.AddEdge(&model.Edge{ID: "e1", From: "a0", To: "a1", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "e2", From: "a1", To: "a2", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "e3", From: "a2", To: "a3", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "e4", From: "b0", To: "b1", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "e5", From: "a0", To: "ms", Kind: model.EdgeDiscovered})

	Layout(d)

	if b0.Position.X != a0.Position.X {
		t.Fatalf("short chain should start at natural depth 0: b0.x=%.0f a0.x=%.0f", b0.Position.X, a0.Position.X)
	}
	if b1.Position.X != b0.Position.X+320 {
		t.Fatalf("short chain sink must keep natural depth: b1.x=%.0f b0.x=%.0f", b1.Position.X, b0.Position.X)
	}
	if b1.Position.X == a3.Position.X {
		t.Fatalf("ordinary short-chain sink b1 should not be right-aligned with a3 (both %.0f)", b1.Position.X)
	}
	if a2.Position.X != a0.Position.X+640 || a3.Position.X != a0.Position.X+960 {
		t.Fatalf("long chain depth changed: a2.x=%.0f a3.x=%.0f", a2.Position.X, a3.Position.X)
	}
	if ms.Position.X <= a3.Position.X {
		t.Fatalf("milestone ms.x=%.0f should sit right of feature sink a3.x=%.0f", ms.Position.X, a3.Position.X)
	}
	if iso.Position.X != a0.Position.X {
		t.Fatalf("isolated node iso.x=%.0f should stay on leftmost column %.0f", iso.Position.X, a0.Position.X)
	}
}

func TestLayoutFullPlacesDownstreamCollaboratorToTheRight(t *testing.T) {
	d := model.NewDAG("x")
	extUp := &model.Node{ID: "ext-up", Title: "Upstream API", Scope: model.ScopeExternal, Status: model.StatusPending}
	mine := &model.Node{ID: "m1", Title: "Core Retry", Scope: model.ScopeMine, Status: model.StatusPending}
	collabDown := &model.Node{ID: "c-down", Title: "Downstream Notify", Scope: model.ScopeCollaborator, Status: model.StatusPending}
	d.AddNode(extUp)
	d.AddNode(mine)
	d.AddNode(collabDown)
	d.AddEdge(&model.Edge{ID: "e1", From: "ext-up", To: "m1", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "e2", From: "m1", To: "c-down", Kind: model.EdgeHard})

	LayoutWith(d, true)

	if extUp.Position.X != 80 {
		t.Fatalf("zero-indegree upstream external should be in Column 0, got x=%.0f", extUp.Position.X)
	}
	if mine.Position.X <= extUp.Position.X {
		t.Fatalf("mine x=%.0f should be right of ext-up x=%.0f", mine.Position.X, extUp.Position.X)
	}
	if collabDown.Position.X <= mine.Position.X {
		t.Fatalf("downstream collaborator x=%.0f should be right of mine x=%.0f (no U-turn)", collabDown.Position.X, mine.Position.X)
	}
}

func TestLayoutFullPutsAllPrerequisiteScopesInColumnZero(t *testing.T) {
	d := model.NewDAG("x")
	mk := func(id, scope string) *model.Node {
		n := &model.Node{ID: id, Title: id, Scope: scope, Status: model.StatusPending}
		d.AddNode(n)
		return n
	}
	ext := mk("ext", model.ScopeExternal)
	collab := mk("collab", model.ScopeCollaborator)
	todo := mk("todo", model.ScopeTodo)
	mine := mk("mine", model.ScopeMine)
	red := mk("red", model.ScopeOutOfScope)
	d.AddEdge(&model.Edge{ID: "e1", From: "ext", To: "mine", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "e2", From: "collab", To: "mine", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "e3", From: "todo", To: "mine", Kind: model.EdgeHard})
	d.AddEdge(&model.Edge{ID: "e4", From: "red", To: "mine", Kind: model.EdgeHard})

	LayoutWith(d, true)

	for _, n := range []*model.Node{ext, collab, todo} {
		if n.Position.X != 80 {
			t.Fatalf("%s (%s) x=%.0f, want 80 (column 0)", n.ID, n.Scope, n.Position.X)
		}
	}
	if mine.Position.X <= 80 {
		t.Fatalf("mine node should flow right of prerequisite sources: x=%.0f", mine.Position.X)
	}
	if red.Position.Y <= mine.Position.Y {
		t.Fatalf("redline should sit below the flow: red.y=%.0f mine.y=%.0f", red.Position.Y, mine.Position.Y)
	}
}
