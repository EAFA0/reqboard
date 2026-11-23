package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"reqboard/internal/gitx"
	"reqboard/internal/lsp"
	"reqboard/internal/store"
	"reqboard/internal/webui"
)

type webServer struct {
	root          string
	openCmd       string
	lsp           *lsp.Manager
	mu            sync.Mutex
	extMu         sync.RWMutex
	externalAllow map[string]bool
}

func runServe(a *Args, rest []string) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	dag, derr := ctx.Store.LoadDAG()
	if derr != nil {
		return fail("%v", derr)
	}

	addr := strings.TrimSpace(a.Str("addr"))
	if port := a.Int("port", 0); port > 0 && addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", port)
	}
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	openCmd := strings.TrimSpace(a.Str("open-cmd"))
	lspSpec := strings.TrimSpace(a.Str("lsp"))
	if lspSpec == "" {
		lspSpec = strings.TrimSpace(os.Getenv("REQBOARD_LSP"))
	}
	var lspMgr *lsp.Manager
	if !strings.EqualFold(lspSpec, "off") && !strings.EqualFold(lspSpec, "none") {
		lspMgr = lsp.NewManager(lspSpec)
		defer lspMgr.Shutdown()
	}

	ln, actualAddr, lerr := listenWithFallback(addr)
	if lerr != nil {
		return fail("listen %s: %v", addr, lerr)
	}

	srv := &http.Server{
		Handler:           newServeHandlerWithLSP(ctx.Root, openCmd, lspMgr),
		ReadHeaderTimeout: 10 * time.Second,
	}

	url := "http://" + actualAddr
	fmt.Printf("[REQBOARD SERVE] %s\n", dag.RequirementName)
	fmt.Printf("  URL:  %s\n", url)
	fmt.Printf("  Root: %s\n", ctx.Root)
	if lspMgr != nil {
		avail := lspMgr.AvailableLanguages()
		if len(avail) > 0 {
			var parts []string
			for l, c := range avail {
				parts = append(parts, l+"="+filepath.Base(strings.Fields(c)[0]))
			}
			fmt.Printf("  LSP:  %s（其余语言自动回退工作区符号索引）\n", strings.Join(parts, ", "))
		} else {
			fmt.Println("  LSP:  未探测到本机 LS，使用内置多语言工作区符号索引（可用 --lsp 指定）")
		}
	}
	if openCmd != "" {
		fmt.Printf("  IDE:  %s\n", openCmd)
	}
	fmt.Println("  按 Ctrl+C 停止（.reqboard/ 热刷新 · 内置 Unified/Split Diff · Cmd/Ctrl+点击跳定义）")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fail("serve: %v", err)
	}
	return nil
}

func listenWithFallback(addr string) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, ln.Addr().String(), nil
	}
	host, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, "", err
	}
	port, convErr := strconv.Atoi(portStr)
	if convErr != nil || port <= 0 {
		return nil, "", err
	}
	for i := 1; i <= 20; i++ {
		tryAddr := net.JoinHostPort(host, strconv.Itoa(port+i))
		if ln2, err2 := net.Listen("tcp", tryAddr); err2 == nil {
			return ln2, ln2.Addr().String(), nil
		}
	}
	return nil, "", err
}

func newServeHandler(root, openCmd string) http.Handler {
	return newServeHandlerWithLSP(root, openCmd, lsp.NewManager(""))
}

func newServeHandlerWithLSP(root, openCmd string, lspMgr *lsp.Manager) http.Handler {
	ws := &webServer{
		root:          root,
		openCmd:       openCmd,
		lsp:           lspMgr,
		externalAllow: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleIndex)
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(webui.Assets()))))
	mux.HandleFunc("/api/status", ws.handleStatus)
	mux.HandleFunc("/api/events", ws.handleEvents)
	mux.HandleFunc("/api/action", ws.handleAction)
	mux.HandleFunc("/api/diff", ws.handleDiff)
	mux.HandleFunc("/api/definition", ws.handleDefinition)
	mux.HandleFunc("/api/references", ws.handleReferences)
	mux.HandleFunc("/api/open-ide", ws.handleOpenIDE)
	return mux
}

