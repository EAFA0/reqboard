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

func runBind(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return fail("usage: reqboard bind <node-id> --repo <r> --file <f> --symbol <s> --why <reason>")
	}
	id := pos[0]
	why := strings.TrimSpace(a.Str("why"))
	if why == "" {
		return &Error{Code: 1, Msg: "--why is required", Text: "[REQBOARD BLOCKED] --why 必填：每处代码改动都必须说明它对应哪条 PRD 规则。\n  -> 示例: reqboard bind " + id + " --repo order_api --file internal/service/x.go --symbol CreateOrder --role impl --why \"PRD 3.2 创单校验\"\n"}
	}
	repoName := a.Str("repo")
	file := a.Str("file")
	symbol := strings.TrimSpace(a.Str("symbol"))
	allSymbols := a.Bool("all-symbols")
	keepStatus := a.Bool("keep-status")
	if repoName == "" || file == "" {
		return fail("--repo and --file are required")
	}
	if allSymbols && symbol != "" {
		return fail("--symbol 与 --all-symbols 只能二选一")
	}
	if !allSymbols && symbol == "" {
		return fail("--symbol is required (or use --all-symbols)")
	}
	role := a.Str("role")
	if role == "" {
		role = model.RoleImpl
	}
	if !model.ValidRole(role) {
		return fail("invalid --role %q (impl|mock|test)", role)
	}
	partial := a.Str("partial")
	if allSymbols && partial != "" {
		return fail("--partial 仅适用于单符号绑定，不能与 --all-symbols 同用")
	}

	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	cfg, cerr := ctx.Store.LoadConfig()
	if cerr != nil {
		return fail("%v", cerr)
	}
	var repo *model.Repo
	for i := range cfg.Repos {
		if cfg.Repos[i].Name == repoName {
			repo = &cfg.Repos[i]
			break
		}
	}
	if repo == nil {
		var names []string
		for _, r := range cfg.Repos {
			names = append(names, r.Name)
		}
		return fail("repo %q not configured (known: %s)", repoName, strings.Join(names, ", "))
	}
	repoPath := store.RepoPath(ctx.Root, *repo)
	abs := file
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(repoPath, filepath.FromSlash(file))
	}
	st, serr := os.Stat(abs)
	if serr != nil || st.IsDir() {
		return fail("file not found: %s", abs)
	}
	storedFile := filepath.ToSlash(file)
	if filepath.IsAbs(file) {
		if rel, err := filepath.Rel(repoPath, file); err == nil && !strings.HasPrefix(rel, "..") {
			storedFile = filepath.ToSlash(rel)
		}
	}
	storedFile = filepath.ToSlash(filepath.Clean(filepath.FromSlash(storedFile)))

	return ctx.WithLock(func() *Error {
		dag, derr := ctx.Store.LoadDAG()
		if derr != nil {
			return fail("%v", derr)
		}
		n, ne := findNodeOrFail(dag, id)
		if ne != nil {
			return ne
		}
		if n.Scope != model.ScopeMine {
			rule := "只有 scope=mine 的节点允许 bind。"
			switch n.Scope {
			case model.ScopeOutOfScope:
				rule = "out_of_scope 是 PRD 红线，禁止 Agent 挂载/修改该部分代码。"
			case model.ScopeExternal:
				rule = "external 是外部联调方依赖，禁止 Agent 修改其代码。"
			case model.ScopeCollaborator:
				rule = "collaborator 由协作者负责，禁止 Agent 修改其代码。"
			case model.ScopeTodo:
				rule = "todo 是前置依赖（DDL / 网关配置 / Feature Flag），不是业务写码节点，禁止 bind。"
			}
			return &Error{Code: 1, Msg: "node scope is " + n.Scope, Text: fmt.Sprintf("[REQBOARD BLOCKED] 无法 bind %s：scope=%s\n  -> %s\n", id, n.Scope, rule)}
		}
		if n.Status == model.StatusProposed {
			return &Error{Code: 1, Msg: "node is proposed", Text: "[REQBOARD BLOCKED] 节点仍为 proposed，待 owner 审批，请勿提前改代码或挂载。\n"}
		}
		if n.Status == model.StatusDeprecated {
			return &Error{Code: 1, Msg: "node is deprecated", Text: "[REQBOARD BLOCKED] 节点已 deprecated，禁止新增绑定；请清理残骸（To-Delete）而不是继续实现。\n"}
		}
		if n.Status == model.StatusDone {
			return &Error{Code: 1, Msg: "node is done", Text: "[REQBOARD BLOCKED] 节点已 done；如需返工请人类先 Review 打回。\n"}
		}

		fileLevel := false
		var targets []string
		if allSymbols {
			baseRef := repo.BaseRef
			if baseRef == "" {
				baseRef = "HEAD"
			}
			found, level, err := changedSymbols(repoPath, baseRef, storedFile, abs)
			if err != nil {
				return fail("%v", err)
			}
			targets, fileLevel = found, level
			if len(targets) == 0 && !fileLevel {
				return fail("文件 %s 相对 base 没有可绑定的符号改动", storedFile)
			}
		} else {
			targets = []string{symbol}
			if strings.EqualFold(filepath.Ext(abs), ".go") {
				src, err := os.ReadFile(abs)
				if err != nil {
					return fail("read %s: %v", abs, err)
				}
				syms, perr := astx.ParseGo(storedFile, src)
				if perr != nil {
					return fail("parse Go file %s: %v", storedFile, perr)
				}
				if sym, names := astx.FindSymbol(syms, symbol); sym == nil {
					sort.Strings(names)
					if len(names) > 40 {
						names = append(names[:40], "…")
					}
					return &Error{Code: 1, Msg: "symbol not found: " + symbol, Text: fmt.Sprintf("[REQBOARD BLOCKED] 符号 %q 不存在于 %s:%s\n  -> 该文件真实顶层符号: %s\n  -> 请用真实符号重试；若文件改错，先 `reqboard check --node %s` 确认。\n", symbol, repoName, storedFile, strings.Join(names, ", "), id)}
				}
			}
		}

		bindings, berr := ctx.Store.LoadBindings()
		if berr != nil {
			return fail("%v", berr)
		}
		granularity := model.GranularitySymbol
		hint := ""
		if partial != "" {
			granularity = model.GranularityPartialSymbol
			hint = partial
		}
		session := a.Str("session")
		if session == "" {
			session = n.SessionID
		}
		if fileLevel {
			targets = []string{""}
			granularity = model.GranularityFile
			hint = ""
		}
		var bound []*model.Binding
		created, updated := 0, 0
		for _, sym := range targets {
			binding, isNew := upsertBinding(bindings, id, repoName, storedFile, sym, granularity, hint, role, why, session)
			bound = append(bound, binding)
			if isNew {
				created++
			} else {
				updated++
			}
		}
		symbols := make([]string, 0, len(bound))
		for _, b := range bound {
			symbols = append(symbols, b.Symbol)
		}
		detail := fmt.Sprintf("%s:%s::%s (%s)", repoName, storedFile, strings.Join(symbols, ","), role)
		if err := ctx.SaveBindings(bindings, "bind", id, detail); err != nil {
			return err
		}
		statusChanged := false
		if n.Status == model.StatusPending && !keepStatus {
			n.Status = model.StatusInProgress
			n.UpdatedAt = model.Now()
			statusChanged = true
			if err := ctx.SaveDAG(dag, "node.status", id, "in_progress (first bind)"); err != nil {
				return err
			}
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[REQBOARD BIND] %s %s\n", id, n.Title))
		if allSymbols {
			sb.WriteString(fmt.Sprintf("  文件级批量挂载：新增 %d 条，更新 %d 条\n", created, updated))
		} else {
			verb := "新增"
			if created == 0 {
				verb = "更新"
			}
			sb.WriteString(fmt.Sprintf("  %s绑定 %s\n", verb, bound[0].ID))
		}
		for _, b := range bound {
			sym := b.Symbol
			if sym == "" {
				sym = "(file)"
			}
			sb.WriteString(fmt.Sprintf("    - %s  %s:%s::%s  role=%s\n", b.ID, repoName, storedFile, sym, role))
		}
		sb.WriteString("  why: " + why + "\n")
		if granularity == model.GranularityPartialSymbol {
			sb.WriteString("  partial anchor: " + hint + "（多节点共享公共符号时用于区分改动块）\n")
		}
		if keepStatus && !statusChanged {
			sb.WriteString("  --keep-status: 节点状态保持 " + n.Status + "\n")
		}
		sb.WriteString("  下一步: 改码后 `reqboard check --node " + id + "`；无 Orphan/Broken 再 `reqboard done " + id + "`。\n")
		data := map[string]interface{}{"ok": true, "bindings": bound, "created": created, "updated": updated}
		if len(bound) == 1 {
			data["binding"] = bound[0]
			data["updated"] = created == 0
		}
		ctx.Emit(sb.String(), data)
		return nil
	})
}

