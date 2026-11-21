package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"reqboard/internal/model"
	"reqboard/internal/store"
	"reqboard/internal/testutil"
)

const prdMarkdown = `# 订单可见性需求

## 1 创单

- 用户可创建订单。
- 创单需校验库存。

## 2 退订

退订不在本期范围。

## 3 查询

订单列表需要展示状态。
`

const planJSON = `{"nodes":[
 {"id":"rb-a","title":"基础创单","scope":"mine","prd_refs":["p:1-2","p:1-3"]},
 {"id":"rb-b","title":"订单查询","scope":"mine","prd_refs":["p:3-2"],"depends_on":["rb-a"]},
 {"id":"rb-c","title":"退订（本期不做）","scope":"out_of_scope","prd_refs":["p:2-2"]}
]}`

func captureCLI(fn func() int) (string, string, int) {
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, rOut) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, rErr) }()
	code := fn()
	_ = wOut.Close()
	_ = wErr.Close()
	wg.Wait()
	os.Stdout, os.Stderr = oldOut, oldErr
	return outBuf.String(), errBuf.String(), code
}

func cli(t *testing.T, root string, args ...string) (string, string, int) {
	t.Helper()
	full := append([]string{}, args...)
	full = append(full, "--root", root)
	return captureCLI(func() int { return Main(full) })
}

func mustCLI(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, errOut, code := cli(t, root, args...)
	if code != 0 {
		t.Fatalf("reqboard %v failed (%d)\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
	}
	return out
}

func jsonMap(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse json: %v\n%s", err, s)
	}
	return m
}

type fixture struct {
	root     string
	repoPath string
	store    *store.Store
}

func setupCLI(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	testutil.NewRepo(t, root, "api", map[string]string{
		"svc/order.go": "package svc\n\nfunc CreateOrder() string {\n\treturn \"old\"\n}\n\nfunc CancelOrder() string {\n\treturn \"cancel\"\n}\n",
	})
	testutil.Write(t, filepath.Join(root, "prd_src.md"), prdMarkdown)
	testutil.Write(t, filepath.Join(root, "plan.json"), planJSON)
	mustCLI(t, root, "init", "--name", "req", "--repo", "api=api:master")
	mustCLI(t, root, "prd", "init", filepath.Join(root, "prd_src.md"))
	mustCLI(t, root, "plan", "import", filepath.Join(root, "plan.json"))
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{root: root, repoPath: filepath.Join(root, "api"), store: s}
}

func TestCLIInitPRDPlanStatus(t *testing.T) {
	f := setupCLI(t)
	out, errOut, code := cli(t, f.root, "status", "--json")
	if code != 0 {
		t.Fatalf("status failed: %s %s", out, errOut)
	}
	m := jsonMap(t, out)
	if m["ok"] != true {
		t.Fatalf("status ok = %v", m["ok"])
	}
	dag := m["dag"].(map[string]interface{})
	if len(dag["nodes"].([]interface{})) != 3 {
		t.Fatalf("nodes = %v", dag["nodes"])
	}
	prd := m["prd"].(map[string]interface{})
	blocks := prd["blocks"].([]interface{})
	if len(blocks) == 0 {
		t.Fatal("no prd blocks")
	}
	cov := prd["coverage"].(map[string]interface{})
	if cov["percent"].(float64) != 100 {
		t.Fatalf("coverage = %v", cov)
	}
	if cov["unmapped"].(float64) != 0 {
		t.Fatalf("unexpected unmapped: %v", cov)
	}
	if m["dispatch"].(map[string]interface{})["status"] != "READY" {
		t.Fatalf("dispatch = %v", m["dispatch"])
	}
	check := m["check"].(map[string]interface{})
	if check["changed_files"].(float64) != 0 {
		t.Fatalf("changed files = %v", check["changed_files"])
	}
}

func TestClaimRejectionsAndInjection(t *testing.T) {
	f := setupCLI(t)

	_, errOut, code := cli(t, f.root, "claim", "rb-c")
	if code == 0 || !strings.Contains(errOut, "out_of_scope") {
		t.Fatalf("out_of_scope claim not blocked: code=%d err=%s", code, errOut)
	}

	out := mustCLI(t, f.root, "node", "add", "--title", "外部依赖", "--scope", "external", "--prd-ref", "p:3-2", "--json")
	externalID := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	out = mustCLI(t, f.root, "propose", "--from", "rb-a", "--prd-ref", "p:3-2", "--title", "发现新边界", "--reason", "测试", "--json")
	proposedID := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	if _, errOut, code = cli(t, f.root, "claim", proposedID); code == 0 || !strings.Contains(errOut, "proposed") {
		t.Fatalf("proposed claim not blocked: %s", errOut)
	}
	if _, errOut, code = cli(t, f.root, "claim", externalID); code == 0 || !strings.Contains(errOut, "external") {
		t.Fatalf("external claim not blocked: %s", errOut)
	}

	mustCLI(t, f.root, "node", "update", "rb-a", "--hold", "true")
	if _, errOut, code = cli(t, f.root, "claim", "rb-a"); code == 0 || !strings.Contains(errOut, "hold") {
		t.Fatalf("hold claim not blocked: %s", errOut)
	}
	mustCLI(t, f.root, "node", "update", "rb-a", "--hold", "false")

	edgeID := edgeIDFor(t, f.root, "rb-a", "rb-b")
	mustCLI(t, f.root, "edge", "update", edgeID, "--kind", "mocked", "--note", "因 AuthService 接口方案未确认，这条链路先 mock")

	out = mustCLI(t, f.root, "claim", "rb-b", "--session", "s-1", "--lease", "api:svc/**")
	for _, want := range []string{"锚定 PRD 原文", "订单列表需要展示状态", "退订不在本期范围", "因 AuthService 接口方案未确认", "实施规则", "s-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("claim output missing %q:\n%s", want, out)
		}
	}
	b, _ := f.store.LoadBindings()
	if len(b.Leases) != 1 || b.Leases[0].SessionID != "s-1" {
		t.Fatalf("lease not written: %+v", b.Leases)
	}
}