func (ws *webServer) loadSnapshot() (map[string]interface{}, *Error) {
	s, err := store.Open(ws.root)
	if err != nil {
		return nil, fail("%v", err)
	}
	ctx := &Ctx{Root: ws.root, Store: s, JSON: true, Actor: "human"}
	_, data, serr := buildStatus(ctx)
	if serr != nil {
		return nil, serr
	}
	data["web"] = map[string]interface{}{
		"mode":             "web",
		"open_ide_enabled": ws.openCmd != "",
	}
	return data, nil
}

func (ws *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	reqName := filepath.Base(ws.root)
	if s, err := store.Open(ws.root); err == nil {
		if dag, derr := s.LoadDAG(); derr == nil && strings.TrimSpace(dag.RequirementName) != "" {
			reqName = dag.RequirementName
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, webui.IndexHTML(reqName))
}

func (ws *webServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	data, err := ws.loadSnapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": formatServeErr(err)})
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (ws *webServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	lastFP := ws.boardFingerprint()
	ticker := time.NewTicker(500 * time.Millisecond)
	keepalive := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		case <-ticker.C:
			fp := ws.boardFingerprint()
			if fp == lastFP {
				continue
			}
			lastFP = fp
			data, err := ws.loadSnapshot()
			if err != nil {
				continue
			}
			raw, merr := json.Marshal(data)
			if merr != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", raw)
			flusher.Flush()
		}
	}
}

func (ws *webServer) boardFingerprint() string {
	dir := filepath.Join(ws.root, store.DirName)
	names := []string{"dag.json", "bindings.json", "config.json", "prd.md", "events.jsonl"}
	var sb strings.Builder
	for _, n := range names {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil {
			sb.WriteString(fmt.Sprintf("%s:%d:%d;", n, st.ModTime().UnixNano(), st.Size()))
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "sources")); err == nil {
		for _, e := range entries {
			if info, ierr := e.Info(); ierr == nil {
				sb.WriteString(fmt.Sprintf("src/%s:%d:%d;", e.Name(), info.ModTime().UnixNano(), info.Size()))
			}
		}
	}
	return sb.String()
}

func (ws *webServer) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	var msg map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&msg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid JSON: " + err.Error()})
		return
	}
	typ := strField(msg, "type")
	args, toast, sourceID, buildErr := ws.buildActionCLIArgs(typ, msg)
	if buildErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": buildErr.Error()})
		return
	}
	if len(args) > 0 {
		if err := ws.execInProcess(args); err != nil {
			snap, _ := ws.loadSnapshot()
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"ok":       false,
				"error":    formatServeErr(err),
				"snapshot": snap,
			})
			return
		}
	}
	snap, serr := ws.loadSnapshot()
	if serr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": formatServeErr(serr)})
		return
	}
	res := map[string]interface{}{
		"ok":       true,
		"toast":    toast,
		"snapshot": snap,
	}
	if sourceID != "" {
		res["source_id"] = sourceID
	}
	writeJSON(w, http.StatusOK, res)
}