// changedSymbols projects the file's diff hunks onto its Go AST symbols. For
// non-Go files (or changes outside any symbol) it returns fileLevel=true so the
// caller records one file-granularity binding instead.
func changedSymbols(repoPath, baseRef, storedFile, abs string) ([]string, bool, error) {
	files, err := gitx.Collect(repoPath, baseRef)
	if err != nil {
		return nil, false, err
	}
	wantAbs := filepath.Clean(abs)
	var fd *gitx.FileDiff
	for i := range files {
		p := filepath.Clean(filepath.Join(repoPath, filepath.FromSlash(files[i].Path)))
		if p == wantAbs || filepath.ToSlash(files[i].Path) == storedFile {
			fd = &files[i]
			break
		}
	}
	if fd == nil {
		return nil, false, fmt.Errorf("文件 %s 相对 %s 无改动，无法 --all-symbols", storedFile, baseRef)
	}
	if fd.Binary || !strings.EqualFold(filepath.Ext(abs), ".go") {
		return nil, true, nil
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, false, err
	}
	hunks := fd.Hunks
	if fd.Untracked {
		lines := strings.Split(strings.TrimSuffix(string(src), "\n"), "\n")
		if len(src) == 0 {
			lines = nil
		}
		h := gitx.Hunk{NewStart: 1, NewCount: len(lines), Lines: make([]string, 0, len(lines))}
		for _, line := range lines {
			h.Lines = append(h.Lines, "+"+line)
		}
		hunks = []gitx.Hunk{h}
	}
	syms, perr := astx.ParseGo(storedFile, src)
	if perr != nil {
		return nil, false, fmt.Errorf("parse Go file %s: %w", storedFile, perr)
	}
	hits := astx.Project(syms, hunks)
	seen := map[string]bool{}
	var out []string
	for _, h := range hits {
		if h.Symbol.Name == "" || seen[h.Symbol.Name] {
			continue
		}
		seen[h.Symbol.Name] = true
		out = append(out, h.Symbol.Name)
	}
	sort.Strings(out)
	if len(out) == 0 {
		// Changes outside any top-level symbol (imports/package comments) still
		// need ownership: fall back to a file-granularity binding.
		return nil, true, nil
	}
	return out, false, nil
}

