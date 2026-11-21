export type LauncherActionId =
  | 'push_goal'
  | 'review_simplify'
  | 'audit_orphan'
  | 'plan_dag'
  | 'node_work'
  | 'node_fix';

export interface LauncherActionMeta {
  id: LauncherActionId;
  label: string;
  desc: string;
  scope: 'workspace' | 'node';
  defaultPrompt: string;
}

export interface LauncherActionStat {
  total: number;
  terminal: number;
  clipboard: number;
  lastAt: string;
}

export type LauncherStatsMap = Record<string, LauncherActionStat>;

export const LAUNCHER_STATS_KEY = 'reqboard.launcherStats';

export const LAUNCHER_ACTIONS: LauncherActionMeta[] = [
  {
    id: 'push_goal',
    label: '推进完整任务',
    desc: '按 reqboard dispatch 持续推进当前可执行节点直至闭环',
    scope: 'workspace',
    defaultPrompt:
      '作为 ReqBoard 协调者，循环执行 reqboard dispatch 按 P0→P1→P2 推进当前可执行节点（单节点 claim→改码/包内单测→bind 仅生产与手写 mock→check --node→done 或 implemented 释放租约），直到返回 QUIESCENT 输出阻塞账单。',
  },
  {
    id: 'review_simplify',
    label: 'Review 与代码精简',
    desc: '对照主 PRD、红线、bindings 与 mock 债做代码审查和冗余精简',
    scope: 'workspace',
    defaultPrompt:
      '请对照当前工作区 .reqboard/prd.md、补充来源、节点 bindings、Orphan Diff、To-Delete 与 Mock 债，全面 Review 本分支多仓改动：识别冗余、越界或重复代码并先输出精简方案，待我确认后再修改与清理。',
  },
  {
    id: 'audit_orphan',
    label: '审计 Orphan / 残骸 Diff',
    desc: '逐条分类未归属改动（漏绑 / 越界 / 同事合入 / 漏建节点）',
    scope: 'workspace',
    defaultPrompt:
      '请运行 reqboard check --json 逐条审计所有 Orphan Diff 与 To-Delete，结合 git diff 与节点说明归类为【确凿漏绑 / 疑似越界冗余 / 同事合入或生成文件 / DAG 漏建节点】，严禁为了清零乱绑或未经确认删代码，先输出分类清单等我拍板。',
  },
  {
    id: 'plan_dag',
    label: '规划 / 校准 DAG',
    desc: '结合代码库与 PRD 建图、校准 stale 节点，并按门禁同步已实现节点的 done 状态',
    scope: 'workspace',
    defaultPrompt:
      '请结合当前 .reqboard/prd.md、补充来源与现有代码库，按 ReqBoard 规范规划或校准 DAG（反碎化、无伪硬边、区分 todo 与真实外部依赖、核对 stale 节点）；若已有实现代码，顺便按真实代码与 bindings 核验每个节点并同步状态：通过 4 道门禁则执行 reqboard done，仍受外部/mock/todo/人工批注阻塞则标记 implemented 并列明缺口，先跑 Critic 自审后再汇报。',
  },
  {
    id: 'node_work',
    label: '推进选中节点',
    desc: '领取该节点并完成实现、包内单测与绑定',
    scope: 'node',
    defaultPrompt:
      '请执行 reqboard claim {nodeId} 获取节点验收标准、PRD 锚点与外部依据，推进该节点实现并运行包内单测，完成 bind（仅生产实现与手写 mock）与 reqboard check --node {nodeId}，达标则 done 否则显式更新状态释放租约。',
  },
  {
    id: 'node_fix',
    label: '按批注修复节点',
    desc: '读取该节点的未解决 Review 批注并逐条修复',
    scope: 'node',
    defaultPrompt:
      '请执行 reqboard claim {nodeId} 查看该节点的未解决 Review 批注与绑定符号，严格按批注逐条修复代码与单测，更新 bind 并跑 reqboard check --node {nodeId}；人类批注禁止自审关闭，修复后执行 reqboard node update {nodeId} --status implemented 释放租约并回报等我点 Reviewed 通过。',
  },
];

export function findLauncherAction(id: string): LauncherActionMeta | undefined {
  return LAUNCHER_ACTIONS.find((a) => a.id === id);
}

/** POSIX-safe single-quoted shell argument. */
export function shellQuote(value: string): string {
  return `'${String(value).replace(/'/g, `'\\''`)}'`;
}

/** Resolve prompt text for an action, applying optional user prompt overrides. */
export function renderPrompt(
  actionId: string,
  opts: {
    nodeId?: string;
    nodeTitle?: string;
    root?: string;
    overrides?: Record<string, string>;
  } = {}
): string {
  const meta = findLauncherAction(actionId);
  const override = opts.overrides && typeof opts.overrides[actionId] === 'string'
    ? opts.overrides[actionId].trim()
    : '';
  const tpl = override || (meta ? meta.defaultPrompt : '');
  const singleLine = tpl.replace(/\r?\n+/g, ' ').trim();
  return singleLine
    .replace(/\{nodeId\}/g, opts.nodeId || '')
    .replace(/\{nodeTitle\}/g, opts.nodeTitle || '')
    .replace(/\{root\}/g, opts.root || '');
}

/** Build the shell command line from reqboard.agentCommand template. */
export function buildLaunchCommand(
  template: string,
  prompt: string,
  opts: { nodeId?: string; root?: string } = {}
): string {
  const trimmed = String(template || '').trim();
  if (!trimmed) {
    return '';
  }
  const hasPrompt = trimmed.includes('{prompt}');
  let cmd = trimmed
    .replace(/\{prompt\}/g, shellQuote(prompt))
    .replace(/\{nodeId\}/g, shellQuote(opts.nodeId || ''))
    .replace(/\{root\}/g, shellQuote(opts.root || ''));
  if (!hasPrompt) {
    cmd += ' ' + shellQuote(prompt);
  }
  return cmd;
}

/** Return an updated immutable copy of local launcher usage stats. */
export function recordLauncherUsage(
  stats: LauncherStatsMap | undefined,
  actionId: string,
  mode: 'terminal' | 'clipboard',
  nowISO = new Date().toISOString()
): LauncherStatsMap {
  const next: LauncherStatsMap = {};
  for (const [k, v] of Object.entries(stats || {})) {
    if (v && typeof v === 'object') {
      next[k] = {
        total: Number(v.total) || 0,
        terminal: Number(v.terminal) || 0,
        clipboard: Number(v.clipboard) || 0,
        lastAt: String(v.lastAt || ''),
      };
    }
  }
  const prev = next[actionId] || { total: 0, terminal: 0, clipboard: 0, lastAt: '' };
  next[actionId] = {
    total: prev.total + 1,
    terminal: prev.terminal + (mode === 'terminal' ? 1 : 0),
    clipboard: prev.clipboard + (mode === 'clipboard' ? 1 : 0),
    lastAt: nowISO,
  };
  return next;
}

/** Flatten total click counts by action ID for lightweight webview display. */
export function summarizeLauncherCounts(stats: LauncherStatsMap | undefined): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const a of LAUNCHER_ACTIONS) {
    counts[a.id] = Number(stats?.[a.id]?.total) || 0;
  }
  return counts;
}
