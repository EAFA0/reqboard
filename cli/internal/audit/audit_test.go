package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reqboard/internal/model"
	"reqboard/internal/testutil"
)

func setup(t *testing.T) (*model.Config, string, *model.DAG, *model.Bindings, string) {
	t.Helper()
	root := t.TempDir()
	repo := testutil.NewRepo(t, root, "api", map[string]string{
		"svc/order.go":      "package svc\n\nfunc CreateOrder() string {\n\treturn \"old\"\n}\n\nfunc CancelOrder() string {\n\treturn \"cancel\"\n}\n",
		"svc/order_mock.go": "package svc\n\nfunc MockCreate() string {\n\treturn \"mock\"\n}\n",
		"api/order.thrift":  "struct Order {\n  1: string id\n}\n",
	})
	s := testutil.InitWorkspace(t, root, "req", []model.Repo{repo})
	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	dag := model.NewDAG("req")
	a := testutil.AddNode(dag, "rb-a", "创单", model.ScopeMine, model.StatusInProgress)
	b := testutil.AddNode(dag, "rb-b", "废弃查询", model.ScopeMine, model.StatusDeprecated)
	c := testutil.AddNode(dag, "rb-c", "断链", model.ScopeMine, model.StatusPending)
	d := testutil.AddNode(dag, "rb-d", "队友节点", model.ScopeMine, model.StatusInProgress)
	e := testutil.AddNode(dag, "rb-e", "契约", model.ScopeMine, model.StatusPending)
	bindings := model.NewBindings()
	add := func(id, node, file, symbol string) {
		bindings.Bindings = append(bindings.Bindings, model.Binding{
			ID: id, NodeID: node, Repo: "api", File: file, Granularity: model.GranularitySymbol,
			Symbol: symbol, Role: model.RoleImpl, Why: "test", BoundAt: model.Now(),
		})
	}
	add("bd-1", a.ID, "svc/order.go", "CreateOrder")
	add("bd-2", b.ID, "svc/order.go", "CancelOrder")
	add("bd-3", c.ID, "svc/order.go", "MissingSymbol")
	add("bd-4", e.ID, "api/order.thrift", "Order")
	bindings.Leases = []model.Lease{
		{NodeID: a.ID, SessionID: "s-a", Patterns: []string{"api:svc/**"}},
		{NodeID: d.ID, SessionID: "s-d", Patterns: []string{"api:other/**"}},
	}
	testutil.SaveDAG(t, s, dag)
	testutil.SaveBindings(t, s, bindings)

	// Working-tree changes.
	repoPath := filepath.Join(root, "api")
	testutil.Write(t, filepath.Join(repoPath, "svc/order.go"),
		"package svc\n\nfunc CreateOrder() string {\n\treturn \"new\"\n}\n\nfunc CancelOrder() string {\n\treturn \"cancel-v2\"\n}\n")
	testutil.Write(t, filepath.Join(repoPath, "svc/teammate.go"),
		"package svc\n\nfunc TeammateWork() string {\n\treturn \"wip\"\n}\n")
	testutil.Write(t, filepath.Join(repoPath, "other/teammate.go"),
		"package other\n\nfunc OtherWork() string {\n\treturn \"wip\"\n}\n")
	testutil.Write(t, filepath.Join(repoPath, "pkg/orphan.go"),
		"package pkg\n\nfunc SneakyOverscope() string {\n\treturn \"x\"\n}\n")
	testutil.Write(t, filepath.Join(repoPath, "svc/order_mock.go"),
		"package svc\n\nfunc MockCreate() string {\n\treturn \"mock-v2\"\n}\n")
	testutil.Write(t, filepath.Join(repoPath, "api/order.thrift"),
		"struct Order {\n  1: string id\n  2: string title\n}\n")
	return cfg, root, dag, bindings, repoPath
}