func TestBindValidationAndAttribution(t *testing.T) {
	f := setupCLI(t)
	mustCLI(t, f.root, "claim", "rb-a", "--session", "s-1", "--lease", "api:svc/**")

	_, errOut, code := cli(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/order.go", "--symbol", "Nope", "--role", "impl", "--why", "x")
	if code == 0 || !strings.Contains(errOut, "CreateOrder") || !strings.Contains(errOut, "CancelOrder") {
		t.Fatalf("wrong symbol not rejected with real names: %s", errOut)
	}
	if _, _, code = cli(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/order.go", "--symbol", "CreateOrder", "--why", ""); code == 0 {
		t.Fatal("empty --why not rejected")
	}
	if _, errOut, code = cli(t, f.root, "bind", "rb-c", "--repo", "api", "--file", "svc/order.go", "--symbol", "CreateOrder", "--why", "x"); code == 0 || !strings.Contains(errOut, "out_of_scope") {
		t.Fatalf("out_of_scope bind not rejected: %s", errOut)
	}

	out := mustCLI(t, f.root, "propose", "--from", "rb-a", "--prd-ref", "p:3-2", "--title", "待批节点", "--reason", "测试", "--json")
	pid := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	if _, errOut, code = cli(t, f.root, "bind", pid, "--repo", "api", "--file", "svc/order.go", "--symbol", "CreateOrder", "--why", "x"); code == 0 || !strings.Contains(errOut, "proposed") {
		t.Fatalf("proposed bind not rejected: %s", errOut)
	}

	mustCLI(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/order.go", "--symbol", "CreateOrder", "--role", "impl", "--why", "PRD 1 创单", "--session", "s-1")
	b, _ := f.store.LoadBindings()
	if len(b.Bindings) != 1 || b.Bindings[0].Symbol != "CreateOrder" || b.Bindings[0].Why != "PRD 1 创单" {
		t.Fatalf("binding not written: %+v", b.Bindings)
	}
	testutil.Write(t, filepath.Join(f.repoPath, "svc/order.go"), "package svc\n\nfunc CreateOrder() string {\n\treturn \"new\"\n}\n\nfunc CancelOrder() string {\n\treturn \"cancel\"\n}\n")
	out, _, code = cli(t, f.root, "check", "--node", "rb-a", "--json")
	if code != 0 {
		t.Fatalf("check should be clean: %s", out)
	}
	m := jsonMap(t, out)
	buckets := m["buckets"].(map[string]interface{})
	if len(buckets["attributed"].([]interface{})) != 1 {
		t.Fatalf("attributed = %v", buckets["attributed"])
	}
}

func TestBindRejectsAmbiguousMethod(t *testing.T) {
	f := setupCLI(t)
	testutil.Write(t, filepath.Join(f.repoPath, "svc/ambiguous.go"), `package svc

type A struct{}

func (a A) Do() {}

type B struct{}

func (b B) Do() {}
`)
	_, errOut, code := cli(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/ambiguous.go", "--symbol", "Do", "--why", "x")
	if code == 0 || !strings.Contains(errOut, "A.Do") || !strings.Contains(errOut, "B.Do") {
		t.Fatalf("ambiguous method not rejected: %s", errOut)
	}
	mustCLI(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/ambiguous.go", "--symbol", "A.Do", "--why", "x")
}

func TestEdgeMockedToHardMarksStale(t *testing.T) {
	f := setupCLI(t)
	eid := edgeIDFor(t, f.root, "rb-a", "rb-b")
	mustCLI(t, f.root, "edge", "update", eid, "--kind", "mocked", "--note", "AuthService 未确认")
	out := mustCLI(t, f.root, "edge", "update", eid, "--kind", "hard", "--json")
	if len(jsonMap(t, out)["stale_nodes"].([]interface{})) != 1 {
		t.Fatalf("stale_nodes missing: %s", out)
	}
	dag, _ := f.store.LoadDAG()
	n := dag.Node("rb-b")
	if !n.Stale || !strings.Contains(n.StaleReason, "mocked") {
		t.Fatalf("target not stale: %+v", n)
	}
}

func TestExternalDoneSkipsCodeGatesAndUnmocksDownstream(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "node", "add", "--title", "AuthService 接口", "--scope", "external", "--prd-ref", "p:3-2", "--json")
	ext := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	mustCLI(t, f.root, "edge", "add", "--from", ext, "--to", "rb-b", "--kind", "mocked", "--note", "AuthService 未确认")

	// rb-b still has a pending mine upstream (rb-a), but external nodes carry no
	// code gates: marking the dependency ready must succeed.
	out = mustCLI(t, f.root, "done", ext, "--unmock-downstream", "--json")
	m := jsonMap(t, out)
	if m["status"] != model.StatusDone || m["external"] != true {
		t.Fatalf("external done = %v", m)
	}
	if len(m["unmocked_edges"].([]interface{})) != 1 {
		t.Fatalf("unmocked_edges = %v", m["unmocked_edges"])
	}
	staleNodes := m["stale_nodes"].([]interface{})
	if len(staleNodes) != 1 || staleNodes[0] != "rb-b" {
		t.Fatalf("stale_nodes = %v", staleNodes)
	}

	dag, _ := f.store.LoadDAG()
	if dag.Node(ext).Status != model.StatusDone {
		t.Fatalf("external node not done: %+v", dag.Node(ext))
	}
	eid := edgeIDFor(t, f.root, ext, "rb-b")
	if dag.Edge(eid).Kind != model.EdgeHard {
		t.Fatalf("edge not switched to hard: %+v", dag.Edge(eid))
	}
	down := dag.Node("rb-b")
	if !down.Stale || !strings.Contains(down.StaleReason, "外部依赖") || !strings.Contains(down.StaleReason, "拆 Mock") {
		t.Fatalf("downstream not stale with external reason: %+v", down)
	}

	events, _ := f.store.Events(0)
	found := false
	for _, ev := range events {
		if ev.Action == "external.unmock_downstream" && ev.NodeID == ext {
			found = true
		}
	}
	if !found {
		t.Fatalf("external.unmock_downstream event missing: %+v", events)
	}

	// External readiness can be rolled back to pending.
	mustCLI(t, f.root, "node", "update", ext, "--status", "pending")
	dag, _ = f.store.LoadDAG()
	if dag.Node(ext).Status != model.StatusPending {
		t.Fatalf("external node not reset to pending: %+v", dag.Node(ext))
	}
}

func TestExternalDoneWithoutUnmockHintsMockedEdges(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "node", "add", "--title", "商品侧", "--scope", "collaborator", "--prd-ref", "p:3-2", "--json")
	ext := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	mustCLI(t, f.root, "edge", "add", "--from", ext, "--to", "rb-b", "--kind", "mocked", "--note", "商品接口待联调")

	out = mustCLI(t, f.root, "done", ext)
	for _, want := range []string{"MOCKED", "商品接口待联调", "--unmock-downstream"} {
		if !strings.Contains(out, want) {
			t.Fatalf("done output missing %q:\n%s", want, out)
		}
	}
	dag, _ := f.store.LoadDAG()
	if dag.Node(ext).Status != model.StatusDone {
		t.Fatalf("external node not done: %+v", dag.Node(ext))
	}
	if dag.Edge(edgeIDFor(t, f.root, ext, "rb-b")).Kind != model.EdgeMocked {
		t.Fatal("mocked edge should be preserved without --unmock-downstream")
	}

	// A late `done --unmock-downstream` still flips the remaining mocked edges.
	out = mustCLI(t, f.root, "done", ext, "--unmock-downstream", "--json")
	late := jsonMap(t, out)
	if late["already_done"] != true || len(late["unmocked_edges"].([]interface{})) != 1 {
		t.Fatalf("late unmock = %v", late)
	}
	dag, _ = f.store.LoadDAG()
	if dag.Edge(edgeIDFor(t, f.root, ext, "rb-b")).Kind != model.EdgeHard || !dag.Node("rb-b").Stale {
		t.Fatalf("late unmock did not flip edge/stale: %+v", dag.Node("rb-b"))
	}

	// node update --status done also works for external nodes and can unmock.
	out = mustCLI(t, f.root, "node", "add", "--title", "AuthService 2", "--scope", "external", "--prd-ref", "p:3-2", "--json")
	ext2 := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	mustCLI(t, f.root, "edge", "add", "--from", ext2, "--to", "rb-a", "--kind", "mocked")
	out = mustCLI(t, f.root, "node", "update", ext2, "--status", "done", "--unmock-downstream", "--json")
	m := jsonMap(t, out)
	if m["ok"] != true {
		t.Fatalf("node update output = %v", m)
	}
	if len(m["unmocked_edges"].([]interface{})) != 1 {
		t.Fatalf("node update unmocked_edges = %v", m["unmocked_edges"])
	}
	dag, _ = f.store.LoadDAG()
	if dag.Node(ext2).Status != model.StatusDone || dag.Edge(edgeIDFor(t, f.root, ext2, "rb-a")).Kind != model.EdgeHard {
		t.Fatalf("node update external done state = %+v", dag.Node(ext2))
	}
	if !dag.Node("rb-a").Stale {
		t.Fatal("node update --unmock-downstream should mark downstream stale")
	}

	// Mine nodes still require the full done gates.
	if _, errOut, code := cli(t, f.root, "node", "update", "rb-a", "--status", "done"); code == 0 || !strings.Contains(errOut, "reqboard done") {
		t.Fatalf("mine node status=done not blocked: %s", errOut)
	}
	if _, errOut, code := cli(t, f.root, "done", "rb-a", "--unmock-downstream"); code == 0 || !strings.Contains(errOut, "仅适用于") {
		t.Fatalf("mine --unmock-downstream not rejected: %s", errOut)
	}
}

func TestMockAddResolve(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "mock", "add", "rb-a", "--desc", "库存服务先 mock", "--json")
	mockID := jsonMap(t, out)["mock"].(map[string]interface{})["id"].(string)
	dag, _ := f.store.LoadDAG()
	if dag.Node("rb-a").Status != model.StatusMocked || len(dag.Node("rb-a").PendingMocks()) != 1 {
		t.Fatalf("mock add state = %+v", dag.Node("rb-a"))
	}
	mustCLI(t, f.root, "mock", "resolve", "rb-a", "--mock", mockID)
	dag, _ = f.store.LoadDAG()
	if dag.Node("rb-a").Status != model.StatusImplemented || len(dag.Node("rb-a").PendingMocks()) != 0 {
		t.Fatalf("mock resolve state = %+v", dag.Node("rb-a"))
	}
}

func TestPRDSyncCascade(t *testing.T) {
	f := setupCLI(t)
	newMD := strings.Replace(prdMarkdown, "- 用户可创建订单。", "- 用户可创建订单（需实名）。", 1) +
		"\n## 4 新增\n\n这是新增未覆盖段落。\n"
	testutil.Write(t, filepath.Join(f.root, "prd_new.md"), newMD)
	out := mustCLI(t, f.root, "prd", "sync", filepath.Join(f.root, "prd_new.md"), "--json")
	m := jsonMap(t, out)
	if len(m["modified"].([]interface{})) != 1 {
		t.Fatalf("modified = %v", m["modified"])
	}
	if len(m["added"].([]interface{})) == 0 {
		t.Fatalf("added = %v", m["added"])
	}
	dag, _ := f.store.LoadDAG()
	if !dag.Node("rb-a").Stale {
		t.Fatalf("rb-a not stale: %+v", dag.Node("rb-a"))
	}
	if !dag.Node("rb-b").Stale {
		t.Fatalf("downstream rb-b not stale: %+v", dag.Node("rb-b"))
	}
	status := jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	cov := status["prd"].(map[string]interface{})["coverage"].(map[string]interface{})
	if cov["unmapped"].(float64) == 0 {
		t.Fatalf("expected unmapped PRD: %v", cov)
	}
	// Ignoring the new paragraph clears Unmapped.
	unmapped := cov["unmapped_anchors"].([]interface{})
	mustCLI(t, f.root, "prd", "ignore", unmapped[0].(string))
	status = jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	cov = status["prd"].(map[string]interface{})["coverage"].(map[string]interface{})
	if cov["unmapped"].(float64) != 0 || cov["ignored"].(float64) != 1 {
		t.Fatalf("ignore failed: %v", cov)
	}
}

func TestDoneGatesAndForceHuman(t *testing.T) {
	f := setupCLI(t)

	// Happy path.
	mustCLI(t, f.root, "claim", "rb-a", "--session", "s-1")
	mustCLI(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/order.go", "--symbol", "CreateOrder", "--why", "PRD 1", "--session", "s-1")
	testutil.Write(t, filepath.Join(f.repoPath, "svc/order.go"), "package svc\n\nfunc CreateOrder() string {\n\treturn \"new\"\n}\n\nfunc CancelOrder() string {\n\treturn \"cancel\"\n}\n")
	mustCLI(t, f.root, "node", "review", "rb-a", "--pass", "--actor", "human")
	out := mustCLI(t, f.root, "done", "rb-a", "--evidence", "test:TC-1", "--json")
	if jsonMap(t, out)["status"] != "done" {
		t.Fatalf("done failed: %s", out)
	}
	dag, _ := f.store.LoadDAG()
	if dag.Node("rb-a").Status != model.StatusDone || len(dag.Node("rb-a").Evidence.TestCases) != 1 {
		t.Fatalf("done state = %+v", dag.Node("rb-a"))
	}

	// Gate 1: upstream not done (fresh workspace, rb-a still pending).
	f1 := setupCLI(t)
	mustCLI(t, f1.root, "node", "review", "rb-b", "--pass", "--actor", "human")
	_, errOut, code := cli(t, f1.root, "done", "rb-b")
	if code == 0 || !strings.Contains(errOut, "G1") {
		t.Fatalf("gate1 not enforced: %s", errOut)
	}

	// Gate 2: mocked in-edge.
	f2 := setupCLI(t)
	mustCLI(t, f2.root, "edge", "update", edgeIDFor(t, f2.root, "rb-a", "rb-b"), "--kind", "mocked", "--note", "AuthService")
	mustCLI(t, f2.root, "node", "review", "rb-b", "--pass", "--actor", "human")
	_, errOut, code = cli(t, f2.root, "done", "rb-b")
	if code == 0 || !strings.Contains(errOut, "mocked_edge") {
		t.Fatalf("gate2 not enforced: %s", errOut)
	}

	// Gate 3: stale.
	f3 := setupCLI(t)
	mustCLI(t, f3.root, "node", "update", "rb-b", "--stale", "true", "--reason", "PRD 改了")
	mustCLI(t, f3.root, "node", "review", "rb-b", "--pass", "--actor", "human")
	_, errOut, code = cli(t, f3.root, "done", "rb-b")
	if code == 0 || !strings.Contains(errOut, "stale_node") {
		t.Fatalf("gate3 not enforced: %s", errOut)
	}

	// Gate 4: orphan in node scope.
	f4 := setupCLI(t)
	mustCLI(t, f4.root, "claim", "rb-b", "--session", "s-2", "--lease", "api:svc/**")
	testutil.Write(t, filepath.Join(f4.repoPath, "svc/sneaky.go"), "package svc\n\nfunc Sneaky() string {\n\treturn \"x\"\n}\n")
	mustCLI(t, f4.root, "node", "review", "rb-b", "--pass", "--actor", "human")
	_, errOut, code = cli(t, f4.root, "done", "rb-b")
	if code == 0 || !strings.Contains(errOut, "orphan_diff") {
		t.Fatalf("gate4 not enforced: %s", errOut)
	}

	// force-human requires a human actor.
	if _, errOut, code = cli(t, f.root, "done", "rb-b", "--force-human"); code == 0 || !strings.Contains(errOut, "只允许人类") {
		t.Fatalf("agent force-human not blocked: %s", errOut)
	}
	mustCLI(t, f.root, "done", "rb-b", "--force-human", "--actor", "human")
	dag, _ = f.store.LoadDAG()
	if dag.Node("rb-b").Status != model.StatusDone {
		t.Fatalf("force-human failed: %+v", dag.Node("rb-b"))
	}
}

func edgeIDFor(t *testing.T, root, from, to string) string {
	t.Helper()
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.LoadDAG()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range d.Edges {
		if e.From == from && e.To == to {
			return e.ID
		}
	}
	t.Fatalf("edge %s->%s not found", from, to)
	return ""
}

func TestDispatchQuiescent(t *testing.T) {
	f := setupCLI(t)
	mustCLI(t, f.root, "node", "update", "rb-a", "--hold", "true")
	mustCLI(t, f.root, "node", "update", "rb-b", "--hold", "true")
	mustCLI(t, f.root, "node", "add", "--title", "外部", "--scope", "external", "--prd-ref", "p:3-2")
	mustCLI(t, f.root, "propose", "--from", "rb-a", "--prd-ref", "p:3-2", "--title", "待批", "--reason", "x")
	out := mustCLI(t, f.root, "dispatch", "--json")
	m := jsonMap(t, out)
	if m["status"] != "QUIESCENT" {
		t.Fatalf("status = %v, items=%v", m["status"], m["items"])
	}
	blocked := m["blocked"].(map[string]interface{})
	for _, k := range []string{"waiting_approval", "waiting_review", "waiting_external"} {
		if len(blocked[k].([]interface{})) == 0 {
			t.Fatalf("blocked.%s empty: %v", k, blocked)
		}
	}
}

func TestDispatchExternalHardVsMocked(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "node", "add", "--title", "AuthService 接口", "--scope", "external", "--prd-ref", "p:3-2", "--json")
	extHard := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	out = mustCLI(t, f.root, "node", "add", "--title", "商品侧", "--scope", "collaborator", "--prd-ref", "p:3-2", "--json")
	extMocked := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	mustCLI(t, f.root, "edge", "add", "--from", extHard, "--to", "rb-a", "--kind", "hard")
	mustCLI(t, f.root, "edge", "add", "--from", extMocked, "--to", "rb-b", "--kind", "mocked", "--note", "先 mock")

	out = mustCLI(t, f.root, "dispatch", "--max-parallel", "4", "--json")
	m := jsonMap(t, out)
	itemIDs := map[string]bool{}
	for _, raw := range m["items"].([]interface{}) {
		itemIDs[raw.(map[string]interface{})["node_id"].(string)] = true
	}
	if itemIDs["rb-a"] {
		t.Fatalf("external --hard--> mine entered P1: %v", m["items"])
	}
	if !itemIDs["rb-b"] {
		t.Fatalf("external --mocked--> mine should stay in P1: %v", m["items"])
	}
	blocked := m["blocked"].(map[string]interface{})
	var reason string
	for _, raw := range blocked["waiting_external"].([]interface{}) {
		b := raw.(map[string]interface{})
		if b["node_id"] == "rb-a" {
			reason = b["reason"].(string)
		}
	}
	if !strings.Contains(reason, "等待外部节点 "+extHard) || !strings.Contains(reason, "改为 mocked") {
		t.Fatalf("rb-a waiting_external reason = %q", reason)
	}
}

func TestNodeLifecycle(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "node", "add", "--title", "新节点", "--prd-ref", "p:3-2", "--json")
	id := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)

	mustCLI(t, f.root, "node", "update", id, "--status", "implemented")
	mustCLI(t, f.root, "node", "review", id, "--request-changes", "--note", "改成排除语义", "--target", "api:svc/order.go:CreateOrder")
	dag, _ := f.store.LoadDAG()
	n := dag.Node(id)
	if n.Status != model.StatusChangesRequested || len(n.OpenNotes()) != 1 {
		t.Fatalf("review changes = %+v", n)
	}
	mustCLI(t, f.root, "node", "review", id, "--pass")
	dag, _ = f.store.LoadDAG()
	n = dag.Node(id)
	if n.Status != model.StatusReviewed || len(n.OpenNotes()) != 0 {
		t.Fatalf("review pass = %+v", n)
	}

	mustCLI(t, f.root, "node", "fail", id, "--error", "env broken")
	mustCLI(t, f.root, "node", "fail", id, "--error", "env broken")
	dag, _ = f.store.LoadDAG()
	if dag.Node(id).Attempts != 2 {
		t.Fatalf("attempts = %+v", dag.Node(id))
	}
	mustCLI(t, f.root, "node", "update", id, "--reset-attempts")
	dag, _ = f.store.LoadDAG()
	if dag.Node(id).Attempts != 0 {
		t.Fatalf("reset attempts = %+v", dag.Node(id))
	}

	mustCLI(t, f.root, "bind", id, "--repo", "api", "--file", "svc/order.go", "--symbol", "CancelOrder", "--why", "x")
	mustCLI(t, f.root, "node", "deprecate", id, "--reason", "砍需求")
	b, _ := f.store.LoadBindings()
	if len(b.BindingsForNode(id)) != 1 {
		t.Fatalf("deprecate dropped bindings: %+v", b.Bindings)
	}

	// approve / reject
	out = mustCLI(t, f.root, "propose", "--from", "rb-a", "--prd-ref", "p:3-2", "--title", "待批1", "--reason", "x", "--json")
	p1 := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	out = mustCLI(t, f.root, "propose", "--from", "rb-a", "--prd-ref", "p:3-2", "--title", "待批2", "--reason", "x", "--json")
	p2 := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)
	mustCLI(t, f.root, "node", "approve", p1)
	mustCLI(t, f.root, "node", "reject", p2)
	dag, _ = f.store.LoadDAG()
	if dag.Node(p1).Status != model.StatusPending || dag.Node(p2).Status != model.StatusDeprecated {
		t.Fatalf("approve/reject = %+v %+v", dag.Node(p1), dag.Node(p2))
	}
}

