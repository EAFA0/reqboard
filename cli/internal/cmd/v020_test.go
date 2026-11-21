package cmd

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"reqboard/internal/model"
	"reqboard/internal/testutil"
)

func TestNodeDeleteWithoutBindingsRemovesNodeAndEdges(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "node", "add", "--title", "误建节点", "--prd-ref", "p:3-2", "--json")
	id := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	mustCLI(t, f.root, "edge", "add", "--from", "rb-a", "--to", id, "--kind", "hard")

	before, _ := f.store.LoadDAG()
	beforeEdges := len(before.Edges)
	mustCLI(t, f.root, "node", "delete", id)

	dag, _ := f.store.LoadDAG()
	if dag.Node(id) != nil {
		t.Fatalf("node %s still exists", id)
	}
	for _, e := range dag.Edges {
		if e.From == id || e.To == id {
			t.Fatalf("edge %s still references deleted node", e.ID)
		}
	}
	if len(dag.Edges) != beforeEdges-1 {
		t.Fatalf("edges = %d, want %d", len(dag.Edges), beforeEdges-1)
	}
	events, _ := f.store.Events(0)
	found := false
	for _, ev := range events {
		if ev.Action == "node.delete" && ev.NodeID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("node.delete event missing: %+v", events)
	}
}

func TestNodeDeleteWithBindingsRequiresForceAndUndoRestores(t *testing.T) {
	f := setupCLI(t)
	mustCLI(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/order.go", "--symbol", "CreateOrder", "--why", "PRD 1")
	_, errOut, code := cli(t, f.root, "node", "delete", "rb-a")
	if code == 0 || !strings.Contains(errOut, "deprecate") || !strings.Contains(errOut, "--force") {
		t.Fatalf("delete with bindings not refused with guidance: %s", errOut)
	}

	mustCLI(t, f.root, "node", "delete", "rb-a", "--force")
	dag, _ := f.store.LoadDAG()
	if dag.Node("rb-a") != nil {
		t.Fatal("force delete left the node behind")
	}
	bindings, _ := f.store.LoadBindings()
	if len(bindings.BindingsForNode("rb-a")) != 0 {
		t.Fatalf("force delete left bindings: %+v", bindings.Bindings)
	}

	// One undo restores both the node and its bindings.
	mustCLI(t, f.root, "undo")
	dag, _ = f.store.LoadDAG()
	if dag.Node("rb-a") == nil {
		t.Fatal("undo did not restore the deleted node")
	}
	bindings, _ = f.store.LoadBindings()
	if len(bindings.BindingsForNode("rb-a")) != 1 {
		t.Fatalf("undo did not restore bindings: %+v", bindings.Bindings)
	}
}

func TestUndoRedoStacksAndDepth(t *testing.T) {
	f := setupCLI(t)
	base := jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	baseUndo := int(base["undo_depth"].(float64))

	out := mustCLI(t, f.root, "node", "add", "--title", "撤销测试", "--prd-ref", "p:3-2", "--json")
	id := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	mustCLI(t, f.root, "node", "update", id, "--title", "撤销测试 v2")

	status := jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	if int(status["undo_depth"].(float64)) != baseUndo+2 {
		t.Fatalf("undo_depth = %v, want %d", status["undo_depth"], baseUndo+2)
	}
	mustCLI(t, f.root, "undo")
	dag, _ := f.store.LoadDAG()
	if dag.Node(id).Title != "撤销测试" {
		t.Fatalf("undo title = %q", dag.Node(id).Title)
	}
	status = jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	if int(status["undo_depth"].(float64)) != baseUndo+1 || int(status["redo_depth"].(float64)) != 1 {
		t.Fatalf("after undo depths = %v/%v", status["undo_depth"], status["redo_depth"])
	}
	mustCLI(t, f.root, "redo")
	dag, _ = f.store.LoadDAG()
	if dag.Node(id).Title != "撤销测试 v2" {
		t.Fatalf("redo title = %q", dag.Node(id).Title)
	}
	status = jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	if int(status["redo_depth"].(float64)) != 0 {
		t.Fatalf("redo_depth = %v, want 0", status["redo_depth"])
	}
	// A new write invalidates the redo branch.
	mustCLI(t, f.root, "node", "update", id, "--description", "new branch")
	status = jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	if int(status["redo_depth"].(float64)) != 0 {
		t.Fatalf("redo_depth after new write = %v, want 0", status["redo_depth"])
	}
}

func TestUndoRedoCapsAtTwentySteps(t *testing.T) {
	f := setupCLI(t)
	for i := 0; i < 25; i++ {
		mustCLI(t, f.root, "node", "add", "--title", "批量"+strconv.Itoa(i), "--prd-ref", "p:3-2")
	}
	status := jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	if int(status["undo_depth"].(float64)) != 20 {
		t.Fatalf("undo_depth = %v, want 20", status["undo_depth"])
	}
	for i := 0; i < 20; i++ {
		mustCLI(t, f.root, "undo")
	}
	if _, _, code := cli(t, f.root, "undo"); code == 0 {
		t.Fatal("21st undo should fail")
	}
}

func TestOpsAddToggleRemove(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "ops", "add", "rb-a", "--kind", "ddl", "--title", "新增可见性索引", "--note", "上线前执行", "--json")
	op := jsonMap(t, out)["op"].(map[string]interface{})
	opID := op["id"].(string)
	if op["kind"] != "ddl" || op["title"] != "新增可见性索引" || op["done"] != false {
		t.Fatalf("op = %+v", op)
	}
	dag, _ := f.store.LoadDAG()
	if len(dag.Node("rb-a").Ops) != 1 {
		t.Fatalf("ops = %+v", dag.Node("rb-a").Ops)
	}
	mustCLI(t, f.root, "ops", "toggle", "rb-a", opID)
	dag, _ = f.store.LoadDAG()
	if !dag.Node("rb-a").Ops[0].Done {
		t.Fatal("ops toggle did not mark done")
	}
	mustCLI(t, f.root, "ops", "remove", "rb-a", opID)
	dag, _ = f.store.LoadDAG()
	if len(dag.Node("rb-a").Ops) != 0 {
		t.Fatalf("ops remove left = %+v", dag.Node("rb-a").Ops)
	}
	if _, _, code := cli(t, f.root, "ops", "add", "rb-a", "--kind", "nope", "--title", "x"); code == 0 {
		t.Fatal("invalid op kind not rejected")
	}
	if _, _, code := cli(t, f.root, "ops", "toggle", "rb-a", "op-99"); code == 0 {
		t.Fatal("unknown op id not rejected")
	}
}