func TestAuditAllBuckets(t *testing.T) {
	cfg, root, dag, bindings, _ := setup(t)
	rep := Run(cfg, root, dag, bindings, Options{All: true})

	if len(rep.Attributed) != 2 {
		t.Fatalf("attributed = %+v", rep.Attributed)
	}
	attr := map[string]Entry{}
	for _, e := range rep.Attributed {
		attr[e.Symbol] = e
	}
	if e, ok := attr["CreateOrder"]; !ok || e.Added != 1 || e.Deleted != 1 || e.NodeIDs[0] != "rb-a" {
		t.Fatalf("CreateOrder attribution = %+v", e)
	}
	if e, ok := attr["Order"]; !ok || e.NodeIDs[0] != "rb-e" {
		t.Fatalf("thrift Order attribution = %+v", e)
	}

	if len(rep.ToDelete) != 1 || rep.ToDelete[0].Symbol != "CancelOrder" || rep.ToDelete[0].NodeIDs[0] != "rb-b" {
		t.Fatalf("to_delete = %+v", rep.ToDelete)
	}
	if len(rep.Orphan) != 1 || rep.Orphan[0].Symbol != "SneakyOverscope" || rep.Orphan[0].Added != 5 {
		t.Fatalf("orphan = %+v", rep.Orphan)
	}
	if len(rep.Leased) != 2 {
		t.Fatalf("leased = %+v", rep.Leased)
	}
	if len(rep.Broken) != 1 || rep.Broken[0].BindingID != "bd-3" {
		t.Fatalf("broken = %+v", rep.Broken)
	}
	if len(rep.Broken[0].Available) == 0 {
		t.Fatal("broken binding should list real symbols")
	}
	if len(rep.Ignored) != 1 || rep.Ignored[0] != "api:svc/order_mock.go" {
		t.Fatalf("ignored = %+v", rep.Ignored)
	}
	if rep.ChangedFiles != 5 {
		t.Fatalf("changed_files = %d, want 5 (ignored generated file excluded)", rep.ChangedFiles)
	}
	if rep.NodeStats["rb-a"].Added == 0 || rep.NodeStats["rb-a"].Deleted == 0 {
		t.Fatalf("node stats = %+v", rep.NodeStats)
	}
	if rep.Violations == 0 {
		t.Fatal("expected violations")
	}
}

func TestAuditNodeScope(t *testing.T) {
	cfg, root, dag, bindings, _ := setup(t)

	rep := Run(cfg, root, dag, bindings, Options{NodeID: "rb-a"})
	if len(rep.Attributed) != 1 || rep.Attributed[0].Symbol != "CreateOrder" {
		t.Fatalf("node attributed = %+v", rep.Attributed)
	}
	if len(rep.Orphan) != 1 || rep.Orphan[0].Symbol != "TeammateWork" {
		t.Fatalf("node own-lease orphan = %+v", rep.Orphan)
	}
	if len(rep.ToDelete) != 0 || len(rep.Broken) != 0 {
		t.Fatalf("node a should not see other buckets: %+v %+v", rep.ToDelete, rep.Broken)
	}

	rep = Run(cfg, root, dag, bindings, Options{NodeID: "rb-b"})
	if len(rep.ToDelete) != 1 || rep.ToDelete[0].Symbol != "CancelOrder" {
		t.Fatalf("deprecated node to_delete = %+v", rep.ToDelete)
	}
	if len(rep.Orphan) != 0 {
		t.Fatalf("deprecated node orphans = %+v", rep.Orphan)
	}

	rep = Run(cfg, root, dag, bindings, Options{NodeID: "rb-c"})
	if len(rep.Broken) != 1 {
		t.Fatalf("broken node = %+v", rep.Broken)
	}
	if len(rep.Orphan) != 0 {
		t.Fatalf("node c orphans = %+v", rep.Orphan)
	}
}

func TestAuditRepoError(t *testing.T) {
	root := t.TempDir()
	s := testutil.InitWorkspace(t, root, "req", []model.Repo{{Name: "gone", Path: "missing", BaseRef: "master"}})
	cfg, _ := s.LoadConfig()
	rep := Run(cfg, root, model.NewDAG("req"), model.NewBindings(), Options{All: true})
	if len(rep.Repos) != 1 || rep.Repos[0].Error == "" {
		t.Fatalf("repos = %+v", rep.Repos)
	}
	if rep.Violations == 0 {
		t.Fatal("missing repo should count as violation")
	}
}

func TestAuditDeletedFileBrokenBinding(t *testing.T) {
	root := t.TempDir()
	repo := testutil.NewRepo(t, root, "api", map[string]string{
		"svc/order.go": "package svc\n\nfunc CreateOrder() string { return \"x\" }\n",
	})
	s := testutil.InitWorkspace(t, root, "req", []model.Repo{repo})
	cfg, _ := s.LoadConfig()
	dag := model.NewDAG("req")
	testutil.AddNode(dag, "rb-a", "A", model.ScopeMine, model.StatusPending)
	b := model.NewBindings()
	b.Bindings = append(b.Bindings, model.Binding{ID: "bd-1", NodeID: "rb-a", Repo: "api", File: "svc/order.go", Granularity: "symbol", Symbol: "CreateOrder", Role: "impl", Why: "x", BoundAt: model.Now()})
	testutil.SaveDAG(t, s, dag)
	testutil.SaveBindings(t, s, b)
	if err := os.Remove(filepath.Join(root, "api", "svc", "order.go")); err != nil {
		t.Fatal(err)
	}
	rep := Run(cfg, root, dag, b, Options{All: true})
	if len(rep.Broken) != 1 || rep.Broken[0].Reason == "" {
		t.Fatalf("deleted file should break binding: %+v", rep.Broken)
	}
}

