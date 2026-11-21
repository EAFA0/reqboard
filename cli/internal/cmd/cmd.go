// Package cmd implements the reqboard command line surface.
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"reqboard/internal/model"
	"reqboard/internal/store"
)

// Error is a CLI failure with an exit code and optional structured payload.
type Error struct {
	Code   int
	Msg    string
	Text   string
	Data   interface{}
	Silent bool
}

func (e *Error) Error() string { return e.Msg }

func fail(format string, args ...interface{}) *Error {
	return &Error{Code: 1, Msg: fmt.Sprintf(format, args...)}
}

// Args is a tiny interspersed flag parser (stdlib flag stops at the first
// positional argument, which is hostile for `node update <id> --title x`).
type Args struct {
	pos   []string
	flags map[string][]string
}

var boolFlags = map[string]bool{
	"json": true, "all": true, "pass": true, "request-changes": true,
	"force-human": true, "auto": true, "replace": true, "help": true,
	"undo": true, "strict": true, "resolve": true, "merge": true,
	"allow-unknown-refs": true, "no-layout": true, "force": true,
	"reset-attempts": true, "auto-layout": true, "unmock-downstream": true,
}

// ParseArgs parses flags and positionals in any order.
func ParseArgs(args []string) *Args {
	a := &Args{flags: map[string][]string{}}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			a.pos = append(a.pos, args[i+1:]...)
			break
		}
		if tok == "-h" || tok == "-help" {
			a.flags["help"] = append(a.flags["help"], "true")
			continue
		}
		if !strings.HasPrefix(tok, "--") {
			a.pos = append(a.pos, tok)
			continue
		}
		body := strings.TrimPrefix(tok, "--")
		key, val, hasVal := strings.Cut(body, "=")
		if !hasVal {
			if boolFlags[key] {
				val = "true"
			} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				val = args[i+1]
				i++
			} else {
				val = "true"
			}
		}
		a.flags[key] = append(a.flags[key], val)
	}
	return a
}

// Bool reports whether a flag is present/truthy.
func (a *Args) Bool(name string) bool {
	vs, ok := a.flags[name]
	if !ok || len(vs) == 0 {
		return false
	}
	v := strings.ToLower(vs[len(vs)-1])
	return v == "" || v == "true" || v == "1" || v == "yes"
}

// Has reports whether a flag was supplied at all.
func (a *Args) Has(name string) bool {
	_, ok := a.flags[name]
	return ok
}

// Str returns the last value of a flag.
func (a *Args) Str(name string) string {
	vs, ok := a.flags[name]
	if !ok || len(vs) == 0 {
		return ""
	}
	return vs[len(vs)-1]
}

// Strs returns all values of a repeatable flag.
func (a *Args) Strs(name string) []string {
	return a.flags[name]
}