func (ws *webServer) execInProcess(argv []string) *Error {
	if len(argv) == 0 {
		return nil
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()

	sub := argv[0]
	rest := append([]string{"--root", ws.root}, argv[1:]...)
	a := ParseArgs(rest)
	prevOut := emitOut
	emitOut = io.Discard
	defer func() { emitOut = prevOut }()
	return dispatchCommand(sub, a, rest)
}

func (ws *webServer) buildActionCLIArgs(typ string, msg map[string]interface{}) ([]string, string, string, error) {
	switch typ {
	case "refresh":
		return nil, "", "", nil
	case "command":
		raw, ok := msg["args"].([]interface{})
		if !ok || len(raw) == 0 {
			return nil, "", "", fmt.Errorf("missing command args")
		}
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			out = append(out, fmt.Sprint(v))
		}
		return out, "已写入 .reqboard/", "", nil
	case "filter.update":
		if boolField(msg, "reset") {
			return []string{"filter", "set", "--reset", "--actor", "human"}, "Diff 过滤已重置", "", nil
		}
		args := []string{"filter", "set", "--actor", "human"}
		if _, ok := msg["excludeTests"]; ok {
			if boolField(msg, "excludeTests") {
				args = append(args, "--exclude-tests", "true")
			} else {
				args = append(args, "--exclude-tests", "false")
			}
		}
		if _, ok := msg["includeGlobs"]; ok {
			args = append(args, "--include", strings.Join(strSliceField(msg, "includeGlobs"), ","))
		}
		if _, ok := msg["ignoreGlobs"]; ok {
			args = append(args, "--exclude", strings.Join(strSliceField(msg, "ignoreGlobs"), ","))
		}
		return args, "Diff 过滤已写入 config.json", "", nil
	case "source.add":
		title := strings.TrimSpace(strField(msg, "title"))
		text := strField(msg, "text")
		if title == "" {
			return nil, "", "", fmt.Errorf("补充来源标题不能为空")
		}
		if strings.TrimSpace(text) == "" {
			return nil, "", "", fmt.Errorf("补充来源正文不能为空")
		}
		id := makeSourceSlug(title)
		kind := strField(msg, "kind")
		if kind == "" {
			kind = "doc"
		}
		args := []string{"source", "add", "--text", text, "--id", id, "--title", title, "--kind", kind, "--actor", "human"}
		if u := strings.TrimSpace(strField(msg, "url")); u != "" {
			args = append(args, "--url", u)
		}
		return args, "补充来源已保存：" + id, id, nil
	case "ref.add":
		title := strings.TrimSpace(strField(msg, "title"))
		if title == "" {
			return nil, "", "", fmt.Errorf("依据标题不能为空")
		}
		kind := strField(msg, "kind")
		if kind == "" {
			kind = "decision"
		}
		args := []string{"ref", "add", strField(msg, "nodeId"), "--title", title, "--kind", kind, "--actor", "human"}
		if u := strings.TrimSpace(strField(msg, "url")); u != "" {
			args = append(args, "--url", u)
		}
		if q := strings.TrimSpace(strField(msg, "quote")); q != "" {
			args = append(args, "--quote", q)
		}
		return args, "外部依据已添加", "", nil
	case "ref.remove":
		return []string{"ref", "remove", strField(msg, "nodeId"), strField(msg, "refId"), "--actor", "human"}, "外部依据已删除", "", nil
	case "todo.add":
		title := strings.TrimSpace(strField(msg, "title"))
		if title == "" {
			return nil, "", "", fmt.Errorf("TODO 标题不能为空")
		}
		kind := strField(msg, "kind")
		if kind == "" {
			kind = "other"
		}
		args := []string{"todo", "add", title, "--kind", kind, "--actor", "human"}
		if note := strings.TrimSpace(strField(msg, "note")); note != "" {
			args = append(args, "--note", note)
		}
		blocks := strSliceField(msg, "blocks")
		if len(blocks) > 0 {
			args = append(args, "--blocks", strings.Join(blocks, ","))
			return args, fmt.Sprintf("TODO 已创建并阻塞 %d 个下游节点", len(blocks)), "", nil
		}
		return args, "TODO 已创建", "", nil
	case "node.create":
		title := strings.TrimSpace(strField(msg, "title"))
		if title == "" {
			title = "新功能点"
		}
		args := []string{"node", "add", "--title", title, "--actor", "human"}
		if sc := strField(msg, "scope"); sc != "" {
			args = append(args, "--scope", sc)
		}
		for _, r := range strSliceField(msg, "prdRefs") {
			args = append(args, "--prd-ref", r)
		}
		if x, ok := numField(msg, "x"); ok {
			args = append(args, "--x", strconv.FormatFloat(x, 'f', -1, 64))
		}
		if y, ok := numField(msg, "y"); ok {
			args = append(args, "--y", strconv.FormatFloat(y, 'f', -1, 64))
		}
		return args, "节点已创建（直写 dag.json）", "", nil
	case "node.update":
		args := []string{"node", "update", strField(msg, "id"), "--actor", "human"}
		p, _ := msg["patch"].(map[string]interface{})
		if p == nil {
			return nil, "", "", nil
		}
		if _, ok := p["title"]; ok {
			args = append(args, "--title", strField(p, "title"))
		}
		if _, ok := p["description"]; ok {
			args = append(args, "--description", strField(p, "description"))
		}
		if _, ok := p["scope"]; ok {
			args = append(args, "--scope", strField(p, "scope"))
		}
		if _, ok := p["status"]; ok {
			args = append(args, "--status", strField(p, "status"))
		}
		if _, ok := p["hold"]; ok {
			args = append(args, "--hold", strconv.FormatBool(boolField(p, "hold")))
		}
		if _, ok := p["stale"]; ok {
			args = append(args, "--stale", strconv.FormatBool(boolField(p, "stale")))
		}
		if _, ok := p["tags"]; ok {
			tags := strSliceField(p, "tags")
			if len(tags) == 0 {
				args = append(args, "--tag", "")
			} else {
				for _, t := range tags {
					args = append(args, "--tag", t)
				}
			}
		}
		if _, ok := p["prd_refs"]; ok {
			refs := strSliceField(p, "prd_refs")
			if len(refs) == 0 {
				args = append(args, "--prd-ref", "")
			} else {
				for _, r := range refs {
					args = append(args, "--prd-ref", r)
				}
			}
		}
		if x, ok := numField(p, "x"); ok {
			args = append(args, "--x", strconv.FormatFloat(x, 'f', -1, 64))
		}
		if y, ok := numField(p, "y"); ok {
			args = append(args, "--y", strconv.FormatFloat(y, 'f', -1, 64))
		}
		if len(args) <= 5 {
			return nil, "", "", nil
		}
		return args, "节点已更新（直写 dag.json）", "", nil
	case "node.deprecate":
		return []string{"node", "deprecate", strField(msg, "id"), "--reason", strField(msg, "reason"), "--actor", "human"}, "节点已标为废弃", "", nil
	case "node.delete":
		args := []string{"node", "delete", strField(msg, "id"), "--actor", "human"}
		if boolField(msg, "force") {
			args = append(args, "--force")
			return args, "节点与 bindings 已强制删除（Ctrl+Z 可撤销）", "", nil
		}
		return args, "节点已删除（Ctrl+Z 可撤销）", "", nil
	case "undo":
		return []string{"undo", "--actor", "human"}, "已撤销 1 步", "", nil
	case "redo":
		return []string{"redo", "--actor", "human"}, "已重做 1 步", "", nil
	case "ops.add":
		title := strings.TrimSpace(strField(msg, "title"))
		if title == "" {
			return nil, "", "", fmt.Errorf("Ops 标题不能为空")
		}
		kind := strField(msg, "kind")
		if kind == "" {
			kind = "other"
		}
		args := []string{"ops", "add", strField(msg, "nodeId"), "--kind", kind, "--title", title, "--actor", "human"}
		if note := strField(msg, "note"); note != "" {
			args = append(args, "--note", note)
		}
		return args, "Ops 已新增", "", nil
	case "ops.toggle":
		return []string{"ops", "toggle", strField(msg, "nodeId"), strField(msg, "opId"), "--actor", "human"}, "Ops 状态已切换", "", nil
	case "ops.remove":
		return []string{"ops", "remove", strField(msg, "nodeId"), strField(msg, "opId"), "--actor", "human"}, "Ops 已删除", "", nil
	case "node.approve":
		return []string{"node", "approve", strField(msg, "id"), "--actor", "human"}, "节点已批准", "", nil
	case "node.reject":
		return []string{"node", "reject", strField(msg, "id"), "--reason", strField(msg, "reason"), "--actor", "human"}, "节点已驳回", "", nil
	case "node.review":
		flag := "--request-changes"
		toast := "节点已打回"
		if boolField(msg, "pass") {
			flag = "--pass"
			toast = "Review 通过"
		}
		args := []string{"node", "review", strField(msg, "id"), flag, "--actor", "human"}
		if note := strField(msg, "note"); note != "" {
			args = append(args, "--note", note)
		}
		if target := strField(msg, "target"); target != "" {
			args = append(args, "--target", target)
		}
		return args, toast, "", nil
	case "node.fail":
		errMsg := strField(msg, "error")
		if errMsg == "" {
			errMsg = "worker failed"
		}
		return []string{"node", "fail", strField(msg, "id"), "--error", errMsg}, "节点已记录失败", "", nil
	case "edge.add":
		kind := strField(msg, "kind")
		if kind == "" {
			kind = "hard"
		}
		args := []string{"edge", "add", "--from", strField(msg, "from"), "--to", strField(msg, "to"), "--kind", kind, "--actor", "human"}
		if note := strField(msg, "note"); note != "" {
			args = append(args, "--note", note)
		}
		return args, "依赖边已新增", "", nil
	case "edge.update":
		args := []string{"edge", "update", strField(msg, "id"), "--actor", "human"}
		if _, ok := msg["kind"]; ok {
			args = append(args, "--kind", strField(msg, "kind"))
		}
		if _, ok := msg["note"]; ok {
			args = append(args, "--note", strField(msg, "note"))
		}
		return args, "依赖边已更新", "", nil
	case "edge.remove":
		return []string{"edge", "remove", strField(msg, "id"), "--actor", "human"}, "依赖边已删除", "", nil
	case "prd.ignore":
		args := []string{"prd", "ignore", strField(msg, "anchor"), "--actor", "human"}
		if boolField(msg, "undo") {
			args = append(args, "--undo")
		}
		return args, "PRD 忽略状态已更新", "", nil
	case "layout":
		args := []string{"layout", "--actor", "human"}
		if boolField(msg, "includeExternal") {
			args = append(args, "--include-external")
			return args, "全图模式：外部/协作者/TODO 已排到最左列", "", nil
		}
		return args, "已按自然拓扑深度重新排版", "", nil
	case "done":
		args := []string{"done", strField(msg, "id"), "--actor", "human"}
		if boolField(msg, "unmock") {
			args = append(args, "--unmock-downstream")
		}
		return args, "节点已标记 Done", "", nil
	default:
		return nil, "", "", fmt.Errorf("unsupported action type %q", typ)
	}
}

