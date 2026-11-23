package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDefinitionResultHandlesLocationAndLocationLink(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "order.go")
	if err := os.WriteFile(f, []byte("package demo\n\nfunc CreateOrder() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]map[string]interface{}{
		{
			"targetUri":            fileURI(f),
			"targetSelectionRange": map[string]interface{}{"start": map[string]int{"line": 2, "character": 5}},
		},
	})
	locs := parseDefinitionResult(raw)
	if len(locs) != 1 {
		t.Fatalf("locs = %+v, want 1", locs)
	}
	if locs[0].AbsPath != f || locs[0].Line != 3 || locs[0].Col != 6 || locs[0].Source != "lsp" {
		t.Fatalf("unexpected location: %+v", locs[0])
	}
	if locs[0].Preview != "func CreateOrder() {}" {
		t.Fatalf("preview = %q", locs[0].Preview)
	}
}

func TestFallbackDefinitionFindsGoAndThriftSymbols(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "biz.go")
	thriftFile := filepath.Join(tmp, "order.thrift")
	_ = os.WriteFile(goFile, []byte("package biz\n\nconst OrderStatusCreated = 10\n"), 0o644)
	_ = os.WriteFile(thriftFile, []byte("service PayerVerifyService {\n  string Ping()\n}\n"), 0o644)

	m := NewManager("")
	defer m.Shutdown()

	locs, src := m.Definition(context.Background(), tmp, thriftFile, 2, 1, "PayerVerifyService", []string{tmp})
	if src != "fallback" || len(locs) == 0 || locs[0].AbsPath != thriftFile || locs[0].Line != 1 {
		t.Fatalf("thrift fallback = src=%s locs=%+v", src, locs)
	}

	locs = fallbackDefinition("bizenum.OrderStatusCreated", "", []string{tmp})
	if len(locs) == 0 || locs[0].AbsPath != goFile || locs[0].Line != 3 {
		t.Fatalf("go fallback = %+v", locs)
	}
}

func TestRealGoplsWhenInstalled(t *testing.T) {
	m := NewManager("")
	defer m.Shutdown()
	if m.resolveCommand("go") == nil {
		t.Skip("gopls not installed on PATH or ~/go/bin")
	}
	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module example.com/demo\n\ngo 1.19\n"), 0o644)
	_ = os.WriteFile(filepath.Join(tmp, "types.go"), []byte("package demo\n\nconst StatusReady = 7\n"), 0o644)
	mainFile := filepath.Join(tmp, "main.go")
	_ = os.WriteFile(mainFile, []byte("package demo\n\nfunc Check() int {\n\treturn StatusReady\n}\n"), 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	locs, src := m.Definition(ctx, tmp, mainFile, 4, 10, "StatusReady", []string{tmp})
	if src != "lsp:go" || len(locs) == 0 {
		t.Fatalf("gopls definition failed: src=%s locs=%+v", src, locs)
	}
	if filepath.Base(locs[0].AbsPath) != "types.go" || locs[0].Line != 3 {
		t.Fatalf("gopls location = %+v, want types.go:3", locs[0])
	}

	refs, refSrc := m.References(ctx, tmp, filepath.Join(tmp, "types.go"), 3, 7, "StatusReady", []string{tmp})
	if refSrc != "lsp:go" || len(refs) == 0 {
		t.Fatalf("gopls references failed: src=%s refs=%+v", refSrc, refs)
	}
	if filepath.Base(refs[0].AbsPath) != "main.go" || refs[0].Line != 4 {
		t.Fatalf("gopls reference = %+v, want main.go:4", refs[0])
	}
}