func TestCheckExitCodeAndCorrections(t *testing.T) {
	f := setupCLI(t)
	testutil.Write(t, filepath.Join(f.repoPath, "svc/orphan.go"), "package svc\n\nfunc Orphan() string {\n\treturn \"x\"\n}\n")
	out, errOut, code := cli(t, f.root, "check", "--all", "--json")
	if code == 0 {
		t.Fatalf("check should exit non-zero: %s %s", out, errOut)
	}
	m := jsonMap(t, out)
	if m["violations"].(float64) != 1 {
		t.Fatalf("violations = %v", m["violations"])
	}
	buckets := m["buckets"].(map[string]interface{})
	orphan := buckets["orphan"].([]interface{})[0].(map[string]interface{})
	if !strings.Contains(orphan["correction"].(string), "reqboard propose") {
		t.Fatalf("correction missing: %v", orphan)
	}
	// Non-JSON output still prints the rule-injection report to stdout.
	out, _, code = cli(t, f.root, "check", "--all")
	if code == 0 || !strings.Contains(out, "Orphan Diff") || !strings.Contains(out, "REQBOARD BLOCKED") {
		t.Fatalf("text check output = %s", out)
	}
}

func TestReviewPack(t *testing.T) {
	f := setupCLI(t)
	mustCLI(t, f.root, "claim", "rb-a", "--session", "s-1")
	mustCLI(t, f.root, "bind", "rb-a", "--repo", "api", "--file", "svc/order.go", "--symbol", "CreateOrder", "--why", "PRD 1", "--session", "s-1")
	testutil.Write(t, filepath.Join(f.repoPath, "svc/order.go"), "package svc\n\nfunc CreateOrder() string {\n\treturn \"new\"\n}\n\nfunc CancelOrder() string {\n\treturn \"cancel\"\n}\n")
	out := mustCLI(t, f.root, "review-pack", "rb-a", "--json")
	m := jsonMap(t, out)
	if len(m["prd_blocks"].([]interface{})) != 2 {
		t.Fatalf("prd blocks = %v", m["prd_blocks"])
	}
	diffs := m["diffs"].([]interface{})
	if len(diffs) != 1 {
		t.Fatalf("diffs = %v", diffs)
	}
	hunks := diffs[0].(map[string]interface{})["hunks"].([]interface{})
	if len(hunks) == 0 {
		t.Fatalf("no hunks in review pack: %v", diffs[0])
	}
}