type webDiffLine struct {
	Kind    string `json:"kind"` // ctx | add | del
	OldLine int    `json:"old_line"`
	NewLine int    `json:"new_line"`
	Text    string `json:"text"`
}

type webDiffHunk struct {
	Header string        `json:"header"`
	Lines  []webDiffLine `json:"lines"`
}

func (ws *webServer) handleDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repoName := strings.TrimSpace(q.Get("repo"))
	relFile := filepath.ToSlash(strings.TrimSpace(q.Get("file")))
	symbol := strings.TrimSpace(q.Get("symbol"))
	focusLine, _ := strconv.Atoi(q.Get("line"))
	ctxLines, err := strconv.Atoi(q.Get("context"))
	if err != nil || ctxLines < 0 {
		ctxLines = 5
	}
	if repoName == "" || relFile == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "repo and file are required"})
		return
	}

	if repoName == "__external__" {
		absPath := filepath.Clean(filepath.FromSlash(relFile))
		ws.extMu.RLock()
		allowed := ws.externalAllow[absPath]
		ws.extMu.RUnlock()
		if !allowed {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"ok": false, "error": "external file not allowlisted by LSP"})
			return
		}
		raw, rerr := os.ReadFile(absPath)
		if rerr != nil {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": rerr.Error()})
			return
		}
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		wLines := make([]webDiffLine, 0, len(lines))
		for idx, l := range lines {
			wLines = append(wLines, webDiffLine{Kind: "ctx", OldLine: idx + 1, NewLine: idx + 1, Text: l})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":         true,
			"repo":       "__external__",
			"file":       absPath,
			"abs_path":   absPath,
			"base_ref":   "external",
			"symbol":     symbol,
			"focus_line": focusLine,
			"status":     "external",
			"added":      0,
			"deleted":    0,
			"binary":     false,
			"hunks": []webDiffHunk{{
				Header: fmt.Sprintf("@@ -1,%d +1,%d @@ (external dependency — read-only)", len(wLines), len(wLines)),
				Lines:  wLines,
			}},
		})
		return
	}

	s, serr := store.Open(ws.root)
	if serr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": serr.Error()})
		return
	}
	cfg, cerr := s.LoadConfig()
	if cerr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": cerr.Error()})
		return
	}
	var repoPath, baseRef string
	for _, rp := range cfg.Repos {
		if rp.Name == repoName {
			repoPath = rp.Path
			if !filepath.IsAbs(repoPath) {
				repoPath = filepath.Join(ws.root, repoPath)
			}
			repoPath = filepath.Clean(repoPath)
			baseRef = rp.BaseRef
			break
		}
	}
	if repoPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": fmt.Sprintf("repo %q 不在 config.json 中", repoName)})
		return
	}
	absPath := filepath.Clean(filepath.Join(repoPath, filepath.FromSlash(relFile)))
	if absPath != repoPath && !strings.HasPrefix(absPath, repoPath+string(filepath.Separator)) {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid file path outside repo"})
		return
	}

	fd, baseSHA, derr := gitx.CollectFile(repoPath, baseRef, relFile, ctxLines)
	if derr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": derr.Error()})
		return
	}
	if focusLine <= 0 && symbol != "" {
		if raw, rerr := os.ReadFile(absPath); rerr == nil {
			focusLine = findSymbolLine(string(raw), symbol)
		}
	}
	hunks := make([]webDiffHunk, 0, len(fd.Hunks))
	for _, h := range fd.Hunks {
		hunks = append(hunks, convertHunk(h))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"repo":       repoName,
		"file":       relFile,
		"abs_path":   absPath,
		"base_ref":   baseSHA,
		"symbol":     symbol,
		"focus_line": focusLine,
		"status":     fd.Status,
		"added":      fd.Added,
		"deleted":    fd.Deleted,
		"binary":     fd.Binary,
		"hunks":      hunks,
	})
}

