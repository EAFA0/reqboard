package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reqboard/internal/testutil"
)

const sourceMarkdown = `# 评审补充

## 1 口径确认

- 线下单在支付成功后即可见。
- 其余口径不变。
`

func addSourceFixture(t *testing.T, f *fixture) {
	t.Helper()
	p := filepath.Join(f.root, "meeting.md")
	testutil.Write(t, p, sourceMarkdown)
	mustCLI(t, f.root, "source", "add", p, "--id", "meeting-review",
		"--title", "评审补充口径", "--kind", "meeting", "--url", "https://example.com/review")
}

func TestSourceAddListAndStatus(t *testing.T) {
	f := setupCLI(t)
	addSourceFixture(t, f)

	raw, err := f.store.ReadFile("sources/meeting-review.md")
	if err != nil {
		t.Fatalf("source file missing: %v", err)
	}
	if !strings.Contains(string(raw), "<!-- @meeting-review:1-2 -->") {
		t.Fatalf("source anchors not namespaced:\n%s", raw)
	}
	dag, _ := f.store.LoadDAG()
	src := dag.Source("meeting-review")
	if src == nil || src.Title != "评审补充口径" || src.Kind != "meeting" || src.File != "sources/meeting-review.md" {
		t.Fatalf("source not registered: %+v", src)
	}

	out := mustCLI(t, f.root, "source", "list", "--json")
	list := jsonMap(t, out)["sources"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("sources = %v", list)
	}
	view := list[0].(map[string]interface{})
	if view["id"] != "meeting-review" || view["blocks"].(float64) != 4 {
		t.Fatalf("source list view = %v", view)
	}

	status := jsonMap(t, mustCLI(t, f.root, "status", "--json"))
	sources := status["sources"].([]interface{})
	if len(sources) != 1 {
		t.Fatalf("status sources = %v", sources)
	}
	srcView := sources[0].(map[string]interface{})
	if srcView["kind"] != "meeting" || srcView["url"] != "https://example.com/review" {
		t.Fatalf("status source view = %v", srcView)
	}
	blocks := srcView["blocks"].([]interface{})
	if len(blocks) != 4 {
		t.Fatalf("status source blocks = %d", len(blocks))
	}
	first := blocks[0].(map[string]interface{})
	for _, key := range []string{"anchor", "kind", "level", "text", "node_ids"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("block missing %q: %v", key, first)
		}
	}
	// Supplemental sources never inflate the main-PRD coverage accounting.
	cov := status["prd"].(map[string]interface{})["coverage"].(map[string]interface{})
	if cov["percent"].(float64) != 100 || cov["unmapped"].(float64) != 0 {
		t.Fatalf("main PRD coverage polluted by source: %v", cov)
	}
}

func TestSourceAddValidationAndTextInput(t *testing.T) {
	f := setupCLI(t)
	p := filepath.Join(f.root, "meeting.md")
	testutil.Write(t, p, sourceMarkdown)

	if _, errOut, code := cli(t, f.root, "source", "add", p, "--id", "p"); code == 0 || !strings.Contains(errOut, "reserved") {
		t.Fatalf("id p not rejected: %s", errOut)
	}
	if _, _, code := cli(t, f.root, "source", "add", p, "--id", "bad id"); code == 0 {
		t.Fatal("invalid slug not rejected")
	}
	if _, _, code := cli(t, f.root, "source", "add", p, "--id", "ok", "--kind", "nope"); code == 0 {
		t.Fatal("invalid kind not rejected")
	}
	if _, _, code := cli(t, f.root, "source", "add", p, "--id", "ok", "--url", "file:///etc/passwd"); code == 0 {
		t.Fatal("non-http url not rejected")
	}
	addSourceFixture(t, f)
	if _, errOut, code := cli(t, f.root, "source", "add", p, "--id", "meeting-review"); code == 0 || !strings.Contains(errOut, "already exists") {
		t.Fatalf("duplicate source not rejected: %s", errOut)
	}

	// --text is the paste-from-clipboard path.
	out := mustCLI(t, f.root, "source", "add", "--text", "# 群聊拍板\n\n- 先做可见性。\n",
		"--id", "chat-review", "--kind", "chat", "--json")
	got := jsonMap(t, out)["source"].(map[string]interface{})
	if got["id"] != "chat-review" || got["title"] != "群聊拍板" {
		t.Fatalf("text source = %v", got)
	}
	// stdin via `-` is the paste-a-long-meeting-notes path.
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	_, _ = w.WriteString("# 会议纪要\n\n- stdin 来源。\n")
	_ = w.Close()
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	mustCLI(t, f.root, "source", "add", "-", "--id", "stdin-review", "--kind", "meeting")
	dag, _ := f.store.LoadDAG()
	if dag.Source("stdin-review") == nil {
		t.Fatal("stdin source not registered")
	}
}