func TestBindAllSymbolsAndKeepStatus(t *testing.T) {
	f := setupCLI(t)
	testutil.Write(t, filepath.Join(f.repoPath, "svc/order.go"),
		"package svc\n\nfunc CreateOrder() string {\n\treturn \"new\"\n}\n\nfunc CancelOrder() string {\n\treturn \"cancel-v2\"\n}\n")
	out := mustCLI(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/order.go", "--all-symbols", "--keep-status", "--why", "PRD 1 创单", "--json")
	m := jsonMap(t, out)
	if int(m["created"].(float64)) != 2 {
		t.Fatalf("created = %v, want 2", m["created"])
	}
	dag, _ := f.store.LoadDAG()
	if dag.Node("rb-a").Status != model.StatusPending {
		t.Fatalf("--keep-status changed node status to %s", dag.Node("rb-a").Status)
	}
	bindings, _ := f.store.LoadBindings()
	got := map[string]bool{}
	for _, b := range bindings.BindingsForNode("rb-a") {
		got[b.Symbol] = true
	}
	if !got["CreateOrder"] || !got["CancelOrder"] {
		t.Fatalf("all-symbols bindings = %+v", got)
	}

	// Without --keep-status the first bind still moves pending -> in_progress.
	out = mustCLI(t, f.root, "bind", "rb-b", "--repo", "api", "--file", "svc/order.go", "--all-symbols", "--why", "PRD 3", "--json")
	if int(jsonMap(t, out)["created"].(float64)) != 2 {
		t.Fatalf("second all-symbols bind = %s", out)
	}
	dag, _ = f.store.LoadDAG()
	if dag.Node("rb-b").Status != model.StatusInProgress {
		t.Fatalf("rb-b status = %s, want in_progress", dag.Node("rb-b").Status)
	}

	// A file with no diff has nothing to bind.
	testutil.Write(t, filepath.Join(f.repoPath, "svc/stable.go"), "package svc\n\nfunc Stable() string { return \"x\" }\n")
	testutil.Commit(t, f.repoPath, "stable")
	if _, errOut, code := cli(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/stable.go", "--all-symbols", "--why", "x"); code == 0 || !strings.Contains(errOut, "无改动") {
		t.Fatalf("unchanged file not rejected: %s", errOut)
	}
}

func TestStatusTextCountsEveryStatus(t *testing.T) {
	f := setupCLI(t)
	mustCLI(t, f.root, "mock", "add", "rb-a", "--desc", "库存服务先 mock")
	out := mustCLI(t, f.root, "node", "add", "--title", "打回节点", "--prd-ref", "p:3-2", "--json")
	id := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	mustCLI(t, f.root, "node", "review", id, "--request-changes", "--note", "改一下")

	text := mustCLI(t, f.root, "status")
	if !strings.Contains(text, "mocked 1") {
		t.Fatalf("status text missing mocked count:\n%s", text)
	}
	if !strings.Contains(text, "changes_requested 1") {
		t.Fatalf("status text missing changes_requested count:\n%s", text)
	}
	line := ""
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "Nodes:") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("Nodes line missing:\n%s", text)
	}
	totalRe := regexp.MustCompile(`Nodes:\s+(\d+)\s+total`)
	totalMatch := totalRe.FindStringSubmatch(line)
	if totalMatch == nil {
		t.Fatalf("cannot parse total: %s", line)
	}
	total, _ := strconv.Atoi(totalMatch[1])
	countRe := regexp.MustCompile(`\|\s*([a-z_]+)\s+(\d+)`)
	sum := 0
	for _, m := range countRe.FindAllStringSubmatch(line, -1) {
		n, _ := strconv.Atoi(m[2])
		sum += n
	}
	if sum != total {
		t.Fatalf("status counts sum %d != Nodes total %d: %s", sum, total, line)
	}
	status := jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	counts := status["status_counts"].(map[string]interface{})
	jsonSum := 0
	for _, v := range counts {
		jsonSum += int(v.(float64))
	}
	if jsonSum != total {
		t.Fatalf("status_counts sum %d != %d: %v", jsonSum, total, counts)
	}
}

