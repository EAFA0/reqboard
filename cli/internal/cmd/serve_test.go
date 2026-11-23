package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reqboard/internal/model"
	"reqboard/internal/testutil"
)

func TestServeEndpointsAndDiffViewer(t *testing.T) {
	f := setupCLI(t)
	// Modify a tracked file and add an untracked file in the fixture git repo.
	testutil.Write(t, filepath.Join(f.repoPath, "svc/order.go"),
		"package svc\n\nfunc CreateOrder() string {\n\treturn \"v2\"\n}\n\nfunc CancelOrder() string {\n\treturn \"cancel\"\n}\n")
	testutil.Write(t, filepath.Join(f.repoPath, "svc/new_helper.go"),
		"package svc\n\nfunc Helper() int {\n\treturn 42\n}\n")

	srv := httptest.NewServer(newServeHandler(f.root, ""))
	defer srv.Close()

	// 1. Index HTML & embedded static assets.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "/assets/web-bridge.js") || !strings.Contains(string(body), "/assets/board.js") {
		t.Fatalf("unexpected index response: status=%d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/assets/board.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("assets/board.js status = %d", resp.StatusCode)
	}

	// 2. /api/status returns live snapshot with web capabilities.
	resp, err = http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	var snap map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&snap)
	resp.Body.Close()
	if snap["ok"] != true {
		t.Fatalf("status ok != true: %v", snap)
	}
	webMeta, _ := snap["web"].(map[string]interface{})
	if webMeta["mode"] != "web" {
		t.Fatalf("web meta missing: %v", webMeta)
	}

	// 3. /api/diff returns real unified hunks + focus_line for symbol.
	resp, err = http.Get(srv.URL + "/api/diff?repo=api&file=svc/order.go&symbol=CreateOrder&context=3")
	if err != nil {
		t.Fatal(err)
	}
	var diff map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&diff)
	resp.Body.Close()
	if resp.StatusCode != 200 || diff["ok"] != true {
		t.Fatalf("diff failed: status=%d body=%v", resp.StatusCode, diff)
	}
	if int(diff["focus_line"].(float64)) != 3 {
		t.Fatalf("focus_line = %v, want 3", diff["focus_line"])
	}
	if int(diff["added"].(float64)) != 1 || int(diff["deleted"].(float64)) != 1 {
		t.Fatalf("diff stat = +%v/-%v, want +1/-1", diff["added"], diff["deleted"])
	}
	hunks := diff["hunks"].([]interface{})
	if len(hunks) != 1 {
		t.Fatalf("hunks len = %d, want 1", len(hunks))
	}

	// 4. /api/diff handles untracked files cleanly.
	resp, err = http.Get(srv.URL + "/api/diff?repo=api&file=svc/new_helper.go&symbol=Helper")
	if err != nil {
		t.Fatal(err)
	}
	diff = map[string]interface{}{}
	_ = json.NewDecoder(resp.Body).Decode(&diff)
	resp.Body.Close()
	if diff["status"] != "untracked" || int(diff["added"].(float64)) != 5 {
		t.Fatalf("untracked diff = %v", diff)
	}

	// 5. /api/diff rejects path traversal outside repo.
	resp, err = http.Get(srv.URL + "/api/diff?repo=api&file=../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("path traversal status = %d, want 400", resp.StatusCode)
	}

	// 6. /api/action creates a node and then undoes it cleanly.
	postAction := func(payload map[string]interface{}) map[string]interface{} {
		raw, _ := json.Marshal(payload)
		r, perr := http.Post(srv.URL+"/api/action", "application/json", bytes.NewReader(raw))
		if perr != nil {
			t.Fatal(perr)
		}
		defer r.Body.Close()
		var out map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&out)
		if r.StatusCode != 200 || out["ok"] != true {
			t.Fatalf("action %v failed: status=%d out=%v", payload["type"], r.StatusCode, out)
		}
		return out
	}

	res := postAction(map[string]interface{}{"type": "node.create", "title": "Web 新建节点", "x": 120, "y": 240})
	dag := res["snapshot"].(map[string]interface{})["dag"].(map[string]interface{})
	nodes := dag["nodes"].([]interface{})
	if len(nodes) != 4 {
		t.Fatalf("nodes after create = %d, want 4", len(nodes))
	}

	res = postAction(map[string]interface{}{"type": "undo"})
	dag = res["snapshot"].(map[string]interface{})["dag"].(map[string]interface{})
	if len(dag["nodes"].([]interface{})) != 3 {
		t.Fatalf("nodes after undo = %d, want 3", len(dag["nodes"].([]interface{})))
	}
}

func TestWebUIAssetsInSyncWithVSCodeExt(t *testing.T) {
	for _, name := range []string{"board.js", "board.css"} {
		cliAsset := filepath.Join("..", "webui", "assets", name)
		extAsset := filepath.Join("..", "..", "..", "vscode-ext", "media", name)
		b1, err := os.ReadFile(cliAsset)
		if err != nil {
			t.Fatalf("read %s: %v", cliAsset, err)
		}
		b2, err := os.ReadFile(extAsset)
		if err != nil {
			t.Fatalf("read %s: %v", extAsset, err)
		}
		if !bytes.Equal(b1, b2) {
			t.Fatalf("%s out of sync between cli/internal/webui/assets and vscode-ext/media; run scripts/sync-ui.sh", name)
		}
	}
}

func TestSubcommandHelpAndNodeListAndDoneGuard(t *testing.T) {
	f := setupCLI(t)

	// Subcommand --help prints specific flags rather than falling through to errors.
	out, _, code := cli(t, f.root, "node", "update", "--help")
	if code != 0 || !strings.Contains(out, "--status") || !strings.Contains(out, "--stale") {
		t.Fatalf("node update --help failed: code=%d out=%s", code, out)
	}
	out, _, code = cli(t, f.root, "serve", "--help")
	if code != 0 || !strings.Contains(out, "--open-cmd") {
		t.Fatalf("serve --help failed: code=%d out=%s", code, out)
	}

	// node list filters cleanly by scope.
	out = mustCLI(t, f.root, "node", "list", "--scope", "mine", "--json")
	m := jsonMap(t, out)
	if int(m["count"].(float64)) != 2 {
		t.Fatalf("node list --scope mine count = %v, want 2", m["count"])
	}

	// Agent cannot reopen an already-done node via `node update --status`.
	mustCLI(t, f.root, "done", "rb-a", "--force-human", "--actor", "human")
	_, errOut, code := cli(t, f.root, "node", "update", "rb-a", "--status", "implemented")
	if code == 0 || !strings.Contains(errOut, "已 done") {
		t.Fatalf("agent should not downgrade done node: code=%d err=%s", code, errOut)
	}
	dag, _ := f.store.LoadDAG()
	if dag.Node("rb-a").Status != model.StatusDone {
		t.Fatalf("rb-a status changed unexpectedly: %s", dag.Node("rb-a").Status)
	}

	// Human can still reopen explicitly.
	mustCLI(t, f.root, "node", "update", "rb-a", "--status", "implemented", "--actor", "human")
	dag, _ = f.store.LoadDAG()
	if dag.Node("rb-a").Status != model.StatusImplemented {
		t.Fatalf("human reopen failed: %s", dag.Node("rb-a").Status)
	}
}