func TestSourceAnchorClaimAndRefsInjection(t *testing.T) {
	f := setupCLI(t)
	addSourceFixture(t, f)
	mustCLI(t, f.root, "node", "update", "rb-a",
		"--prd-ref", "p:1-2", "--prd-ref", "p:1-3", "--prd-ref", "meeting-review:1-2")

	out := mustCLI(t, f.root, "claim", "rb-a", "--session", "s-1")
	for _, want := range []string{
		"[补充依据 — 与主 PRD 口径冲突时优先采用]",
		"线下单在支付成功后即可见",
		"评审补充口径",
		"@meeting-review:1-2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("claim missing %q:\n%s", want, out)
		}
	}

	refOut := mustCLI(t, f.root, "ref", "add", "rb-a", "--title", "评审拍板", "--kind", "decision",
		"--url", "https://example.com/decision", "--quote", "先按补充口径实现", "--json")
	ref := jsonMap(t, refOut)["ref"].(map[string]interface{})
	if ref["id"] != "rf-01" || ref["kind"] != "decision" || ref["quote"] != "先按补充口径实现" {
		t.Fatalf("ref = %v", ref)
	}
	out = mustCLI(t, f.root, "claim", "rb-a", "--session", "s-2")
	for _, want := range []string{"外部依据 / 拍板摘录（refs）", "[decision] 评审拍板", "https://example.com/decision", "原话: 先按补充口径实现"} {
		if !strings.Contains(out, want) {
			t.Fatalf("claim missing ref %q:\n%s", want, out)
		}
	}
	claimJSON := jsonMap(t, mustCLI(t, f.root, "claim", "rb-a", "--session", "s-3", "--json"))
	sourceBlocks := claimJSON["source_blocks"].([]interface{})
	if len(sourceBlocks) != 1 || sourceBlocks[0].(map[string]interface{})["source_id"] != "meeting-review" {
		t.Fatalf("claim source_blocks = %v", sourceBlocks)
	}
	if len(claimJSON["refs"].([]interface{})) != 1 {
		t.Fatalf("claim refs = %v", claimJSON["refs"])
	}

	mustCLI(t, f.root, "ref", "remove", "rb-a", "rf-01")
	dag, _ := f.store.LoadDAG()
	if len(dag.Node("rb-a").Refs) != 0 {
		t.Fatalf("ref remove left = %+v", dag.Node("rb-a").Refs)
	}
}