func TestPlanningOnlyInitAndRepoAdd(t *testing.T) {
	root := t.TempDir()
	out := mustCLI(t, root, "init", "--name", "plan-only", "--json")
	m := jsonMap(t, out)
	if m["planning_only"] != true {
		t.Fatalf("planning_only = %v, want true", m["planning_only"])
	}
	if repos := m["repos"].([]interface{}); len(repos) != 0 {
		t.Fatalf("repos = %v, want empty", repos)
	}

	// status/check must work before any code repo is mounted.
	out = mustCLI(t, root, "status", "--json")
	if jsonMap(t, out)["ok"] != true {
		t.Fatalf("status failed: %s", out)
	}
	mustCLI(t, root, "check", "--all", "--json")

	testutil.NewRepo(t, root, "api", map[string]string{"svc/order.go": "package svc\n"})
	out = mustCLI(t, root, "repo", "add", "--name", "api", "--path", "api", "--base", "master", "--json")
	repo := jsonMap(t, out)["repo"].(map[string]interface{})
	if repo["name"] != "api" || repo["base_ref"] != "master" {
		t.Fatalf("repo = %v", repo)
	}
	out = mustCLI(t, root, "repo", "list", "--json")
	if len(jsonMap(t, out)["repos"].([]interface{})) != 1 {
		t.Fatalf("repo list = %s", out)
	}
	mustCLI(t, root, "repo", "remove", "api")
	out = mustCLI(t, root, "repo", "list", "--json")
	if len(jsonMap(t, out)["repos"].([]interface{})) != 0 {
		t.Fatalf("repo not removed: %s", out)
	}
}

