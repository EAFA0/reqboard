---
name: reqboard
description: 多仓复杂需求的本地控制面：主 PRD + 补充来源锚点、节点级拍板 refs、功能级 DAG、AST Diff 归因、完结硬门禁与 /goal 自动调度。用户提到 ReqBoard、Orphan Diff、To-Delete、reqboard、补充来源/会议纪要/拍板摘录，或要让一个 /goal 协调者持续推进需求时使用。
---

# ReqBoard

`.reqboard/` 是唯一事实源：`config.json / prd.md / sources/<id>.md / dag.json / bindings.json / events.jsonl`。
所有写操作走 `reqboard` CLI（flock + 原子 rename + events.jsonl）；不要手改 JSON 绕过门禁。

## 三种角色

### Planner（只拆图，不写码）
0. `reqboard init --name <slug>` 在无 git 子仓时进入规划模式；看板审完 DAG 后用 `reqboard repo add --name <n> --path <dir> [--base <ref>]` 挂载代码仓。
1. `reqboard prd init <src.md>`；PRD 更新用 `prd sync <new.md>`（自动对齐锚点并级联 stale）。
2. 主 PRD 之外的依据先登记为补充来源，再在 `prd_refs` 里用 `<source_id>:...` 锚点引用：
   `reqboard source add <file.md> --id <slug> [--title ...] [--kind doc|meeting|chat|spec] [--url ...]`；
   可直接 `--text "粘贴的会议纪要/群聊拍板"` 或 `--file -` 从 stdin 保存。更新用 `reqboard source sync <id> <new.md>`（级联 stale）。
   节点级拍板/外链用 `reqboard ref add <node_id> --title "..." [--kind url|meeting|decision|chat] [--url ...] [--quote "..."]`。
3. `reqboard plan import plan.json`：按业务功能/规则拆节点，业务叶子必须有 `prd_refs`（可引用补充来源锚点）或 `refs`，DAG 必须无环；`todo` 前置节点允许无 `prd_refs`。`plan.json` 节点支持可选 `refs` 数组。
4. `out_of_scope` 红线、`external` 依赖与工程 `todo` 前置必须显式建节点，禁止留给 Agent 脑补；TODO 可用 `reqboard todo add` 或 plan.json 的 `scope: "todo"` 建。
5. `reqboard status` 检查 Unmapped PRD、覆盖率与 Orphan Diff。**覆盖率只考核主 `prd.md`**；补充来源未引用段落不产生 Unmapped。

**三段式高质量建图（强烈推荐在技术方案对齐末尾执行，而非冷读 PRD 一把梭）**：
0. **最佳触发时机**：先和人把技术方案、代码现状、真增量与分工对齐完，在方案收尾阶段产出 `plan.json` 导入看板，再由人在看板微调定版——避免纯看 PRD 冷猜造出假外部依赖和 0 改动废节点。
1. **现状接地**：拆图前先核实代码/知识库现状，标清 PRD 里哪些是**已有能力（0 改动，禁建新 `external`/`mock`）**，哪些才是真实代码增量。
2. **强模型出初版 `plan.json`**：按反碎片化准则拆解，禁止把“保持现状/正常获取”类不变式声明拆成待开发节点。
3. **独立 Critic Review 纠错**：`plan import` 前换独立无偏见视角（优先异构强模型）对照 `[prd.md + 现状 + plan.json]` 专查 4 类缺陷并修正：① 假增量节点（0 改动复用项）；② 假外部依赖/假 Mock（已有能力被误当新联调）；③ 无意义连线；④ 漏拆真实增量与评论区边界。

**反碎片化准则（硬要求）**：
- 多段相关 PRD 锚定到**同一个功能点节点**（复用 `prd_refs`），不要一段一节点。
- 不为纯名词解释、纯复用老逻辑（0 代码改动）、并列枚举拆孤立开发节点。
- 只为“有独立代码增量行为 / 独立验收路径”的规则建节点。
- 外部依赖、协作者与跨节点工程 TODO 是一等前置节点（`external` / `collaborator` / `todo`），不进入主画布业务网格：
  `reqboard todo add "<title>" --kind ddl|gateway|flag|other --blocks <n1,n2,...> [--note "..."]`。
- 仅影响单个节点的历史 `ops` 清单继续兼容：`reqboard ops add <node_id> --kind ... --title "..."`；跨节点 1→N 阻塞请用 `todo`。

### Goal Orchestrator（`/goal`，只调度不写码）
循环直到 `QUIESCENT`：
1. `reqboard dispatch --max-parallel N --session <sid>`
2. 按返回的 P0→P1→P2 `items` 并发拉起 Worker；每个 Worker 只领 1 个 `node_id`。
3. 回收结果后再次 `dispatch`；`QUIESCENT` 时输出分类阻塞账单并停机。
禁止 Orchestrator 亲自改业务代码，也禁止绕过 dispatch 自行猜测下一步。

### Node Worker（单节点，短上下文）
`claim → 读注入 → 改码/包内单测 → bind → check --node → 更新状态/done → 回报`
1. `reqboard claim <id> --session <sid> --lease <repo:glob>`
   - 拒绝 `out_of_scope`/`external`/`proposed`/`hold`；注入 PRD 原文、红线、mocked 备注、批注与人工调整。
   - 自动追加节点引用的补充来源段落原文（标注 `[补充依据 — 与主 PRD 口径冲突时优先采用]`）以及 `refs` 的类型/标题/URL/原话摘录；冲突时以补充依据和最新人工调整优先。