func TestPlanImportAcceptsSourceAnchorsAndRefsOnlyLeaf(t *testing.T) {
	f := setupCLI(t)
	addSourceFixture(t, f)
	planPath := filepath.Join(f.root, "plan_v3.json")
	testutil.Write(t, planPath, `{"nodes":[
 {"id":"rb-src","title":"补充口径节点","scope":"mine","prd_refs":["meeting-review:1-2"],"depends_on":["rb-a"]},
 {"id":"rb-ref","title":"仅拍板依据节点","scope":"mine","refs":[{"kind":"decision","title":"评审拍板","quote":"先按补充口径实现"}]}
]}`)
	mustCLI(t, f.root, "plan", "import", planPath)
	dag, _ := f.store.LoadDAG()
	if refs := dag.Node("rb-src").PRDRefs; len(refs) != 1 || refs[0] != "meeting-review:1-2" {
		t.Fatalf("source anchor refs = %v", refs)
	}
	refNode := dag.Node("rb-ref")
	if len(refNode.Refs) != 1 || refNode.Refs[0].ID == "" || refNode.Refs[0].Kind != "decision" {
		t.Fatalf("refs-only leaf = %+v", refNode)
	}

	badPath := filepath.Join(f.root, "plan_bad.json")
	testutil.Write(t, badPath, `{"nodes":[{"id":"rb-ghost","title":"幽灵来源","scope":"mine","prd_refs":["ghost:1-2"]}]}`)
	if _, errOut, code := cli(t, f.root, "plan", "import", badPath); code == 0 || !strings.Contains(errOut, "ghost:1-2") {
		t.Fatalf("unregistered source anchor not rejected: %s", errOut)
	}
}

func TestSourceSyncCascadesStale(t *testing.T) {
	f := setupCLI(t)
	addSourceFixture(t, f)
	mustCLI(t, f.root, "node", "update", "rb-a",
		"--prd-ref", "p:1-2", "--prd-ref", "p:1-3", "--prd-ref", "meeting-review:1-2")
	newPath := filepath.Join(f.root, "meeting_v2.md")
	testutil.Write(t, newPath, strings.Replace(sourceMarkdown, "- 线下单在支付成功后即可见。", "- 线下单在支付成功且风控通过后即可见。", 1))
	out := mustCLI(t, f.root, "source", "sync", "meeting-review", newPath, "--json")
	m := jsonMap(t, out)
	if len(m["modified"].([]interface{})) != 1 {
		t.Fatalf("modified = %v", m["modified"])
	}
	stale := m["stale_nodes"].(map[string]interface{})
	if _, ok := stale["rb-a"]; !ok {
		t.Fatalf("referencing node not stale: %v", stale)
	}
	if _, ok := stale["rb-b"]; !ok {
		t.Fatalf("downstream node not stale: %v", stale)
	}
	dag, _ := f.store.LoadDAG()
	if !dag.Node("rb-a").Stale || !strings.Contains(dag.Node("rb-a").StaleReason, "meeting-review") {
		t.Fatalf("rb-a stale state = %+v", dag.Node("rb-a"))
	}
}

func TestSourceRemoveGuardsReferences(t *testing.T) {
	f := setupCLI(t)
	addSourceFixture(t, f)
	mustCLI(t, f.root, "node", "update", "rb-a",
		"--prd-ref", "p:1-2", "--prd-ref", "p:1-3", "--prd-ref", "meeting-review:1-2")
	if _, errOut, code := cli(t, f.root, "source", "remove", "meeting-review"); code == 0 || !strings.Contains(errOut, "--force") {
		t.Fatalf("referenced source not guarded: %s", errOut)
	}
	out := mustCLI(t, f.root, "source", "remove", "meeting-review", "--force", "--json")
	if jsonMap(t, out)["stripped_refs"].(float64) != 1 {
		t.Fatalf("stripped refs = %v", out)
	}
	dag, _ := f.store.LoadDAG()
	if dag.Source("meeting-review") != nil {
		t.Fatal("source still registered")
	}
	for _, r := range dag.Node("rb-a").PRDRefs {
		if strings.HasPrefix(r, "meeting-review:") {
			t.Fatalf("dangling source ref left: %s", r)
		}
	}
	if _, err := f.store.ReadFile("sources/meeting-review.md"); err == nil {
		t.Fatal("source file not removed")
	}
}