func TestRepoAddRejectsNonGitDir(t *testing.T) {
	root := t.TempDir()
	mustCLI(t, root, "init", "--name", "plan-only")
	if err := os.MkdirAll(filepath.Join(root, "not-git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := cli(t, root, "repo", "add", "--name", "not-git", "--path", "not-git")
	if code == 0 || !strings.Contains(errOut, "not a git repository") {
		t.Fatalf("non-git repo not rejected: code=%d err=%s", code, errOut)
	}
}

func TestPRDIgnoreBatch(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "prd", "ignore", "p:1-2", "p:1-3", "--json")
	if len(jsonMap(t, out)["anchors"].([]interface{})) != 2 {
		t.Fatalf("ignore output = %s", out)
	}
	dag, _ := f.store.LoadDAG()
	if len(dag.IgnoredPRDRefs) != 2 {
		t.Fatalf("ignored = %v", dag.IgnoredPRDRefs)
	}
	mustCLI(t, f.root, "prd", "unignore", "p:1-2", "p:1-3")
	dag, _ = f.store.LoadDAG()
	if len(dag.IgnoredPRDRefs) != 0 {
		t.Fatalf("unignore left = %v", dag.IgnoredPRDRefs)
	}
}

func TestTodoCommandLifecycle(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "todo", "add", "DDL: orders add confirmation_mode",
		"--kind", "ddl", "--blocks", "rb-a,rb-b", "--note", "上线前执行", "--json")
	m := jsonMap(t, out)
	node := m["node"].(map[string]interface{})
	todoID := node["id"].(string)
	if node["scope"] != model.ScopeTodo || node["status"] != model.StatusPending {
		t.Fatalf("todo node = %+v", node)
	}
	tags := node["tags"].([]interface{})
	if len(tags) != 1 || tags[0] != "ddl" {
		t.Fatalf("todo tags = %v, want [ddl]", tags)
	}
	edges := m["edges"].([]interface{})
	if len(edges) != 2 {
		t.Fatalf("edges = %v, want 2", edges)
	}
	dag, _ := f.store.LoadDAG()
	if len(dag.OutEdges(todoID)) != 2 {
		t.Fatalf("todo out edges = %+v", dag.OutEdges(todoID))
	}
	for _, e := range dag.OutEdges(todoID) {
		if e.Kind != model.EdgeHard {
			t.Fatalf("todo edge kind = %s, want hard", e.Kind)
		}
	}

	out = mustCLI(t, f.root, "todo", "list", "--json")
	list := jsonMap(t, out)
	if list["total"].(float64) != 1 || list["done"].(float64) != 0 {
		t.Fatalf("todo list = %v", list)
	}
	todo := list["todos"].([]interface{})[0].(map[string]interface{})
	if todo["kind"] != "ddl" || todo["done"] != false {
		t.Fatalf("todo list item = %+v", todo)
	}
	if len(todo["blocks"].([]interface{})) != 2 {
		t.Fatalf("todo blocks = %+v", todo["blocks"])
	}

	// TODO nodes are not business coding nodes: claim/bind must be rejected.
	if _, errOut, code := cli(t, f.root, "claim", todoID); code == 0 || !strings.Contains(errOut, "todo") {
		t.Fatalf("todo claim not rejected: %s", errOut)
	}
	if _, errOut, code := cli(t, f.root, "bind", todoID, "--repo", "api", "--file", "svc/order.go", "--symbol", "CreateOrder", "--why", "x"); code == 0 || !strings.Contains(errOut, "todo") {
		t.Fatalf("todo bind not rejected: %s", errOut)
	}

	// Default todo --hard--> mine does not block P1 start; the TODO itself is
	// listed in waiting_todo instead of the coding queue.
	out = mustCLI(t, f.root, "dispatch", "--max-parallel", "4", "--json")
	plan := jsonMap(t, out)
	items := map[string]bool{}
	for _, raw := range plan["items"].([]interface{}) {
		items[raw.(map[string]interface{})["node_id"].(string)] = true
	}
	if !items["rb-a"] || !items["rb-b"] {
		t.Fatalf("default todo must not block P1 start: %v", plan["items"])
	}
	if items[todoID] {
		t.Fatalf("todo node entered P1 queue: %v", plan["items"])
	}
	blocked := plan["blocked"].(map[string]interface{})
	foundTodo := false
	for _, raw := range blocked["waiting_todo"].([]interface{}) {
		if raw.(map[string]interface{})["node_id"] == todoID {
			foundTodo = true
		}
	}
	if !foundTodo {
		t.Fatalf("todo not listed in waiting_todo: %v", blocked)
	}

	// Gate 1 blocks downstream completion until the TODO is done.
	_, errOut, code := cli(t, f.root, "done", "rb-a")
	if code == 0 || !strings.Contains(errOut, "前置 TODO") || !strings.Contains(errOut, todoID) {
		t.Fatalf("Gate 1 todo block missing: %s", errOut)
	}

	// done <todo> skips all code gates and unlocks the blocked downstream.
	out = mustCLI(t, f.root, "done", todoID, "--json")
	done := jsonMap(t, out)
	if done["status"] != model.StatusDone || done["todo"] != true || done["prerequisite"] != true {
		t.Fatalf("todo done = %v", done)
	}
	unlocked := done["unlocked"].([]interface{})
	if len(unlocked) != 1 || unlocked[0] != "rb-a" {
		t.Fatalf("todo unlocked = %v, want [rb-a]", unlocked)
	}
	mustCLI(t, f.root, "done", "rb-a", "--json")

	out = mustCLI(t, f.root, "todo", "list", "--json")
	list = jsonMap(t, out)
	if list["done"].(float64) != 1 {
		t.Fatalf("todo list done = %v", list["done"])
	}
}