// Int parses an int flag with a default.
func (a *Args) Int(name string, def int) int {
	s := a.Str(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// Pos returns positional arguments.
func (a *Args) Pos() []string { return a.pos }

// Ctx is shared command context.
type Ctx struct {
	Root  string
	Store *store.Store
	JSON  bool
	Actor string
}

// NewCtx opens an existing workspace.
func NewCtx(a *Args) (*Ctx, *Error) {
	jsonOut := a.Bool("json")
	actor := a.Str("actor")
	if actor == "" {
		actor = "agent"
	}
	switch actor {
	case "human", "agent", "cli", "planner", "orchestrator", "worker":
	default:
		return nil, fail("invalid --actor %q (human|agent|cli)", actor)
	}
	root := a.Str("root")
	var err error
	if root == "" {
		wd, _ := os.Getwd()
		root, err = store.Find(wd)
		if err != nil {
			return nil, &Error{Code: 1, Msg: err.Error(), Text: err.Error() + "\n  -> 在需求 worktree 根目录运行，或 `reqboard init --root <dir>`"}
		}
	} else {
		root = normalizeRoot(root)
		if _, statErr := os.Stat(root); statErr != nil {
			return nil, fail("root not found: %s", root)
		}
	}
	s, err := store.Open(root)
	if err != nil {
		return nil, &Error{Code: 1, Msg: err.Error()}
	}
	return &Ctx{Root: root, Store: s, JSON: jsonOut, Actor: actor}, nil
}

func normalizeRoot(root string) string {
	if strings.HasSuffix(strings.TrimRight(root, "/"), "/"+store.DirName) {
		root = strings.TrimSuffix(strings.TrimRight(root, "/"), "/"+store.DirName)
	}
	return root
}

// Emit prints text or JSON.
func (c *Ctx) Emit(text string, data interface{}) {
	if c.JSON {
		out, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			fmt.Printf("{\"ok\":false,\"error\":%q}\n", err.Error())
			return
		}
		fmt.Println(string(out))
		return
	}
	if text != "" {
		fmt.Print(text)
		if !strings.HasSuffix(text, "\n") {
			fmt.Println()
		}
	}
}

// SaveDAG persists a mutated DAG with a revision bump and event.
func (c *Ctx) SaveDAG(d *model.DAG, action, nodeID, detail string) *Error {
	d.Touch()
	if err := c.Store.SaveDAG(d); err != nil {
		return fail("save dag.json: %v", err)
	}
	_ = c.Store.AppendEvent(store.Event{Actor: c.Actor, Action: action, NodeID: nodeID, Detail: detail})
	return nil
}

// SaveBindings persists bindings.json.
func (c *Ctx) SaveBindings(b *model.Bindings, action, nodeID, detail string) *Error {
	if err := c.Store.SaveBindings(b); err != nil {
		return fail("save bindings.json: %v", err)
	}
	_ = c.Store.AppendEvent(store.Event{Actor: c.Actor, Action: action, NodeID: nodeID, Detail: detail})
	return nil
}

// WithLock runs a read-modify-write under flock.
func (c *Ctx) WithLock(fn func() *Error) *Error {
	var out *Error
	err := c.Store.WithLock(func() error {
		out = fn()
		return nil
	})
	if err != nil {
		return fail("lock .reqboard: %v", err)
	}
	return out
}

// Main is the CLI entrypoint. It returns a process exit code.
func Main(argv []string) int {
	if len(argv) == 0 {
		fmt.Print(usage())
		return 0
	}
	if argv[0] == "-h" || argv[0] == "--help" || argv[0] == "help" {
		fmt.Print(usage())
		return 0
	}
	if argv[0] == "version" || argv[0] == "--version" {
		fmt.Println("reqboard 0.1.3")
		return 0
	}

	cmd := argv[0]
	rest := argv[1:]
	a := ParseArgs(rest)
	jsonOut := a.Bool("json")

	var err *Error
	switch cmd {
	case "init":
		err = runInit(rest)
	case "repo":
		err = runRepo(a, rest)
	case "prd":
		err = runPRD(a, rest)
	case "plan":
		err = runPlan(a, rest)
	case "node":
		err = runNode(a, rest)
	case "edge":
		err = runEdge(a, rest)
	case "claim":
		err = runClaim(a, rest)
	case "bind":
		err = runBind(a, rest)
	case "unbind":
		err = runUnbind(a, rest)
	case "propose":
		err = runPropose(a, rest)
	case "mock":
		err = runMock(a, rest)
	case "check":
		err = runCheck(a, rest)
	case "review-pack":
		err = runReviewPack(a, rest)
	case "done":
		err = runDone(a, rest)
	case "dispatch":
		err = runDispatch(a, rest)
	case "status":
		err = runStatus(a, rest)
	case "filter":
		err = runFilter(a, rest)
	case "layout":
		err = runLayout(a, rest)
	default:
		err = &Error{Code: 2, Msg: fmt.Sprintf("unknown command %q", cmd), Text: usage()}
	}

	if err == nil {
		return 0
	}
	if err.Silent {
		// The handler already emitted its report (e.g. check with violations).
	} else if jsonOut {
		payload := map[string]interface{}{
			"ok":    false,
			"error": err.Msg,
			"code":  err.Code,
		}
		if err.Data != nil {
			payload["data"] = err.Data
		}
		out, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(out))
	} else {
		if err.Text != "" {
			fmt.Fprintln(os.Stderr, strings.TrimRight(err.Text, "\n"))
		} else {
			fmt.Fprintln(os.Stderr, "[REQBOARD ERROR] "+err.Msg)
		}
	}
	if err.Code == 0 {
		return 1
	}
	return err.Code
}

func usage() string {
	return `reqboard — 本地需求控制面 CLI

用法:
  reqboard init [--root <dir>] [--name <name>] [--repo <name=path:base>...]
  reqboard repo list | repo add --name <n> --path <dir> [--base <ref>] | repo remove <name> [--force]
  reqboard prd init <src.md> | prd sync <new.md> | prd ignore <p:x> [<p:y>...] [--undo]
  reqboard plan import <plan.json> [--replace] [--no-layout] [--allow-unknown-refs]
  reqboard node add|update|deprecate|approve|reject|review|fail ...
  reqboard edge add|update|remove ...
  reqboard claim <id> [--session <sid>] [--lease <repo:glob>...]
  reqboard bind <id> --repo <r> --file <f> --symbol <s> [--partial <hint>] [--role impl|mock|test] --why <reason> [--session <sid>]
  reqboard unbind <binding-id>
  reqboard propose --from <id> --prd-ref <p:x> --title <t> --reason <r>
  reqboard mock add|resolve <id> --desc <d> [--edge <eid>]
  reqboard check [--node <id> | --all] [--json]
  reqboard review-pack <id> [--json]
  reqboard done <id> [--evidence <e>...] [--force-human] [--unmock-downstream]
  reqboard dispatch [--max-parallel N] [--session <sid>] [--json]
  reqboard status [--json]
  reqboard filter show [--json]
  reqboard filter set [--include <comma-globs>] [--exclude <comma-globs>] [--exclude-tests=true|false] [--reset]
  reqboard layout [--auto]

所有命令支持 --json；写操作自动 flock + 原子 rename + events.jsonl 追加。
`
}

func (c *Ctx) event(action, nodeID, detail string) store.Event {
	return store.Event{Actor: c.Actor, Action: action, NodeID: nodeID, Detail: detail}
}
