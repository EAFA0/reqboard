// Package lsp provides a lightweight, zero-dependency JSON-RPC 2.0 LSP client
// manager for `reqboard serve`, supporting any stdio language server (gopls,
// typescript-language-server, pyright-langserver, rust-analyzer, clangd, etc.)
// with an automatic workspace symbol fallback when no language server is present.
package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Location is a 1-based jump destination returned by LSP or fallback search.
type Location struct {
	AbsPath string `json:"abs_path"`
	Line    int    `json:"line"` // 1-based
	Col     int    `json:"col"`  // 1-based
	Preview string `json:"preview,omitempty"`
	Source  string `json:"source"` // "lsp" | "fallback"
}

// Manager lazily spawns and reuses stdio LSP servers keyed by (root, language).
type Manager struct {
	mu      sync.Mutex
	servers map[string]*server
	custom  map[string][]string // languageID -> argv
}

// NewManager creates an LSP manager. spec is an optional comma-separated list
// like "go=gopls,python=pyright-langserver --stdio,typescript=typescript-language-server --stdio".
func NewManager(spec string) *Manager {
	m := &Manager{
		servers: map[string]*server{},
		custom:  map[string][]string{},
	}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		lang, cmdStr, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		lang = normalizeLang(lang)
		argv := strings.Fields(strings.TrimSpace(cmdStr))
		if lang != "" && len(argv) > 0 {
			m.custom[lang] = argv
		}
	}
	return m
}

// Shutdown cleanly terminates all running LSP subprocesses.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.servers {
		s.close()
		delete(m.servers, k)
	}
}

// Definition resolves definitions for (absPath, line 1-based, col 1-based).
// If no LSP is available or LSP fails, it falls back to lightweight workspace
// symbol scanning using word.
func (m *Manager) Definition(ctx context.Context, workspaceRoot, absPath string, line, col int, word string, repoRoots []string) ([]Location, string) {
	lang := LanguageForFile(absPath)
	if lang != "" {
		if srv := m.getOrStart(workspaceRoot, lang); srv != nil {
			locs, err := srv.definition(ctx, absPath, line, col)
			if err == nil && len(locs) > 0 {
				return locs, "lsp:" + lang
			}
		}
	}
	if strings.TrimSpace(word) != "" {
		locs := fallbackDefinition(word, absPath, repoRoots)
		if len(locs) > 0 {
			return locs, "fallback"
		}
	}
	return nil, "none"
}

// AvailableLanguages reports detected or configured language server commands.
func (m *Manager) AvailableLanguages() map[string]string {
	out := map[string]string{}
	langs := []string{"go", "typescript", "javascript", "python", "rust", "cpp", "java"}
	for k := range m.custom {
		langs = append(langs, k)
	}
	seen := map[string]bool{}
	for _, l := range langs {
		if seen[l] {
			continue
		}
		seen[l] = true
		if argv := m.resolveCommand(l); len(argv) > 0 {
			out[l] = strings.Join(argv, " ")
		}
	}
	return out
}

func (m *Manager) getOrStart(root, lang string) *server {
	argv := m.resolveCommand(lang)
	if len(argv) == 0 {
		return nil
	}
	key := filepath.Clean(root) + "|" + lang
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.servers[key]; ok && !s.isClosed() {
		return s
	}
	s, err := startServer(root, lang, argv)
	if err != nil {
		return nil
	}
	m.servers[key] = s
	return s
}

func (m *Manager) resolveCommand(lang string) []string {
	lang = normalizeLang(lang)
	if argv, ok := m.custom[lang]; ok {
		if bin := findExecutable(argv[0]); bin != "" {
			cp := append([]string{bin}, argv[1:]...)
			return cp
		}
		return nil
	}
	var candidates [][]string
	switch lang {
	case "go":
		candidates = [][]string{{"gopls"}}
	case "typescript", "javascript":
		candidates = [][]string{{"typescript-language-server", "--stdio"}}
	case "python":
		candidates = [][]string{{"pyright-langserver", "--stdio"}, {"basedpyright-langserver", "--stdio"}, {"pylsp"}}
	case "rust":
		candidates = [][]string{{"rust-analyzer"}}
	case "c", "cpp":
		candidates = [][]string{{"clangd"}}
	case "java":
		candidates = [][]string{{"jdtls"}}
	}
	for _, c := range candidates {
		if bin := findExecutable(c[0]); bin != "" {
			return append([]string{bin}, c[1:]...)
		}
	}
	return nil
}