func TestLayoutIncludeExternalPutsSourcesInColumnZero(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "node", "add", "--title", "AuthService 接口", "--scope", "external", "--prd-ref", "p:3-2", "--json")
	ext := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	mustCLI(t, f.root, "edge", "add", "--from", ext, "--to", "rb-a", "--kind", "hard")

	out = mustCLI(t, f.root, "layout", "--include-external", "--json")
	m := jsonMap(t, out)
	if m["include_external"] != true || m["layout"] != "full-graph-external-column-0" {
		t.Fatalf("layout output = %v", m)
	}
	dag, _ := f.store.LoadDAG()
	if dag.Node(ext).Position.X != 80 {
		t.Fatalf("external x = %.0f, want 80 (column 0)", dag.Node(ext).Position.X)
	}
	if dag.Node("rb-a").Position.X <= dag.Node(ext).Position.X {
		t.Fatalf("downstream x = %.0f, external x = %.0f", dag.Node("rb-a").Position.X, dag.Node(ext).Position.X)
	}

	// Default noise-reduced layout parks external nodes above the main grid.
	mustCLI(t, f.root, "layout")
	dag, _ = f.store.LoadDAG()
	if dag.Node("rb-a").Position.X != 80 {
		t.Fatalf("main x after default layout = %.0f, want 80", dag.Node("rb-a").Position.X)
	}
	if dag.Node(ext).Position.Y >= dag.Node("rb-a").Position.Y {
		t.Fatalf("external should be parked above main grid: ext.y=%.0f main.y=%.0f",
			dag.Node(ext).Position.Y, dag.Node("rb-a").Position.Y)
	}
}