2. 只改锚定 PRD 覆盖的行为；每处生产改动必须挂载（**默认全局排除单测，只需 `bind` 生产 `impl` 与手写 `mock`，无需绑定 `*_test.go`**）：
   `reqboard bind <id> --repo <r> --file <f> --symbol <s> --role impl|mock --why "..."`
   - 大文件多符号一次挂载：`reqboard bind <id> --repo <r> --file <f> --all-symbols --why "..." [--keep-status]`。
   - **并发单测隔离**：多 Worker 并发改同一仓库时，Worker 只跑自己改动包的单测（如 `go test ./<pkg>/...`），不要跑全仓 `go test ./...` 避免被同仓其他未写完的 Worker 编挂；整波回收后再由 Orchestrator 跑全仓编译与提交。
   - 手写业务 mock（如 `mock_provider.go`）显式 `--role mock` 后不会被默认 `**/*_mock.go` / `**/mock_*.go` 吞掉。
3. 自检：`reqboard check --node <id>` 必须无 Orphan/Broken/To-Delete。
4. 交差/完结：能过门禁则 `reqboard done <id> --evidence ...`；若被上游/mock 暂卡完结门禁，则显式 `reqboard node update <id> --status implemented`（或 `mocked`）以**自动释放包租约**；遇阻失败调用 `reqboard node fail <id> --error "..."`（自动释放租约并计入熔断）。
5. 发现遗漏/越界：`reqboard propose --from <id> --prd-ref <p:x> --title ... --reason ...`，审批前不要改。

## 关键语义
- `tags: ["scope"|"e2e"|"milestone"]` 的节点不进入 P1 实现队列，作为范围基线 / 端到端 / 验收节点由 owner 或 QA 复核。
- 边：`hard` 实线；`mocked` 橙虚线（带备注，阻塞 done）；`stale` 黄虚线；`discovered` 灰点线。
- **外部硬阻塞**：`mine/pending` 若存在 `external|collaborator --hard--> mine` 且上游未 done，不进入 P1，归入 `waiting_external`（原因会提示改 `mocked` 或等外部 done）。
- **TODO 前置**：`todo` 节点自身不进入 P1，归入 `waiting_todo`；`todo --hard--> mine` 默认只卡下游 `done`，DDL 未跑完仍可先写代码/单测；只有 TODO 带 `block-start` tag 时才卡 P1 开工。
- **Mock 先行**：`external --mocked--> mine` 仍可进入 P1 带 Mock 并行开发；`mine --> mine` 依赖只卡完结、不卡开工。
- **前置就绪**：`reqboard done <external|collaborator|todo-id> [--unmock-downstream]` 或 `node update <id> --status done` 无需我方代码门禁；`--unmock-downstream` 会把 mocked 出边切回 hard，并把下游 mine 标 `stale`（需拆 Mock 切真联调）。
- `reqboard todo add "<title>" --kind ddl|gateway|flag|other --blocks <n1,n2,...>` 自动建 `hard` 边；`reqboard todo list [--json]` 查看完成状态与阻塞下游。
- `mocked → hard` 自动把下游标 `stale`。
- `deprecated` 节点保留 bindings，残留 diff 一直进 `To-Delete`；误建节点用 `reqboard node delete <id>` 彻底删除（有 bindings 时需 `--force`，否则先提示改用 `deprecate`）。
- `done` 四道门禁（仅 mine）：前置全 done / 零 Mock 债 / 非 stale 且无 open 批注 / check 干净；上游 TODO 未完成时 Gate 1 会明确报错并提示先 `reqboard done <todo-id>`。
- `reqboard layout` 主画布按自然拓扑深度（ASAP）左→右排布，保留真实链路长短；仅 `milestone` / `e2e` 终结业务场景节点钉在最右终列。
- `external|collaborator|todo|out_of_scope` 默认不占主画布网格；`reqboard layout --include-external`（看板「画布含前置」）把它们统一排到最左 Column 0 源头列，自然融入左到右拓扑。
- `reqboard undo` / `redo`：每次写 `dag.json` / `bindings.json` / `config.json` 前自动在 `.reqboard/.undo/` 记录前态快照（最多 20 步，flock 内原子完成）；`status --json` 返回 `undo_depth` / `redo_depth`。
- 节点级 `ops` 是兼容保留的轻量交付清单；跨节点 DDL / 网关 / Flag 用一等 `todo` 节点，看板顶部统一 Prerequisite Dock 管理。
- Diff 采用三点语义：先 `git merge-base <base_ref> HEAD`，再对共同祖先执行 `git diff`，base 分支领先时不会把其新提交误报为反向删除。
- 人类在看板的修改优先级最高；`claim` 会注入 `[人工最新调整]`。

## 常用命令
`status` / `filter show|set` / `dispatch` / `claim` / `bind [--all-symbols]` / `unbind` / `propose` / `mock add|resolve` / `todo add|list` / `ops add|toggle|remove` / `source add|sync|list|remove` / `ref add|remove` / `check --all` / `review-pack` / `done [--unmock-downstream]` / `layout [--include-external]` / `node delete [--force]` / `undo` / `redo`

Diff 审计默认排除单测（`exclude_tests=true`）与生成代码；需要收窄/放宽时用 `reqboard filter set --include <globs> --exclude <globs> --exclude-tests=true|false`，`filter show --json` 可回读当前生效规则。显式 `bind` 的 mock/impl 文件（以及显式绑定的测试文件）会豁免默认噪声过滤，确保手写 mock 与验收代码可见。
所有命令支持 `--json`；报错文本即规则注入，按提示纠偏。
