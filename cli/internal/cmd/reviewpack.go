package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"reqboard/internal/astx"
	"reqboard/internal/gitx"
	"reqboard/internal/model"
	"reqboard/internal/store"
)

func runReviewPack(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return fail("usage: reqboard review-pack <node-id>")
	}
	id := pos[0]
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	dag, derr := ctx.Store.LoadDAG()
	if derr != nil {
		return fail("%v", derr)
	}
	n, ne := findNodeOrFail(dag, id)
	if ne != nil {
		return ne
	}
	cfg, cerr := ctx.Store.LoadConfig()
	if cerr != nil {
		return fail("%v", cerr)
	}
	bindings, berr := ctx.Store.LoadBindings()
	if berr != nil {
		return fail("%v", berr)
	}
	doc := loadPRDDoc(ctx)

	type diffSection struct {
		BindingID string      `json:"binding_id"`
		Repo      string      `json:"repo"`
		File      string      `json:"file"`
		Symbol    string      `json:"symbol"`
		Role      string      `json:"role"`
		Why       string      `json:"why"`
		Start     int         `json:"start,omitempty"`
		End       int         `json:"end,omitempty"`
		Hunks     []gitx.Hunk `json:"hunks"`
		Error     string      `json:"error,omitempty"`
	}
	var sections []diffSection
	for _, b := range bindings.BindingsForNode(id) {
		sec := diffSection{BindingID: b.ID, Repo: b.Repo, File: b.File, Symbol: b.Symbol, Role: b.Role, Why: b.Why, Hunks: []gitx.Hunk{}}
		var repo *model.Repo
		for i := range cfg.Repos {
			if cfg.Repos[i].Name == b.Repo {
				repo = &cfg.Repos[i]
				break
			}
		}
		if repo == nil {
			sec.Error = "repo not configured"
			sections = append(sections, sec)
			continue
		}
		repoPath := store.RepoPath(ctx.Root, *repo)
		base := repo.BaseRef
		if base == "" {
			base = "HEAD"
		}
		abs := b.File
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(repoPath, filepath.FromSlash(b.File))
		}
		if src, err := os.ReadFile(abs); err == nil {
			if syms, perr := astx.Symbols(b.File, src); perr == nil {
				if sym, _ := astx.FindSymbol(syms, b.Symbol); sym != nil {
					sec.Start, sec.End = sym.Start, sym.End
				}
			}
		}
		hunks, herr := gitx.DiffFile(repoPath, base, filepath.ToSlash(b.File), 3)
		if herr != nil {
			sec.Error = herr.Error()
			sections = append(sections, sec)
			continue
		}
		for _, h := range hunks {
			if sec.Start > 0 && !hunkTouches(h, sec.Start, sec.End) {
				continue
			}
			sec.Hunks = append(sec.Hunks, h)
		}
		sections = append(sections, sec)
	}
	sort.SliceStable(sections, func(i, j int) bool {
		if sections[i].Repo != sections[j].Repo {
			return sections[i].Repo < sections[j].Repo
		}
		return sections[i].File < sections[j].File
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[REQBOARD REVIEW PACK] %s  %s\n", n.ID, n.Title))
	sb.WriteString(fmt.Sprintf("  scope=%s status=%s stale=%v review_notes_open=%d\n", n.Scope, n.Status, n.Stale, len(n.OpenNotes())))
	sb.WriteString("\n—— 锚定 PRD 片段 ——\n")
	if doc == nil {
		sb.WriteString("  （prd.md 为空）\n")
	} else {
		set := doc.AnchorSet()
		for _, ref := range n.PRDRefs {
			b := set[ref]
			if b == nil {
				sb.WriteString("  [MISSING] " + ref + "\n")
				continue
			}
			sb.WriteString(fmt.Sprintf("  <!-- @%s -->\n", b.Anchor))
			for _, line := range strings.Split(b.Text, "\n") {
				sb.WriteString("  " + line + "\n")
			}
		}
	}
	if len(n.OpenNotes()) > 0 {
		sb.WriteString("\n—— 待处理批注 ——\n")
		for _, r := range n.OpenNotes() {
			sb.WriteString(fmt.Sprintf("  - %s: %s\n", r.ID, r.Comment))
		}
	}
	sb.WriteString("\n—— 挂载符号与真实 Diff ——\n")
	if len(sections) == 0 {
		sb.WriteString("  （无绑定；先 `reqboard bind ...` 再生成 review-pack）\n")
	}
	for _, sec := range sections {
		sb.WriteString(fmt.Sprintf("\n## %s:%s :: %s  [%s]\n", sec.Repo, sec.File, sec.Symbol, sec.Role))
		sb.WriteString("   why: " + sec.Why + "\n")
		if sec.Error != "" {
			sb.WriteString("   [ERROR] " + sec.Error + "\n")
			continue
		}
		if len(sec.Hunks) == 0 {
			sb.WriteString("   （该符号相对 base 无 diff）\n")
			continue
		}
		for _, h := range sec.Hunks {
			sb.WriteString("   " + h.Header + "\n")
			for _, line := range h.Lines {
				sb.WriteString("   " + line + "\n")
			}
		}
	}
	sb.WriteString("\n—— 快审提示 ——\n")
	sb.WriteString("  检查：是否只改了 PRD 覆盖的行为？是否塞入未绑定的额外改动？是否遗留 Mock？\n")

	prdBlocks := []map[string]interface{}{}
	if doc != nil {
		set := doc.AnchorSet()
		for _, ref := range n.PRDRefs {
			if b := set[ref]; b != nil {
				prdBlocks = append(prdBlocks, map[string]interface{}{"anchor": b.Anchor, "text": b.Text})
			}
		}
	}
	data := map[string]interface{}{
		"ok": true, "node": n, "prd_blocks": prdBlocks,
		"bindings": bindings.BindingsForNode(id), "diffs": sections,
		"open_review_notes": n.OpenNotes(), "self_mocks": n.SelfMocks,
	}
	ctx.Emit(sb.String(), data)
	return nil
}

func hunkTouches(h gitx.Hunk, start, end int) bool {
	ns, ne := h.NewStart, h.NewStart+h.NewCount-1
	if h.NewCount == 0 {
		ne = ns
	}
	os, oe := h.OldStart, h.OldStart+h.OldCount-1
	if h.OldCount == 0 {
		oe = os
	}
	inter := func(a, b, c, d int) bool { return a <= d && c <= b }
	return inter(ns, ne, start, end) || inter(os, oe, start, end)
}
