# ReqBoard for VS Code

三栏联动需求看板，配合本地 `reqboard` CLI 使用：

- **左栏**：多来源「需求依据」视图。顶部 Tab 栏为 `[主 PRD]` + 各补充来源（显示标题与被引用段数）+ `[+ 添加来源]`；主 PRD 保留绿/灰/红覆盖热力，补充来源只把被节点引用的段落标淡绿，未引用段落保持安静灰条（绝不标红 `Unmapped`）。
- **中栏**：主画布默认只渲染我方业务主干（`mine` / `proposed`），按自然拓扑深度左→右展开；仅 `milestone` / `e2e` 终结业务场景节点钉在最右终列。上方 **Prerequisite Dock** 统一收纳 `external` / `collaborator` / `todo`；`out_of_scope` 收进红线胶囊。支持平移缩放、拖拽节点、双击建节点、拖拽连线、编辑边类型/备注、节点状态直改。
- **右栏**：按节点过滤的多仓 Diff 绑定、`Orphan Diff` / `To-Delete` / `Broken Binding` 警戒桶、外部依据 / 拍板摘录（Refs）增删、Review 通过/打回/Done。

## 多来源需求依据

- **补充来源**：点 `[+ 添加来源]` 弹出轻量浮层，可直接粘贴会议纪要/群聊拍板文本（填标题、类型 `meeting|chat|doc|spec`、可选 URL）立即保存为 `.reqboard/sources/<id>.md`；也可通过 VS Code 原生文件选择器导入本地 Markdown。CLI 等价命令：`reqboard source add ... --id <slug>`（支持 `--text` 或 `--file -` 从 stdin 粘贴）。
- **Tab 联动**：点击右栏 `@<source_id>:...` 锚点、画布节点上的补充来源微标，或选中 `prd_refs` 含补充锚点的节点，左栏会自动切到对应来源 Tab 并滚动高亮该段。
- **段落动作**：补充来源每段悬浮显示 `[+ 建节点]` 与 `[+ 挂到当前节点]`；后者一键把该锚点追加到当前选中节点的 `prd_refs`。
- **Refs**：右栏与节点编辑弹层都有「外部依据 / 拍板摘录」区，支持 `url|meeting|decision|chat` 类型、标题、可选 URL、可选原话摘录；可点击外链、逐条删除。画布节点卡片若挂有 refs 或补充来源锚点，会显示 `ref N` 微标。
- **覆盖率口径**：只有主 PRD 参与强制覆盖考核；补充来源未引用段落不会产生 `Unmapped` 噪声。

## 前置依赖 Dock

顶部统一停靠栏把外部依赖与工程 TODO 收敛到一处，主画布保持 100% 纯业务功能树：

- **顶条**：`前置依赖 done/total`、类型筛选 `[全部] [外部] [TODO]`、状态筛选 `[待就绪] [已就绪]`、`[+ 新增 TODO]`、`[隐藏/展开]`，右侧是折叠的 `红线 Out-of-Scope (N)` 胶囊。
- **新增 TODO**：极简浮层输入标题、选择类型（`DDL` / `网关` / `Flag` / `其他`）、可选勾选要阻塞的下游我方节点（留空即独立需求级 TODO），回车创建并自动连 `hard` 边。
- **TODO 卡片**：类型色标（`DDL` / `网关` / `Flag` / `TODO`）、标题、影响我方下游数 `→ N`、`[✓ Done]` 勾选钮；点击卡片可编辑标题/类型/备注、切换 `block-start` 或删除。
- **悬停/点击联动**：被该前置阻塞的下游节点高亮发光，其余节点淡化；点击我方节点会反向高亮 Dock 中对应的前置卡片。
- **节点胶囊**：主画布节点卡片显示所挂前置，如 `等:DDL`、`✓DDL`、`mock:Auth`；点击直接编辑该条连线。
- 旧的顶栏 `Ops 清单` 独立抽屉已移除；历史节点 `ops` 字段仍在节点编辑弹层里可勾选/增删兼容。

## Diff 过滤栏

