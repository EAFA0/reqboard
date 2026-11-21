# ReqBoard

面向多仓复杂需求与多 coding agent session 并发的本地需求控制面：

- **功能/规则级 DAG**：先锚定 PRD 功能点，再挂载代码；技术方案变了只标 `stale`，骨架不散。
- **双向孤儿拦截**：左栏 `Unmapped PRD`，右栏 `Orphan Diff` / `To-Delete` / `Broken Binding`。
- **Diff 降噪开箱即用**：默认排除单测与生成代码，支持 Include / Exclude glob 白名单；CLI 与 VS Code 右栏共用同一份 `config.json` 过滤配置。
- **不卡开工，只卡完结**：`done` 四道硬门禁（前置完结 / 零 Mock 债 / 非陈旧且无批注 / 代码干净）；但外部 `hard` 依赖会先卡开工，`mocked` 边仍可带 Mock 并行。
- **外部依赖聚合降噪**：主画布默认只画我方主干，`external` / `collaborator` 收进 External Dock，`out_of_scope` 收进红线胶囊；外部就绪可一键 `--unmock-downstream`。
- **CLI 即规则注入**：`claim` 实时注入 PRD 原文、红线、mocked 备注、Review 批注与人工最新调整。
- **人类直写零延迟**：VS Code 三栏看板直接读写 `.reqboard/`，`fs.watch` 热刷新。

## 30 秒上手

```bash
# 1. 构建 CLI（纯 Go 标准库，无外部依赖）
cd cli && go build -o ../bin/reqboard . && cd ..
ln -sf "$PWD/bin/reqboard" ~/.local/bin/reqboard

# 2. 安装 VS Code 扩展
code --install-extension vscode-ext/reqboard-vscode-0.1.3.vsix

# 3. 在下一个需求 worktree 初始化
cd <requirement-worktree>
reqboard init --name <需求名>            # 自动扫描 git 子仓；无子仓时进入规划模式
reqboard repo add --name <n> --path <dir> --base <ref>  # 规划后按需挂载代码仓
reqboard prd init <src.md>               # 注入 <!-- @p:... --> 稳定锚点
reqboard plan import plan.json           # 功能级 DAG + PRD 覆盖率
reqboard status                          # 看板快照
reqboard filter show                     # 查看当前 Diff 过滤（默认排除单测）
reqboard filter set --include 'src/**, *.go' --exclude 'vendor/**' --exclude-tests=true

# 4. 启动 /goal 协调者（coding agent）
/goal 读取 .reqboard/ 当前状态，循环执行 `reqboard dispatch` 并按 P0→P1→P2 清单并发派发 Worker；
每个 Worker 只领 1 个 node_id，走 claim → 改码 → bind → check --node → done；
无项时输出 QUIESCENT 阻塞账单并停止。

# 5. 外部依赖就绪时（可选一键拆 Mock）
reqboard done <external-id> --unmock-downstream   # mocked 出边切 hard，下游 mine 标 stale
```

VS Code 命令面板执行 **ReqBoard: Open Board**，选择包含 `.reqboard/` 的目录。点击文件/符号优先调用 `focusedGit.openFileComparison`，未安装 focused-git 时自动降级 `vscode.diff`。

## Demo（可直接体验）

```bash
demo/setup.sh
cd demo
reqboard status
reqboard check --all          # Orphan Diff + To-Delete
reqboard dispatch --max-parallel 6
reqboard done rb-precheck     # 4 道门禁拦截
```

Demo 含 2 个真实 Go 仓库（`order_api` / `order_service`，各有 `master` 与 `feature/order-visibility`）、12 节点左细右粗 DAG、`out_of_scope` 红线、`proposed` 待批节点、橙色 `mocked` 边、`Orphan Diff`、`To-Delete` 残骸与 `Unmapped PRD`。

## 目录

| 路径 | 说明 |
| :--- | :--- |
| `cli/` | Go 1.19 纯标准库 CLI（AST/Hunk 对账、门禁、dispatch） |
| `vscode-ext/` | TypeScript 三栏看板扩展 + 已打包 `.vsix` |
| `skill/SKILL.md` | Planner / Goal Orchestrator / Node Worker 薄 Skill |
| `demo/` | 多仓 Go Demo 沙箱（`setup.sh` 可重建） |
| `docs/DESIGN.md` | 架构与协议设计稿 |

## 验证

```bash
cd cli && go test ./...
cd vscode-ext && npm test
```