func findExecutable(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	extras := []string{
		filepath.Join(home, "go", "bin", name),
		filepath.Join(home, ".local", "bin", name),
		filepath.Join(home, ".cargo", "bin", name),
	}
	for _, p := range extras {
		if st, serr := os.Stat(p); serr == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// LanguageForFile maps common source file extensions to LSP language IDs.
func LanguageForFile(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py", ".pyi":
		return "python"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh":
		return "cpp"
	case ".java":
		return "java"
	default:
		return ""
	}
}

func normalizeLang(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "go", "golang":
		return "go"
	case "ts", "tsx", "typescript":
		return "typescript"
	case "js", "jsx", "javascript":
		return "javascript"
	case "py", "python":
		return "python"
	case "rs", "rust":
		return "rust"
	case "c":
		return "c"
	case "cpp", "cc", "cxx", "c++":
		return "cpp"
	case "java":
		return "java"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

/* ---------------- JSON-RPC 2.0 stdio LSP server ---------------- */

type server struct {
	root   string
	lang   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	wmu    sync.Mutex
	mu     sync.Mutex
	nextID int
	waiter map[int]chan rpcResponse
	opened map[string]int64 // uri -> mtime unix nano
	closed bool
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func startServer(root, lang string, argv []string) (*server, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &server{
		root:   root,
		lang:   lang,
		cmd:    cmd,
		stdin:  stdin,
		waiter: map[int]chan rpcResponse{},
		opened: map[string]int64{},
	}
	go s.readLoop(stdout)

	initCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rootURI := fileURI(root)
	_, err = s.call(initCtx, "initialize", map[string]interface{}{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"workspaceFolders": []map[string]string{
			{"uri": rootURI, "name": filepath.Base(root)},
		},
		"capabilities": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"definition": map[string]interface{}{"linkSupport": true},
			},
		},
	})
	if err != nil {
		s.close()
		return nil, err
	}
	_ = s.notify("initialized", map[string]interface{}{})
	return s, nil
}

func (s *server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *server) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for id, ch := range s.waiter {
		close(ch)
		delete(s.waiter, id)
	}
	s.mu.Unlock()
	_ = s.stdin.Close()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
}

func (s *server) readLoop(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		contentLen := -1
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				s.close()
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				n, _ := strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
				contentLen = n
			}
		}
		if contentLen <= 0 {
			continue
		}
		body := make([]byte, contentLen)
		if _, err := io.ReadFull(br, body); err != nil {
			s.close()
			return
		}
		var env struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			continue
		}
		if env.ID != nil && env.Method == "" {
			s.mu.Lock()
			ch := s.waiter[*env.ID]
			delete(s.waiter, *env.ID)
			s.mu.Unlock()
			if ch != nil {
				ch <- rpcResponse{Result: env.Result, Error: env.Error}
			}
		} else if env.ID != nil && env.Method != "" {
			// Respond with null result to server-to-client requests (e.g. client/registerCapability).
			_ = s.writeJSON(map[string]interface{}{"jsonrpc": "2.0", "id": *env.ID, "result": nil})
		}
	}
}

func (s *server) writeJSON(v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(raw))
	buf.Write(raw)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.stdin.Write(buf.Bytes())
	return err
}

