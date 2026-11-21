package astx

import (
	"testing"

	"reqboard/internal/gitx"
)

const goSrc = `package demo

import "fmt"

type Order struct {
	ID string
}

type Repo interface {
	Get() string
}

func (o *Order) Create() string {
	return fmt.Sprintf("%s", o.ID)
}

func Cancel() {
	println("cancel")
}

func init() {}

func init() {}

const (
	A = 1
	B = 2
)

var Global = 1
`

func TestParseGoSymbols(t *testing.T) {
	syms, err := ParseGo("demo.go", []byte(goSrc))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, s := range syms {
		names[s.Name] = s.Kind
	}
	for name, kind := range map[string]string{
		"Order": "type", "Repo": "type", "Order.Create": "method", "Cancel": "func",
		"init": "func", "init#2": "func", "A": "const", "B": "const", "Global": "var",
	} {
		if names[name] != kind {
			t.Fatalf("symbol %s = %q, want %q (all=%v)", name, names[name], kind, names)
		}
	}
	sym, _ := FindSymbol(syms, "Create")
	if sym == nil || sym.Name != "Order.Create" {
		t.Fatalf("method suffix lookup failed: %+v", sym)
	}
}

func TestProject(t *testing.T) {
	syms, err := ParseGo("demo.go", []byte(goSrc))
	if err != nil {
		t.Fatal(err)
	}
	// Change line 11 (inside Order.Create) and line 15 (Cancel).
	hunks := []gitx.Hunk{
		{NewStart: 14, NewCount: 1, OldStart: 14, OldCount: 1, Lines: []string{"-\treturn \"old\"", "+\treturn \"new\""}},
		{NewStart: 18, NewCount: 2, OldStart: 18, OldCount: 1, Lines: []string{"-\tprintln(\"cancel\")", "+\tprintln(\"cancel-v2\")", "+\tprintln(\"extra\")"}},
	}
	hits := Project(syms, hunks)
	byName := map[string]Hit{}
	for _, h := range hits {
		byName[h.Symbol.Name] = h
	}
	if h, ok := byName["Order.Create"]; !ok || h.Added != 1 || h.Deleted != 1 {
		t.Fatalf("Order.Create hit = %+v (all=%+v)", h, hits)
	}
	if h, ok := byName["Cancel"]; !ok || h.Added != 2 || h.Deleted != 1 {
		t.Fatalf("Cancel hit = %+v", h)
	}
}

func TestProjectPackageLineFoldsIntoFirstSymbol(t *testing.T) {
	src := "package demo\n\nimport \"fmt\"\n\nfunc A() { fmt.Println(1) }\n"
	syms, _ := ParseGo("a.go", []byte(src))
	hunks := []gitx.Hunk{{NewStart: 1, NewCount: 5, Lines: []string{
		"+package demo", "+", "+import \"fmt\"", "+", "+func A() { fmt.Println(1) }",
	}}}
	hits := Project(syms, hunks)
	if len(hits) != 1 || hits[0].Symbol.Name != "A" {
		t.Fatalf("package lines should fold into first symbol, got %+v", hits)
	}
}

func TestParseThriftBlocks(t *testing.T) {
	src := "namespace go demo\n\nstruct Order {\n  1: string id\n}\n\nservice Svc {\n  string Get(1: string id)\n}\n"
	syms := ParseBlocks("demo.thrift", []byte(src))
	if len(syms) != 2 || syms[0].Name != "Order" || syms[1].Name != "Svc" {
		t.Fatalf("thrift symbols = %+v", syms)
	}
	if syms[0].End < 3 || syms[1].End < 7 {
		t.Fatalf("block ranges = %+v", syms)
	}
}

func TestSymbolsFallbackOnParseError(t *testing.T) {
	syms, err := Symbols("bad.go", []byte("package x\nfunc ("))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if len(syms) != 1 || syms[0].Name != "(file)" {
		t.Fatalf("fallback = %+v", syms)
	}
}