func TestTodoNodeUpdateStatusDoneSkipsGates(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "todo", "add", "Gateway: allow visibility query",
		"--kind", "gateway", "--blocks", "rb-a", "--json")
	todoID := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)

	out = mustCLI(t, f.root, "node", "update", todoID, "--status", "done", "--json")
	m := jsonMap(t, out)
	if m["prerequisite"] != true || m["todo"] != true {
		t.Fatalf("node update result = %v", m)
	}
	node := m["node"].(map[string]interface{})
	if node["status"] != model.StatusDone {
		t.Fatalf("todo status = %v", node["status"])
	}
	unlocked := m["unlocked"].([]interface{})
	if len(unlocked) != 1 || unlocked[0] != "rb-a" {
		t.Fatalf("unlocked = %v", unlocked)
	}
	dag, _ := f.store.LoadDAG()
	if dag.Node(todoID).Status != model.StatusDone {
		t.Fatalf("todo not persisted as done: %+v", dag.Node(todoID))
	}
}

func TestTodoBlockStartTagBlocksDispatch(t *testing.T) {
	f := setupCLI(t)
	out := mustCLI(t, f.root, "todo", "add", "DDL: orders add index",
		"--kind", "ddl", "--blocks", "rb-a", "--tag", "block-start", "--json")
	todoID := jsonMap(t, out)["node"].(map[string]interface{})["id"].(string)

	out = mustCLI(t, f.root, "dispatch", "--max-parallel", "4", "--json")
	plan := jsonMap(t, out)
	for _, raw := range plan["items"].([]interface{}) {
		if raw.(map[string]interface{})["node_id"] == "rb-a" {
			t.Fatalf("block-start todo must keep rb-a out of P1: %v", plan["items"])
		}
	}
	blocked := plan["blocked"].(map[string]interface{})
	reason := ""
	for _, raw := range blocked["waiting_todo"].([]interface{}) {
		b := raw.(map[string]interface{})
		if b["node_id"] == "rb-a" {
			reason = b["reason"].(string)
		}
	}
	if !strings.Contains(reason, "前置 TODO") || !strings.Contains(reason, todoID) || !strings.Contains(reason, "block-start") {
		t.Fatalf("rb-a waiting_todo reason = %q", reason)
	}
}

