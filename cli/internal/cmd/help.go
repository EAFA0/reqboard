package cmd

import "strings"

func subcommandHelp(cmd string, pos []string) string {
	sub := ""
	if len(pos) > 0 {
		sub = pos[0]
	}
	switch cmd {
	case "node":
		switch sub {
		case "list", "ls":
			return `用法: reqboard node list [--scope <mine|external|collaborator|todo|out_of_scope>] [--status <status>] [--stale] [--json]
按紧凑列表输出当前 DAG 节点（含 ID、scope、status、stale、PRD 锚点数、绑定数、标题），避免手写脚本解析 dag.json。
`
		case "add":
			return `用法: reqboard node add --title "<标题>" [--id <slug>] [--description "<说明>"]
                        [--scope mine|external|collaborator|todo|out_of_scope]
                        [--prd-ref <anchor>...] [--tag <t>...] [--x N --y N]
                        [--allow-unknown-refs] [--actor human|agent] [--json]
`
		case "update":
			return `用法: reqboard node update <id> [--title "<标题>"] [--description "<说明>"]
                           [--scope mine|external|collaborator|todo|out_of_scope]
                           [--status pending|in_progress|mocked|implemented|changes_requested|reviewed]
                           [--hold=true|false] [--stale=true|false [--reason "..."]]
                           [--tag <t>...] [--prd-ref <anchor>...] [--x N --y N]
                           [--reset-attempts] [--actor human|agent] [--json]
说明:
  - 不能直接改 status=done（请走 reqboard done <id>）；
  - 非人类不能把已 done 的节点降级重开，也不能清除 stale（需 --actor human 或由人类 Review 打回）。
`
		case "review":
			return `用法: reqboard node review <id> --pass [--actor human|agent] [--json]
      reqboard node review <id> --request-changes --note "<批注>" [--note ...] [--target "<符号>"] [--actor human|agent] [--json]
说明: 含人类写下的未闭环批注时，禁止非人类执行 --pass 自审放行。
`
		case "delete", "rm":
			return `用法: reqboard node delete <id> [--force] [--actor human|agent] [--json]
说明: 节点仍有 bindings 时默认拦截，需 --force 或改用 reqboard node deprecate <id>。
`
		case "deprecate":
			return `用法: reqboard node deprecate <id> [--reason "<原因>"] [--actor human|agent] [--json]
`
		default:
			return `用法: reqboard node <list|add|update|deprecate|delete|approve|reject|review|fail> ...
示例:
  reqboard node list [--scope mine] [--status implemented] [--json]
  reqboard node add --title "..." --scope mine --prd-ref p:1-1
  reqboard node update <id> --status implemented --description "..."
  reqboard node review <id> --pass --actor human
  reqboard node delete <id> [--force]
提示: 可执行 reqboard node <子命令> --help 查看详细参数。
`
		}
	case "edge":
		return `用法: reqboard edge add --from <id> --to <id> [--kind hard|mocked|discovered|stale] [--note "..."] [--json]
      reqboard edge update <edge-id> [--kind hard|mocked|discovered|stale] [--note "..."] [--json]
      reqboard edge remove <edge-id> [--json]
`
	case "bind":
		return `用法: reqboard bind <node-id> --repo <r> --file <f> --symbol <s> [--role impl|mock|test] --why "<理由>" [--partial "<提示>"] [--session <sid>] [--keep-status] [--json]
      reqboard bind <node-id> --repo <r> --file <f> --all-symbols [--role impl|mock|test] --why "<理由>" [--keep-status] [--json]
说明: 仅 scope=mine 允许 bind；生产代码只绑 impl / 手写 mock，不要绑单测。
`
	case "done":
		return `用法: reqboard done <node-id> [--evidence "<证据>"...] [--unmock-downstream] [--force-human --actor human] [--json]
说明: 自动执行 G1(上游齐备) / G2(零 Mock 债) / G3(非 stale 且无 open 批注) / G4(本节点代码干净) 4 道完结门禁。
`
	case "todo":
		return `用法: reqboard todo add "<标题>" [--kind ddl|gateway|flag|other] [--blocks <n1,n2,...>] [--note "..."] [--tag block-start] [--json]
      reqboard todo list [--json]
`
	case "mock":
		return `用法: reqboard mock add <node-id> --desc "<说明>" [--edge <edge-id>] [--json]
      reqboard mock resolve <node-id> --mock <mock-id> [--json]
`
	case "plan":
		return `用法: reqboard plan import <plan.json> [--replace] [--no-layout] [--allow-unknown-refs] [--json]
`
	case "check":
		return `用法: reqboard check [--node <id> | --all] [--json]
`
	case "dispatch":
		return `用法: reqboard dispatch [--max-parallel N] [--session <sid>] [--json]
`
	case "serve":
		return `用法: reqboard serve [--root <dir>] [--addr 127.0.0.1:8787] [--port 8787] [--open-cmd "<cmd>"]
启动本地 Web 三栏看板（内嵌浏览器代码 Diff 查看器，支持 GoLand / VS Code / 任意 IDE 或纯终端用户）。
参数:
  --addr <host:port>   监听地址（默认 127.0.0.1:8787；端口冲突时自动顺延）
  --port <N>           简写监听端口（等价于 --addr 127.0.0.1:<N>）
  --open-cmd <tpl>     可选：点击「在 IDE 打开」时执行的本机命令模板，支持 {file} 和 {line}
                       例如: --open-cmd "goland --line {line} {file}" 或 "code --goto {file}:{line}"
`
	}
	return strings.TrimSpace(usage()) + "\n"
}