func upsertBinding(bindings *model.Bindings, nodeID, repoName, storedFile, symbol, granularity, hint, role, why, session string) (*model.Binding, bool) {
	for i := range bindings.Bindings {
		b := &bindings.Bindings[i]
		if b.NodeID == nodeID && b.Repo == repoName && filepath.ToSlash(b.File) == storedFile && b.Symbol == symbol {
			b.Granularity = granularity
			b.AnchorHint = hint
			b.Role = role
			b.Why = why
			b.SessionID = session
			b.BoundAt = model.Now()
			return b, false
		}
	}
	used := map[string]bool{}
	for _, b := range bindings.Bindings {
		used[b.ID] = true
	}
	nb := model.Binding{
		ID:     model.NewID("bd", used, nodeID, repoName, storedFile, symbol, model.Now()),
		NodeID: nodeID, Repo: repoName, File: storedFile,
		Granularity: granularity, Symbol: symbol, AnchorHint: hint,
		Role: role, Why: why, SessionID: session, BoundAt: model.Now(),
	}
	bindings.Bindings = append(bindings.Bindings, nb)
	return &bindings.Bindings[len(bindings.Bindings)-1], true
}

func runUnbind(a *Args, rest []string) *Error {
	pos := a.Pos()
	if len(pos) == 0 {
		return fail("usage: reqboard unbind <binding-id>")
	}
	id := pos[0]
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		bindings, berr := ctx.Store.LoadBindings()
		if berr != nil {
			return fail("%v", berr)
		}
		b := bindings.FindBinding(id)
		if b == nil {
			return fail("binding not found: %s", id)
		}
		nodeID := b.NodeID
		out := []model.Binding{}
		for _, x := range bindings.Bindings {
			if x.ID != id {
				out = append(out, x)
			}
		}
		bindings.Bindings = out
		if err := ctx.SaveBindings(bindings, "unbind", nodeID, fmt.Sprintf("%s:%s::%s", b.Repo, b.File, b.Symbol)); err != nil {
			return err
		}
		ctx.Emit(fmt.Sprintf("[REQBOARD UNBIND] %s 已移除 %s:%s::%s\n", id, b.Repo, b.File, b.Symbol),
			map[string]interface{}{"ok": true, "binding_id": id, "node_id": nodeID})
		return nil
	})
}