右栏顶部常驻一条紧凑过滤栏，与 CLI `reqboard filter` 共用 `.reqboard/config.json`：

- **排除单测**：默认勾选；命中内置单测 glob（`*_test.go`、`*.test.ts`、`*.spec.js`、`test_*.py` 等）的文件不计入 Diff 审计。点一下即写配置并刷新。
- **Include / Exclude**：逗号分隔的 glob，回车或点「应用」写入 `config.json`；Include 非空时只审计命中的路径（支持 `repo:path`），Exclude 覆盖默认生成代码规则。
- **重置**：恢复「排除单测 + 默认生成代码 glob」。
- **搜索**：即时按仓库 / 文件 / 符号过滤右栏条目，只影响显示，不写文件。

## 降噪与前置依赖

- **Prerequisite Dock**：默认展开的紧凑横排条，显示 `前置依赖 done/total`，支持 `全部 / 外部 / TODO` 类型筛选与 `全部 / 待就绪 / 已就绪` 状态筛选。
- 每张前置卡片显示影响我方下游节点数（`→ N`）与 `[✓ Done]` 开关；若存在 `mocked` 出边，会询问是否同时 `--unmock-downstream`（切回 `hard` 并把下游标 `stale`）。
- `todo` 卡片支持类型色标与一键 Done；Done 后自动解锁所有仅受它阻塞的下游节点。
- 主画布节点上的小胶囊：橙色 `mock:xxx`、灰色 `等:xxx` / `等:DDL`、绿色 `✓xxx`；点击直接编辑对应前置边。
- 悬停/点击前置卡片会高亮所有依赖它的我方节点并淡化其他节点，顶部显示关联边状态与备注；点击我方节点会反向高亮 Dock 中的前置卡片。
- 顶栏 `[↩ 撤销 (N)]` / `[↪ 重做 (N)]` 显示可用步数，支持 `Ctrl+Z` / `Cmd+Z` 与 `Ctrl+Shift+Z` / `Cmd+Shift+Z`；输入框聚焦时不拦截浏览器原生撤销。
- 节点编辑弹层仍保留历史 `ops` 清单的勾选/增删兼容。
- 节点编辑弹层与右栏 `[删除]`：无绑定直接删，有绑定弹窗区分「标为废弃」与「强制删除」；选中节点按 `Delete` / `Backspace` 也可删除，误删可 `Ctrl+Z` 还原。
- 顶栏 `[画布含前置]` 可查看全图，此时 `external` / `collaborator` / `todo` 统一排到最左 Column 0 源头列；`红线 Out-of-Scope (N)` 胶囊默认折叠，点击展开定位 PRD。
- 顶栏 `[自动左到右排版]` 与 CLI `reqboard layout` 使用同一套自然拓扑算法：普通节点保留真实链路深度，仅 `milestone` / `e2e` 钉最右；全图模式下前置节点统一进 Column 0。
- 点击左栏 PRD 段落会智能定位：命中主画布节点时自动拉近居中并脉冲高亮；纯前置依赖只聚焦 Dock 卡片及其下游，不会把主画布推到空白区；纯红线只展开红线面板。
- 多节点段落会在画布上方显示「关联节点」快捷条，可一键切换聚焦我方 / 前置 / 红线节点。

## 使用

1. 安装：`code --install-extension reqboard-vscode-0.3.0.vsix`
2. 在需求 worktree 运行 `reqboard init` / `prd init` / `plan import`。
3. 命令面板执行 **ReqBoard: Open Board**，选择包含 `.reqboard/` 的目录。

点击文件/符号会优先调用 `focusedGit.openFileComparison`（需安装 focused-git），否则自动降级为 `vscode.diff` 对比 git base 与工作区文件。

Orchestrator session 的跳转链接由 `reqboard.sessionLinkTemplate` 配置（默认留空，只弹提示；可填你的 AI agent CLI 的 session 深链，`{session}` 会替换为 session_id）。

所有修改通过本地 `reqboard` CLI 落盘（flock + 原子 rename + events.jsonl），并监听 `.reqboard/` 热刷新。