func TestAuditExcludesTestsByDefault(t *testing.T) {
	cfg, root, dag, bindings, repoPath := setup(t)
	// A changed test file must be ignored even when it is explicitly bound.
	testutil.Write(t, filepath.Join(repoPath, "pkg/order_test.go"),
		"package svc\n\nimport \"testing\"\n\nfunc TestCreateOrder(t *testing.T) {}\n")
	bindings.Bindings = append(bindings.Bindings, model.Binding{
		ID: "bd-test", NodeID: "rb-a", Repo: "api", File: "pkg/order_test.go",
		Granularity: model.GranularityFile, Role: model.RoleTest, Why: "unit test", BoundAt: model.Now(),
	})

	rep := Run(cfg, root, dag, bindings, Options{All: true})
	if !rep.Filter.ExcludeTests {
		t.Fatal("exclude_tests should default to true")
	}
	found := false
	for _, f := range rep.Ignored {
		if f == "api:pkg/order_test.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("test file not reported as ignored: %+v", rep.Ignored)
	}
	for _, e := range rep.Orphan {
		if e.File == "pkg/order_test.go" {
			t.Fatalf("test file leaked into orphan diff: %+v", e)
		}
	}
}

func TestAuditExcludeTestsOptOut(t *testing.T) {
	cfg, root, dag, bindings, repoPath := setup(t)
	no := false
	cfg.ExcludeTests = &no
	testutil.Write(t, filepath.Join(repoPath, "pkg/order_test.go"),
		"package svc\n\nimport \"testing\"\n\nfunc TestCreateOrder(t *testing.T) {}\n")

	rep := Run(cfg, root, dag, bindings, Options{All: true})
	if rep.Filter.ExcludeTests {
		t.Fatal("exclude_tests should be false after opt-out")
	}
	found := false
	for _, e := range rep.Orphan {
		if e.File == "pkg/order_test.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("test file should surface as orphan when exclusion is off: %+v", rep.Orphan)
	}
}

func TestAuditIncludeGlobsAllowList(t *testing.T) {
	cfg, root, dag, bindings, _ := setup(t)
	cfg.IncludeGlobs = []string{"svc/**"}

	rep := Run(cfg, root, dag, bindings, Options{All: true})
	if len(rep.Filter.IncludeGlobs) != 1 || rep.Filter.IncludeGlobs[0] != "svc/**" {
		t.Fatalf("filter include_globs = %+v", rep.Filter.IncludeGlobs)
	}
	for _, e := range append(append([]Entry{}, rep.Attributed...), rep.Orphan...) {
		if !strings.HasPrefix(e.File, "svc/") {
			t.Fatalf("file outside include allow-list leaked into audit: %+v", e)
		}
	}
	found := false
	for _, f := range rep.Ignored {
		if f == "api:api/order.thrift" {
			found = true
		}
	}
	if !found {
		t.Fatalf("file outside include allow-list not reported as ignored: %+v", rep.Ignored)
	}
}

func TestEffectiveFilterDefaults(t *testing.T) {
	f := EffectiveFilter(&model.Config{})
	if !f.ExcludeTests || len(f.IncludeGlobs) != 0 || len(f.IgnoreGlobs) != 0 {
		t.Fatalf("defaults = %+v", f)
	}
	yes := false
	f = EffectiveFilter(&model.Config{ExcludeTests: &yes, IncludeGlobs: []string{"*.go"}, IgnoreGlobs: []string{"vendor/**"}})
	if f.ExcludeTests || len(f.IncludeGlobs) != 1 || len(f.IgnoreGlobs) != 1 {
		t.Fatalf("explicit = %+v", f)
	}
}

func TestAuditIncludeGlobsRepoQualified(t *testing.T) {
	cfg, root, dag, bindings, _ := setup(t)
	cfg.IncludeGlobs = []string{"api:api/*.thrift"}

	rep := Run(cfg, root, dag, bindings, Options{All: true})
	if len(rep.Attributed) != 1 || rep.Attributed[0].Symbol != "Order" {
		t.Fatalf("attributed = %+v", rep.Attributed)
	}
	if len(rep.Orphan) != 0 {
		t.Fatalf("repo-qualified include should drop non-matching files: %+v", rep.Orphan)
	}
}
