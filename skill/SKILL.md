---
name: reqboard
description: 多仓复杂需求的本地控制面：PRD 锚点、功能级 DAG、AST Diff 归因、完结硬门禁与 /goal 自动调度。用户提到 ReqBoard、Orphan Diff、To-Delete、reqboard，或要让一个 /goal 协调者持续推进需求时使用。
---

# ReqBoard

`.reqboard/` 是唯一事实源：`config.json / prd.md / dag.json / bindings.json / events.jsonl`。
所有写操作走 `reqboard` CLI（flock + 原子 rename + events.jsonl）；不要手改 JSON 绕过门禁。

## 三种角色

### Planner（只拆图，不写码）
0. `reqboard init --name <slug>` 在无 git 子仓时进入规划模式；看板审完 DAG 后用 `reqboard repo add --name <n> --path <dir> [--base <ref>]` 挂载代码仓。
1. `reqboard prd init <src.md>`；PRD 更新用 `prd sync <new.md>`（自动对齐锚点并级联 stale）。
2. `reqboard plan import plan.json`：按业务功能/规则拆节点，叶子必须有 `prd_refs`，DAG 必须无环。
3. `out_of_scope` 红线与 `external` 依赖必须显式建节点，禁止留给 Agent 脑补。
4. `reqboard status` 检查 Unmapped PRD、覆盖率与 Orphan Diff。

### Goal Orchestrator（`/goal`，只调度不写码）
循环直到 `QUIESCENT`：
1. `reqboard dispatch --max-parallel N --session <sid>`
2. 按返回的 P0→P1→P2 `items` 并发拉起 Worker；每个 Worker 只领 1 个 `node_id`。
3. 回收结果后再次 `dispatch`；`QUIESCENT` 时输出分类阻塞账单并停机。
禁止 Orchestrator 亲自改业务代码，也禁止绕过 dispatch 自行猜测下一步。

### Node Worker（单节点，短上下文）
`claim → 读注入 → 改码/单测 → bind → check --node → 回报`
1. `reqboard claim <id> --session <sid> --lease <repo:glob>`
   - 拒绝 `out_of_scope`/`external`/`proposed`/`hold`；注入 PRD 原文、红线、mocked 备注、批注与人工调整。
2. 只改锚定 PRD 覆盖的行为；每处改动必须：
   `reqboard bind <id> --repo <r> --file <f> --symbol <s> --role impl|mock|test --why "..."`
3. 自检：`reqboard check --node <id>` 必须无 Orphan/Broken/To-Delete。
4. 完结：`reqboard done <id> --evidence ...`；失败按输出的纠偏命令处理，禁止硬标 done。
5. 发现遗漏/越界：`reqboard propose --from <id> --prd-ref <p:x> --title ... --reason ...`，审批前不要改。

## 关键语义
- `tags: ["scope"|"e2e"|"milestone"]` 的节点不进入 P1 实现队列，作为范围基线 / 端到端 / 验收节点由 owner 或 QA 复核。
- 边：`hard` 实线；`mocked` 橙虚线（带备注，阻塞 done）；`stale` 黄虚线；`discovered` 灰点线。
- **外部硬阻塞**：`mine/pending` 若存在 `external|collaborator --hard--> mine` 且上游未 done，不进入 P1，归入 `waiting_external`（原因会提示改 `mocked` 或等外部 done）。
- **Mock 先行**：`external --mocked--> mine` 仍可进入 P1 带 Mock 并行开发；`mine --> mine` 依赖只卡完结、不卡开工。
- **外部就绪**：`reqboard done <external-id> [--unmock-downstream]` 或 `node update <id> --status done [--unmock-downstream]` 无需我方代码门禁；`--unmock-downstream` 会把 mocked 出边切回 hard，并把下游 mine 标 `stale`（需拆 Mock 切真联调）。
- `mocked → hard` 自动把下游标 `stale`。
- `deprecated` 节点保留 bindings，残留 diff 一直进 `To-Delete`。
- `done` 四道门禁（仅 mine）：前置全 done / 零 Mock 债 / 非 stale 且无 open 批注 / check 干净。
- `reqboard layout` 只给我方主干（`mine` / `proposed`）排紧凑左到右网格；`external|collaborator|out_of_scope` 收纳到独立停靠区，不占主画布列宽。
- 人类在看板的修改优先级最高；`claim` 会注入 `[人工最新调整]`。

## 常用命令
`status` / `filter show|set` / `dispatch` / `claim` / `bind` / `unbind` / `propose` / `mock add|resolve` / `check --all` / `review-pack` / `done [--unmock-downstream]` / `layout`

Diff 审计默认排除单测（`exclude_tests=true`）与生成代码；需要收窄/放宽时用 `reqboard filter set --include <globs> --exclude <globs> --exclude-tests=true|false`，`filter show --json` 可回读当前生效规则。
所有命令支持 `--json`；报错文本即规则注入，按提示纠偏。