func TestTodoRejectsUnknownOrNonMineBlocks(t *testing.T) {
	f := setupCLI(t)
	if _, errOut, code := cli(t, f.root, "todo", "add", "DDL: x", "--kind", "ddl", "--blocks", "rb-ghost"); code == 0 || !strings.Contains(errOut, "unknown node") {
		t.Fatalf("unknown blocks target not rejected: %s", errOut)
	}
	if _, errOut, code := cli(t, f.root, "todo", "add", "DDL: x", "--kind", "ddl", "--blocks", "rb-c"); code == 0 || !strings.Contains(errOut, "scope=mine") {
		t.Fatalf("out_of_scope blocks target not rejected: %s", errOut)
	}
}

func TestHumanReviewNoteRequiresHumanPass(t *testing.T) {
	f := setupCLI(t)
	mustCLI(t, f.root, "node", "review", "rb-a", "--request-changes", "--note", "缺少幂等校验", "--actor", "human")

	// Agent must not self-approve a human-authored review note.
	_, errOut, code := cli(t, f.root, "node", "review", "rb-a", "--pass")
	if code == 0 || !strings.Contains(errOut, "只能由人类审核通过") {
		t.Fatalf("agent should be blocked from passing human review note: code=%d err=%s", code, errOut)
	}
	dag, _ := f.store.LoadDAG()
	if dag.Node("rb-a").Status != model.StatusChangesRequested || len(dag.Node("rb-a").OpenNotes()) != 1 {
		t.Fatalf("human note should remain open: %+v", dag.Node("rb-a"))
	}

	// Human can pass and resolve the open note.
	mustCLI(t, f.root, "node", "review", "rb-a", "--pass", "--actor", "human")
	dag, _ = f.store.LoadDAG()
	if dag.Node("rb-a").Status != model.StatusReviewed || len(dag.Node("rb-a").OpenNotes()) != 0 {
		t.Fatalf("human pass failed: %+v", dag.Node("rb-a"))
	}

	// Non-human review notes (e.g., independent critic agent) can still be passed by agent.
	mustCLI(t, f.root, "node", "review", "rb-a", "--request-changes", "--note", "自动巡检建议补边界单测", "--actor", "agent")
	mustCLI(t, f.root, "node", "review", "rb-a", "--pass", "--actor", "agent")
	dag, _ = f.store.LoadDAG()
	if dag.Node("rb-a").Status != model.StatusReviewed || len(dag.Node("rb-a").OpenNotes()) != 0 {
		t.Fatalf("agent pass on agent-authored note failed: %+v", dag.Node("rb-a"))
	}
}