func (ws *webServer) handleDefinition(w http.ResponseWriter, r *http.Request) {
	ws.handleSymbolLookup(w, r, false)
}

func (ws *webServer) handleReferences(w http.ResponseWriter, r *http.Request) {
	ws.handleSymbolLookup(w, r, true)
}

func (ws *webServer) handleSymbolLookup(w http.ResponseWriter, r *http.Request, references bool) {
	q := r.URL.Query()
	repoName := strings.TrimSpace(q.Get("repo"))
	relFile := filepath.ToSlash(strings.TrimSpace(q.Get("file")))
	word := strings.TrimSpace(q.Get("word"))
	line, _ := strconv.Atoi(q.Get("line"))
	col, _ := strconv.Atoi(q.Get("col"))
	if repoName == "" || relFile == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "repo and file are required"})
		return
	}

	s, serr := store.Open(ws.root)
	if serr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": serr.Error()})
		return
	}
	cfg, cerr := s.LoadConfig()
	if cerr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": cerr.Error()})
		return
	}

	type repoInfo struct {
		name string
		path string
	}
	var repos []repoInfo
	var repoRoots []string
	var sourceRepoPath string
	for _, rp := range cfg.Repos {
		p := rp.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(ws.root, p)
		}
		p = filepath.Clean(p)
		repos = append(repos, repoInfo{name: rp.Name, path: p})
		repoRoots = append(repoRoots, p)
		if rp.Name == repoName {
			sourceRepoPath = p
		}
	}

	var absPath string
	if repoName == "__external__" {
		absPath = filepath.Clean(filepath.FromSlash(relFile))
		ws.extMu.RLock()
		allowed := ws.externalAllow[absPath]
		ws.extMu.RUnlock()
		if !allowed {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"ok": false, "error": "external file not allowlisted"})
			return
		}
		sourceRepoPath = ws.root
	} else {
		if sourceRepoPath == "" {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": fmt.Sprintf("repo %q 不在 config.json 中", repoName)})
			return
		}
		absPath = filepath.Clean(filepath.Join(sourceRepoPath, filepath.FromSlash(relFile)))
		if absPath != sourceRepoPath && !strings.HasPrefix(absPath, sourceRepoPath+string(filepath.Separator)) {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid file path outside repo"})
			return
		}
	}

	// Prefer the multi-repo workspace root when go.work is present so gopls resolves cross-repo imports.
	lspRoot := sourceRepoPath
	if lsp.LanguageForFile(absPath) == "go" {
		if _, err := os.Stat(filepath.Join(ws.root, "go.work")); err == nil {
			lspRoot = ws.root
		}
	}

	if ws.lsp == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "source": "disabled", "locations": []interface{}{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	var locs []lsp.Location
	var source string
	if references {
		locs, source = ws.lsp.References(ctx, lspRoot, absPath, line, col, word, repoRoots)
	} else {
		locs, source = ws.lsp.Definition(ctx, lspRoot, absPath, line, col, word, repoRoots)
	}

	type outLoc struct {
		Repo    string `json:"repo"`
		File    string `json:"file"`
		AbsPath string `json:"abs_path"`
		Line    int    `json:"line"`
		Col     int    `json:"col"`
		Preview string `json:"preview"`
		Source  string `json:"source"`
	}
	mapped := make([]outLoc, 0, len(locs))
	for _, loc := range locs {
		clean := filepath.Clean(loc.AbsPath)
		matchedRepo := ""
		matchedFile := ""
		for _, rp := range repos {
			if clean == rp.path || strings.HasPrefix(clean, rp.path+string(filepath.Separator)) {
				if rel, rerr := filepath.Rel(rp.path, clean); rerr == nil {
					matchedRepo = rp.name
					matchedFile = filepath.ToSlash(rel)
					break
				}
			}
		}
		if matchedRepo == "" {
			matchedRepo = "__external__"
			matchedFile = filepath.ToSlash(clean)
			ws.extMu.Lock()
			ws.externalAllow[clean] = true
			ws.extMu.Unlock()
		}
		mapped = append(mapped, outLoc{
			Repo:    matchedRepo,
			File:    matchedFile,
			AbsPath: clean,
			Line:    loc.Line,
			Col:     loc.Col,
			Preview: loc.Preview,
			Source:  loc.Source,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":        true,
		"source":    source,
		"locations": mapped,
	})
}

func convertHunk(h gitx.Hunk) webDiffHunk {
	out := webDiffHunk{Header: h.Header, Lines: make([]webDiffLine, 0, len(h.Lines))}
	oldN := h.OldStart
	newN := h.NewStart
	for _, raw := range h.Lines {
		if raw == `\ No newline at end of file` {
			continue
		}
		if strings.HasPrefix(raw, "+") {
			out.Lines = append(out.Lines, webDiffLine{Kind: "add", NewLine: newN, Text: raw[1:]})
			newN++
		} else if strings.HasPrefix(raw, "-") {
			out.Lines = append(out.Lines, webDiffLine{Kind: "del", OldLine: oldN, Text: raw[1:]})
			oldN++
		} else if strings.HasPrefix(raw, " ") {
			out.Lines = append(out.Lines, webDiffLine{Kind: "ctx", OldLine: oldN, NewLine: newN, Text: raw[1:]})
			oldN++
			newN++
		}
	}
	return out
}

func (ws *webServer) handleOpenIDE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	if ws.openCmd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "启动 reqboard serve 时未配置 --open-cmd"})
		return
	}
	var body struct {
		File string `json:"file"`
		Line int    `json:"line"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	target := filepath.Clean(body.File)
	if !ws.pathInConfiguredRepo(target) {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "target file is outside configured repos"})
		return
	}
	if body.Line <= 0 {
		body.Line = 1
	}
	parts := strings.Fields(ws.openCmd)
	if len(parts) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "empty --open-cmd"})
		return
	}
	hasFile := false
	args := make([]string, 0, len(parts))
	for _, tok := range parts[1:] {
		if strings.Contains(tok, "{file}") {
			hasFile = true
		}
		tok = strings.ReplaceAll(tok, "{file}", target)
		tok = strings.ReplaceAll(tok, "{line}", strconv.Itoa(body.Line))
		args = append(args, tok)
	}
	if !hasFile {
		args = append(args, target)
	}
	cmd := exec.Command(parts[0], args...)
	if err := cmd.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	go func() { _ = cmd.Wait() }()
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (ws *webServer) pathInConfiguredRepo(abs string) bool {
	s, err := store.Open(ws.root)
	if err != nil {
		return false
	}
	cfg, err := s.LoadConfig()
	if err != nil {
		return false
	}
	for _, rp := range cfg.Repos {
		p := rp.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(ws.root, p)
		}
		p = filepath.Clean(p)
		if abs == p || strings.HasPrefix(abs, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func findSymbolLine(source, symbol string) int {
	if strings.TrimSpace(symbol) == "" {
		return 0
	}
	lines := strings.Split(source, "\n")
	parts := strings.Split(symbol, ".")
	name := regexp.QuoteMeta(parts[len(parts)-1])
	var cands []*regexp.Regexp
	if len(parts) > 1 {
		recv := regexp.QuoteMeta(parts[len(parts)-2])
		cands = append(cands, regexp.MustCompile(`\bfunc\s*\([^)]*\b`+recv+`\b[^)]*\)\s*`+name+`\b`))
	}
	cands = append(cands,
		regexp.MustCompile(`\bfunc\s+(?:\([^)]*\)\s*)?`+name+`\s*\(`),
		regexp.MustCompile(`\btype\s+`+name+`\b`),
		regexp.MustCompile(`\b(?:struct|service|enum|union|exception|message)\s+`+name+`\b`),
		regexp.MustCompile(`\b(?:const|var)\s+`+name+`\b`),
		regexp.MustCompile(`^\s*`+name+`\b`),
	)
	for i, line := range lines {
		for _, re := range cands {
			if re.MatchString(line) {
				return i + 1
			}
		}
	}
	return 0
}

func makeSourceSlug(title string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
		if b.Len() >= 24 {
			break
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "src"
	}
	return fmt.Sprintf("%s-%s", base, strconv.FormatInt(time.Now().UnixNano()%1000000, 36))
}

func formatServeErr(e *Error) string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Text) != "" {
		return strings.TrimSpace(e.Text)
	}
	return e.Msg
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func strField(m map[string]interface{}, k string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func boolField(m map[string]interface{}, k string) bool {
	v, ok := m[k]
	if !ok || v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return parseBool(fmt.Sprint(v), false)
}

func numField(m map[string]interface{}, k string) (float64, bool) {
	v, ok := m[k]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		f, err := strconv.ParseFloat(fmt.Sprint(v), 64)
		return f, err == nil
	}
}

func strSliceField(m map[string]interface{}, k string) []string {
	v, ok := m[k]
	if !ok || v == nil {
		return nil
	}
	switch raw := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		var out []string
		for _, part := range strings.Split(raw, ",") {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
