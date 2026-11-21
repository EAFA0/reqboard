# ReqBoard

面向多仓复杂需求与多 coding agent session 并发的本地需求控制面：

- **功能/规则级 DAG**：先锚定 PRD 功能点，再挂载代码；技术方案变了只标 `stale`，骨架不散。
- **双向孤儿拦截**：左栏 `Unmapped PRD`，右栏 `Orphan Diff` / `To-Delete` / `Broken Binding`。
- **Diff 降噪开箱即用**：默认排除单测与生成代码，支持 Include / Exclude glob 白名单；CLI 与 VS Code 右栏共用同一份 `config.json` 过滤配置。
- **不卡开工，只卡完结**：`done` 四道硬门禁（前置完结 / 零 Mock 债 / 非陈旧且无批注 / 代码干净）；`external|collaborator --hard--> mine` 先卡开工，`mocked` 边仍可带 Mock 并行；`todo --hard--> mine` 默认只卡完结，只有带 `block-start` tag 才卡开工。
- **统一前置依赖模型**：`external` / `collaborator` / `todo` 三类前置节点统一收进顶部 Prerequisite Dock，主画布默认只画我方业务主干；`todo` 支持 DDL / 网关配置 / Feature Flag，可一对多阻塞下游并一键 Done 解锁。
- **自然拓扑布局**：主画布按真实依赖深度自然左→右展开，不再把短链硬拉到最右；仅 `milestone` / `e2e` 终结业务场景节点钉在最右终列；全图模式把全部前置节点放 Column 0 源头列。
- **CLI 即规则注入**：`claim` 实时注入 PRD 原文、红线、mocked 备注、Review 批注与人工最新调整。
- **人类直写零延迟**：VS Code 三栏看板直接读写 `.reqboard/`，`fs.watch` 热刷新。
- **误操作可回滚**：`reqboard undo` / `redo` 最多 20 步快照；看板 `Ctrl+Z` / `Cmd+Z` 一键还原，`node delete` 有绑定保护。
- **节点级 Ops 兼容**：旧节点 `ops` 字段仍在节点编辑弹层可勾选；跨节点 DDL / 网关 / Flag 建议改用一等 `todo` 节点。

## 30 秒上手

```bash
# 1. 构建 CLI（纯 Go 标准库，无外部依赖）
cd cli && go build -o ../bin/reqboard . && cd ..
ln -sf "$PWD/bin/reqboard" ~/.local/bin/reqboard

# 2. 安装 VS Code 扩展
code --install-extension vscode-ext/reqboard-vscode-0.2.1.vsix

# 3. 在下一个需求 worktree 初始化
cd <requirement-worktree>
reqboard init --name <需求名>            # 自动扫描 git 子仓；无子仓时进入规划模式
reqboard repo add --name <n> --path <dir> --base <ref>  # 规划后按需挂载代码仓
reqboard prd init <src.md>               # 注入 <!-- @p:... --> 稳定锚点
reqboard plan import plan.json           # 功能级 DAG + PRD 覆盖率
reqboard status                          # 看板快照
reqboard filter show                     # 查看当前 Diff 过滤（默认排除单测）
reqboard filter set --include 'src/**, *.go' --exclude 'vendor/**' --exclude-tests=true

# 4. 前置依赖（外部 / 协作者 / 工程 TODO）
reqboard todo add "DDL: orders add confirmation_mode" --kind ddl --blocks rb-a,rb-b --note "上线前执行"
reqboard todo list                       # 查看全部 TODO、完成状态与阻塞下游
reqboard done <todo-id>                  # 跳过代码门禁，直接 Done 并解锁下游
reqboard done <external-id> --unmock-downstream   # mocked 出边切 hard，下游 mine 标 stale

# 5. 启动 /goal 协调者（coding agent）
/goal 读取 .reqboard/ 当前状态，循环执行 `reqboard dispatch` 并按 P0→P1→P2 清单并发派发 Worker；
每个 Worker 只领 1 个 node_id，走 claim → 改码 → bind → check --node → done；
无项时输出 QUIESCENT 阻塞账单并停止。
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

Demo 含 2 个真实 Go 仓库（`order_api` / `order_service`，各有 `master` 与 `feature/order-visibility`）、自然拓扑 DAG、`out_of_scope` 红线、`proposed` 待批节点、橙色 `mocked` 边、一条 `DDL: orders add confirmation_mode` 的 1→2 阻塞 TODO、`Orphan Diff`、`To-Delete` 残骸与 `Unmapped PRD`。

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
