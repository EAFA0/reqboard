# ReqBoard for VS Code

三栏联动需求看板，配合本地 `reqboard` CLI 使用：

- **左栏**：`prd.md` 原文 + 稳定锚点覆盖热力（绿=已覆盖 / 灰=忽略或红线 / 红=Unmapped）。
- **中栏**：主画布默认只渲染我方主干（`mine` / `proposed`），上方 **External Dock** 聚合全部 `external` / `collaborator` 依赖；`out_of_scope` 收进红线胶囊。支持平移缩放、拖拽节点、双击建节点、拖拽连线、编辑边类型/备注、节点状态直改。
- **右栏**：按节点过滤的多仓 Diff 绑定、`Orphan Diff` / `To-Delete` / `Broken Binding` 警戒桶、Review 通过/打回/Done。

## Diff 过滤栏

右栏顶部常驻一条紧凑过滤栏，与 CLI `reqboard filter` 共用 `.reqboard/config.json`：

- **排除单测**：默认勾选；命中内置单测 glob（`*_test.go`、`*.test.ts`、`*.spec.js`、`test_*.py` 等）的文件不计入 Diff 审计。点一下即写配置并刷新。
- **Include / Exclude**：逗号分隔的 glob，回车或点「应用」写入 `config.json`；Include 非空时只审计命中的路径（支持 `repo:path`），Exclude 覆盖默认生成代码规则。
- **重置**：恢复「排除单测 + 默认生成代码 glob」。
- **搜索**：即时按仓库 / 文件 / 符号过滤右栏条目，只影响显示，不写文件。

## 降噪与外部依赖

- **External Dock**：默认展开的紧凑横排条，显示 `外部依赖 done/total`，支持 `全部 / 待就绪 / 已就绪` 过滤。
- 每张外部卡片显示影响我方下游节点数（`→ N`）与 `[✓ Done]` 开关；若存在 `mocked` 出边，会询问是否同时 `--unmock-downstream`（切回 `hard` 并把下游标 `stale`）。
- 主画布节点上的小胶囊：橙色 `mock:xxx`、灰色 `等:xxx`、绿色 `✓xxx`；点击直接编辑对应外部边。
- 悬停/点击外部卡片会高亮所有依赖它的我方节点并淡化其他节点，顶部显示关联边状态与备注；点击我方节点会反向高亮 Dock 中的外部卡片。
- 顶栏 `[↩ 撤销 (N)]` / `[↪ 重做 (N)]` 显示可用步数，支持 `Ctrl+Z` / `Cmd+Z` 与 `Ctrl+Shift+Z` / `Cmd+Shift+Z`；输入框聚焦时不拦截浏览器原生撤销。
- 顶栏 `Ops 清单 (done/total)` 折叠胶囊汇总全需求未完成的 DDL / 网关配置 / Feature Flag，支持直接勾选与跳转所属节点；节点卡片底部显示紧凑 `ops` 徽标。
- 节点编辑弹层与右栏 `[删除]`：无绑定直接删，有绑定弹窗区分「标为废弃」与「强制删除」；选中节点按 `Delete` / `Backspace` 也可删除，误删可 `Ctrl+Z` 还原。
- 顶栏 `[画布含外部]` 可查看全图，此时 `external` / `collaborator` 排到最左 Column 0 源头列；`红线 Out-of-Scope (N)` 胶囊默认折叠，点击展开定位 PRD。
- 点击左栏 PRD 段落会智能定位：命中主画布节点时自动拉近居中并脉冲高亮；纯外部依赖只聚焦 Dock 卡片及其下游，不会把主画布推到空白区；纯红线只展开红线面板。
- 多节点段落会在画布上方显示「关联节点」快捷条，可一键切换聚焦我方 / 外部 / 红线节点。

## 使用

1. 安装：`code --install-extension reqboard-vscode-0.2.0.vsix`
2. 在需求 worktree 运行 `reqboard init` / `prd init` / `plan import`。
3. 命令面板执行 **ReqBoard: Open Board**，选择包含 `.reqboard/` 的目录。

点击文件/符号会优先调用 `focusedGit.openFileComparison`（需安装 focused-git），否则自动降级为 `vscode.diff` 对比 git base 与工作区文件。

Orchestrator session 的跳转链接由 `reqboard.sessionLinkTemplate` 配置（默认留空，只弹提示；可填你的 AI agent CLI 的 session 深链，`{session}` 会替换为 session_id）。

所有修改通过本地 `reqboard` CLI 落盘（flock + 原子 rename + events.jsonl），并监听 `.reqboard/` 热刷新。