func (s *server) notify(method string, params interface{}) error {
	return s.writeJSON(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (s *server) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("lsp closed")
	}
	s.nextID++
	id := s.nextID
	ch := make(chan rpcResponse, 1)
	s.waiter[id] = ch
	s.mu.Unlock()

	if err := s.writeJSON(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		s.mu.Lock()
		delete(s.waiter, id)
		s.mu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.waiter, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("lsp closed")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("lsp error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (s *server) ensureOpen(absPath string) error {
	st, err := os.Stat(absPath)
	if err != nil {
		return err
	}
	uri := fileURI(absPath)
	mtime := st.ModTime().UnixNano()
	s.mu.Lock()
	prev, ok := s.opened[uri]
	s.mu.Unlock()
	if ok && prev == mtime {
		return nil
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	if ok {
		_ = s.notify("textDocument/didClose", map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
		})
	}
	if err := s.notify("textDocument/didOpen", map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":        uri,
			"languageId": s.lang,
			"version":    1,
			"text":       string(raw),
		},
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.opened[uri] = mtime
	s.mu.Unlock()
	return nil
}

func (s *server) definition(ctx context.Context, absPath string, line, col int) ([]Location, error) {
	if err := s.ensureOpen(absPath); err != nil {
		return nil, err
	}
	l := line - 1
	if l < 0 {
		l = 0
	}
	c := col - 1
	if c < 0 {
		c = 0
	}
	res, err := s.call(ctx, "textDocument/definition", map[string]interface{}{
		"textDocument": map[string]string{"uri": fileURI(absPath)},
		"position":     map[string]int{"line": l, "character": c},
	})
	if err != nil {
		return nil, err
	}
	return parseDefinitionResult(res), nil
}

func parseDefinitionResult(raw json.RawMessage) []Location {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	type lspPos struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	}
	type lspRange struct {
		Start lspPos `json:"start"`
	}
	type lspLoc struct {
		URI                  string    `json:"uri"`
		Range                lspRange  `json:"range"`
		TargetURI            string    `json:"targetUri"`
		TargetSelectionRange *lspRange `json:"targetSelectionRange"`
		TargetRange          *lspRange `json:"targetRange"`
	}
	var items []lspLoc
	if trimmed[0] == '[' {
		_ = json.Unmarshal(trimmed, &items)
	} else {
		var single lspLoc
		if err := json.Unmarshal(trimmed, &single); err == nil {
			items = append(items, single)
		}
	}
	seen := map[string]bool{}
	var out []Location
	for _, it := range items {
		u := it.TargetURI
		r := lspRange{}
		if u != "" {
			if it.TargetSelectionRange != nil {
				r = *it.TargetSelectionRange
			} else if it.TargetRange != nil {
				r = *it.TargetRange
			}
		} else {
			u = it.URI
			r = it.Range
		}
		abs := uriToPath(u)
		if abs == "" {
			continue
		}
		ln := r.Start.Line + 1
		col := r.Start.Character + 1
		key := fmt.Sprintf("%s:%d:%d", abs, ln, col)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Location{
			AbsPath: abs,
			Line:    ln,
			Col:     col,
			Preview: readLinePreview(abs, ln),
			Source:  "lsp",
		})
	}
	return out
}

func fileURI(absPath string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Clean(absPath))}
	return u.String()
}

func uriToPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p, err := url.PathUnescape(u.Path)
	if err != nil {
		p = u.Path
	}
	return filepath.Clean(filepath.FromSlash(p))
}

func readLinePreview(absPath string, line int) string {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

/* ---------------- Lightweight multi-language fallback ---------------- */

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "kitex_gen": true,
	"dist": true, "build": true, ".reqboard": true, "__pycache__": true, "target": true,
}

func fallbackDefinition(word, currentFile string, repoRoots []string) []Location {
	name := strings.TrimSpace(word)
	if len(name) < 2 {
		return nil
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	q := regexp.QuoteMeta(name)
	re := regexp.MustCompile(
		`(?:\bfunc\s+(?:\([^)]*\)\s*)?` + q + `\s*\(|\b(?:type|struct|interface|service|enum|class|def|fn|trait|union|exception|message)\s+` + q + `\b|\b(?:const|var|let)\s+` + q + `\b|^\s*` + q + `\s*(?:=|:[^=]))`,
	)
	var out []Location
	seen := map[string]bool{}
	scanFile := func(p string) {
		if len(out) >= 20 {
			return
		}
		raw, err := os.ReadFile(p)
		if err != nil || len(raw) > 512*1024 || bytes.IndexByte(raw, 0) >= 0 {
			return
		}
		for idx, line := range strings.Split(string(raw), "\n") {
			if re.MatchString(line) {
				key := fmt.Sprintf("%s:%d", p, idx+1)
				if !seen[key] {
					seen[key] = true
					out = append(out, Location{
						AbsPath: p,
						Line:    idx + 1,
						Col:     1,
						Preview: strings.TrimSpace(line),
						Source:  "fallback",
					})
				}
				if len(out) >= 20 {
					return
				}
			}
		}
	}

	// Prioritize current file first, then same directory, then workspace repos.
	if currentFile != "" {
		scanFile(currentFile)
		dir := filepath.Dir(currentFile)
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					p := filepath.Join(dir, e.Name())
					if p != currentFile {
						scanFile(p)
					}
				}
			}
		}
	}
	for _, root := range repoRoots {
		if len(out) >= 20 {
			break
		}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || len(out) >= 20 {
				return filepath.SkipDir
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if p == currentFile {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			switch ext {
			case ".go", ".thrift", ".proto", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".c", ".h", ".cpp", ".hpp":
				scanFile(p)
			}
			return nil
		})
	}
	return out
}
