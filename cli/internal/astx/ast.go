// Package astx projects git diff line ranges onto top-level Go symbols (and
// coarse blocks for thrift/proto) without any external parser.
package astx

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"reqboard/internal/gitx"
)

// Symbol is a top-level declaration range.
type Symbol struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // func | method | type | const | var | block | file
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Hit is a symbol with the portion of diff attributed to it.
type Hit struct {
	Symbol     Symbol   `json:"symbol"`
	Added      int      `json:"added"`
	Deleted    int      `json:"deleted"`
	AddedLines []string `json:"added_lines,omitempty"`
}

// FileSymbol is the fallback symbol for files we cannot parse.
func FileSymbol(lines int) Symbol {
	if lines < 1 {
		lines = 1
	}
	return Symbol{Name: "(file)", Kind: "file", Start: 1, End: lines}
}

// ParseGo extracts top-level declarations from Go source.
func ParseGo(path string, src []byte) ([]Symbol, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []Symbol
	seen := map[string]int{}
	add := func(name, kind string, start, end int) {
		if name == "" {
			return
		}
		seen[name]++
		if seen[name] > 1 {
			name = fmt.Sprintf("%s#%d", name, seen[name])
		}
		out = append(out, Symbol{Name: name, Kind: kind, Start: start, End: end})
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			kind := "func"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				name = receiverName(d.Recv.List[0].Type) + "." + name
			}
			add(name, kind, fset.Position(d.Pos()).Line, fset.Position(d.End()).Line)
		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					add(ts.Name.Name, "type", fset.Position(ts.Pos()).Line, fset.Position(ts.End()).Line)
				}
			case token.CONST, token.VAR:
				kind := "const"
				if d.Tok == token.VAR {
					kind = "var"
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					start := fset.Position(vs.Pos()).Line
					end := fset.Position(vs.End()).Line
					if len(vs.Names) == 0 {
						continue
					}
					for _, n := range vs.Names {
						add(n.Name, kind, start, end)
					}
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, nil
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	}
	return "?"
}

var thriftDeclRe = regexp.MustCompile(`^\s*(struct|service|enum|union|exception|typedef)\s+([A-Za-z_][A-Za-z0-9_]*)`)
var protoDeclRe = regexp.MustCompile(`^\s*(message|service|enum)\s+([A-Za-z_][A-Za-z0-9_]*)`)

// ParseBlocks extracts coarse blocks from thrift/proto files.
func ParseBlocks(path string, src []byte) []Symbol {
	ext := strings.ToLower(filepath.Ext(path))
	var re *regexp.Regexp
	switch ext {
	case ".thrift":
		re = thriftDeclRe
	case ".proto":
		re = protoDeclRe
	default:
		return []Symbol{FileSymbol(countLines(src))}
	}
	lines := strings.Split(string(src), "\n")
	var out []Symbol
	for i, line := range lines {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		start := i + 1
		end := braceEnd(lines, i)
		if end < start {
			end = start
		}
		out = append(out, Symbol{Name: m[2], Kind: m[1], Start: start, End: end})
	}
	if len(out) == 0 {
		return []Symbol{FileSymbol(len(lines))}
	}
	return out
}

func braceEnd(lines []string, startIdx int) int {
	depth := 0
	seen := false
	for i := startIdx; i < len(lines); i++ {
		for _, r := range lines[i] {
			switch r {
			case '{':
				depth++
				seen = true
			case '}':
				depth--
				if seen && depth <= 0 {
					return i + 1
				}
			}
		}
		if seen && depth == 0 {
			return i + 1
		}
	}
	return len(lines)
}

func countLines(src []byte) int {
	if len(src) == 0 {
		return 0
	}
	n := strings.Count(string(src), "\n")
	if src[len(src)-1] != '\n' {
		n++
	}
	return n
}

// Symbols parses a file by extension, falling back to a file-level symbol.
func Symbols(path string, src []byte) ([]Symbol, error) {
	if strings.EqualFold(filepath.Ext(path), ".go") {
		syms, err := ParseGo(path, src)
		if err != nil {
			return []Symbol{FileSymbol(countLines(src))}, err
		}
		if len(syms) == 0 {
			return []Symbol{FileSymbol(countLines(src))}, nil
		}
		return syms, nil
	}
	return ParseBlocks(path, src), nil
}

