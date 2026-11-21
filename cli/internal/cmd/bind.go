package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"reqboard/internal/astx"
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
	if repoName == "" || file == "" || symbol == "" {
		return fail("--repo, --file and --symbol are required")
	}
	role := a.Str("role")
	if role == "" {
		role = model.RoleImpl
	}
	if !model.ValidRole(role) {
		return fail("invalid --role %q (impl|mock|test)", role)
	}
	partial := a.Str("partial")

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
		updated := false
		for i := range bindings.Bindings {
			b := &bindings.Bindings[i]
			if b.NodeID == id && b.Repo == repoName && filepath.ToSlash(b.File) == storedFile && b.Symbol == symbol {
				b.Granularity = granularity
				b.AnchorHint = hint
				b.Role = role
				b.Why = why
				b.SessionID = session
				b.BoundAt = model.Now()
				updated = true
				break
			}
		}
		var binding *model.Binding
		if updated {
			for i := range bindings.Bindings {
				if bindings.Bindings[i].NodeID == id && bindings.Bindings[i].Repo == repoName && filepath.ToSlash(bindings.Bindings[i].File) == storedFile && bindings.Bindings[i].Symbol == symbol {
					binding = &bindings.Bindings[i]
					break
				}
			}
		} else {
			used := map[string]bool{}
			for _, b := range bindings.Bindings {
				used[b.ID] = true
			}
			nb := model.Binding{
				ID:     model.NewID("bd", used, id, repoName, storedFile, symbol, model.Now()),
				NodeID: id, Repo: repoName, File: storedFile,
				Granularity: granularity, Symbol: symbol, AnchorHint: hint,
				Role: role, Why: why, SessionID: session, BoundAt: model.Now(),
			}
			bindings.Bindings = append(bindings.Bindings, nb)
			binding = &bindings.Bindings[len(bindings.Bindings)-1]
		}
		if err := ctx.SaveBindings(bindings, "bind", id, fmt.Sprintf("%s:%s::%s (%s)", repoName, storedFile, symbol, role)); err != nil {
			return err
		}
		if n.Status == model.StatusPending {
			n.Status = model.StatusInProgress
			n.UpdatedAt = model.Now()
			if err := ctx.SaveDAG(dag, "node.status", id, "in_progress (first bind)"); err != nil {
				return err
			}
		}
		action := "新增"
		if updated {
			action = "更新"
		}
		msg := fmt.Sprintf("[REQBOARD BIND] %s %s\n  %s绑定 %s: %s:%s::%s  role=%s\n  why: %s\n", id, n.Title, action, binding.ID, repoName, storedFile, symbol, role, why)
		if granularity == model.GranularityPartialSymbol {
			msg += "  partial anchor: " + hint + "（多节点共享公共符号时用于区分改动块）\n"
		}
		msg += "  下一步: 改码后 `reqboard check --node " + id + "`；无 Orphan/Broken 再 `reqboard done " + id + "`。\n"
		ctx.Emit(msg, map[string]interface{}{"ok": true, "binding": binding, "updated": updated})
		return nil
	})
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