// SymbolAt returns the symbol containing a 1-based line.
func SymbolAt(syms []Symbol, line int) *Symbol {
	for i := range syms {
		if line >= syms[i].Start && line <= syms[i].End {
			return &syms[i]
		}
	}
	return nil
}

// FindSymbol resolves an exact symbol name, then a unique method suffix
// (`Foo` -> `Type.Foo`). It returns the symbol and the available names.
func FindSymbol(syms []Symbol, name string) (*Symbol, []string) {
	names := make([]string, 0, len(syms))
	for i := range syms {
		names = append(names, syms[i].Name)
		if syms[i].Name == name {
			return &syms[i], names
		}
	}
	var suffix []*Symbol
	for i := range syms {
		if strings.HasSuffix(syms[i].Name, "."+name) {
			suffix = append(suffix, &syms[i])
		}
	}
	if len(suffix) == 1 {
		return suffix[0], names
	}
	return nil, names
}

// symbolForLine finds the containing symbol; package/import lines above the
// first declaration fold into the nearest following symbol so one untracked
// file does not produce both a `(file)` and a function orphan.
func symbolForLine(syms []Symbol, line, maxLine int) *Symbol {
	if s := SymbolAt(syms, line); s != nil {
		return s
	}
	for i := range syms {
		if syms[i].Start >= line {
			return &syms[i]
		}
	}
	if len(syms) > 0 {
		return &syms[len(syms)-1]
	}
	if maxLine < 1 {
		maxLine = 1
	}
	return &Symbol{Name: "(file)", Kind: "file", Start: 1, End: maxLine}
}

// Project maps diff hunks onto symbols, distributing +/- counts.
func Project(syms []Symbol, hunks []gitx.Hunk) []Hit {
	if len(syms) == 0 {
		return nil
	}
	agg := map[string]*Hit{}
	maxLine := 0
	for _, s := range syms {
		if s.End > maxLine {
			maxLine = s.End
		}
	}
	ensure := func(s Symbol) *Hit {
		if h, ok := agg[s.Name]; ok {
			return h
		}
		h := &Hit{Symbol: s}
		agg[s.Name] = h
		return h
	}
	// Make sure every symbol touched by a hunk is represented even with 0 counts.
	for _, h := range hunks {
		newLine := h.NewStart
		oldLine := h.OldStart
		for _, line := range h.Lines {
			switch {
			case strings.HasPrefix(line, "+"):
				ln := newLine
				s := symbolForLine(syms, ln, maxLine)
				hh := ensure(*s)
				hh.Added++
				text := strings.TrimPrefix(line, "+")
				if len(hh.AddedLines) < 200 {
					hh.AddedLines = append(hh.AddedLines, text)
				}
				newLine++
			case strings.HasPrefix(line, "-"):
				ln := oldLine
				if ln < 1 {
					ln = 1
				}
				s := SymbolAt(syms, ln)
				if s == nil {
					s = symbolForLine(syms, newLine, maxLine)
				}
				ensure(*s).Deleted++
				oldLine++
			default:
				newLine++
				oldLine++
			}
		}
		// Pure deletion: hunk has no +/- lines (or only deletions) and
		// NewCount == 0; attribute it to the surrounding symbol.
		if h.NewCount == 0 {
			probe := h.NewStart
			if probe < 1 {
				probe = 1
			}
			if probe > maxLine && maxLine > 0 {
				probe = maxLine
			}
			if s := SymbolAt(syms, probe); s != nil {
				ensure(*s)
			}
		}
	}
	out := make([]Hit, 0, len(agg))
	for _, h := range agg {
		out = append(out, *h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Symbol.Start != out[j].Symbol.Start {
			return out[i].Symbol.Start < out[j].Symbol.Start
		}
		return out[i].Symbol.Name < out[j].Symbol.Name
	})
	return out
}
