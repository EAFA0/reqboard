import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

import { CLIError, ReqboardCLI, gitShow } from './cli';
import { buildReadOnlyFileDiff, collectFileDiff } from './diffService';
import {
  LAUNCHER_STATS_KEY,
  LauncherStatsMap,
  buildLaunchCommand,
  findLauncherAction,
  recordLauncherUsage,
  renderPrompt,
  summarizeLauncherCounts,
} from './launcher';
import { findSymbolLine } from './util';
import { renderHTML } from './webview';

const WATCHED = ['events.jsonl', 'dag.json', 'bindings.json', 'prd.md', 'config.json', 'sources'];

function sourceSlug(seed: string): string {
  const base = String(seed || '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 24);
  return `${base || 'src'}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 5)}`;
}

/** Singleton webview panel with fs.watch hot reload. */
export class BoardPanel {
  public static current: BoardPanel | undefined;

  private readonly panel: vscode.WebviewPanel;
  private readonly cli: ReqboardCLI;
  private snapshot: any;
  private watcher: fs.FSWatcher | undefined;
  private refreshTimer: NodeJS.Timeout | undefined;
  private refreshing = false;
  private pendingRefresh = false;
  private cleaned = false;
  private readonly externalAllow = new Set<string>();

  public static createOrShow(context: vscode.ExtensionContext, root: string): void {
    if (BoardPanel.current) {
      if (BoardPanel.current.root === root) {
        BoardPanel.current.panel.reveal(vscode.ViewColumn.One);
        return;
      }
      BoardPanel.current.dispose();
    }
    BoardPanel.current = new BoardPanel(context, root);
  }

  private constructor(private readonly context: vscode.ExtensionContext, public readonly root: string) {
    this.cli = new ReqboardCLI(root, context.extensionPath);
    this.panel = vscode.window.createWebviewPanel(
      'reqboard.board',
      `ReqBoard: ${path.basename(root)}`,
      vscode.ViewColumn.One,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [vscode.Uri.joinPath(context.extensionUri, 'media')],
      }
    );
    this.panel.webview.html = renderHTML(this.panel.webview, context.extensionUri);
    this.panel.webview.onDidReceiveMessage((msg) => void this.onMessage(msg), undefined, context.subscriptions);
    this.panel.onDidDispose(() => this.cleanup(), undefined, context.subscriptions);
    this.startWatcher();
    this.scheduleRefresh();
  }

  public dispose(): void {
    this.cleanup();
    this.panel.dispose();
  }

  private cleanup(): void {
    if (this.cleaned) {
      return;
    }
    this.cleaned = true;
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer);
      this.refreshTimer = undefined;
    }
    this.watcher?.close();
    this.watcher = undefined;
    if (BoardPanel.current === this) {
      BoardPanel.current = undefined;
    }
  }

  private startWatcher(): void {
    try {
      this.watcher = fs.watch(path.join(this.root, '.reqboard'), (_event, filename) => {
        const name = filename ? filename.toString() : '';
        if (!name || WATCHED.includes(name)) {
          this.scheduleRefresh();
        }
      });
    } catch {
      // fs.watch is best-effort; the refresh button still works.
    }
  }

  private scheduleRefresh(): void {
    if (this.cleaned) {
      return;
    }
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer);
    }
    this.refreshTimer = setTimeout(() => {
      this.refreshTimer = undefined;
      void this.refresh();
    }, 150);
  }

  private post(msg: any): void {
    if (!this.cleaned) {
      void this.panel.webview.postMessage(msg);
    }
  }

  private async refresh(): Promise<void> {
    if (this.refreshing) {
      this.pendingRefresh = true;
      return;
    }
    this.refreshing = true;
    try {
      const res = await this.cli.run(['status']);
      if (!res.data) {
        this.post({ type: 'error', message: 'reqboard status 没有返回 JSON' });
        return;
      }
      this.snapshot = res.data;
      this.post({ type: 'snapshot', data: { ...res.data, launcher: this.launcherSummary() } });
    } catch (e: any) {
      this.post({ type: 'error', message: this.describeError(e) });
    } finally {
      this.refreshing = false;
      if (this.pendingRefresh) {
        this.pendingRefresh = false;
        this.scheduleRefresh();
      }
    }
  }

  private describeError(e: any): string {
    if (e instanceof CLIError) {
      const parts = [e.message];
      const violations = e.data?.violations;
      if (Array.isArray(violations) && violations.length > 0) {
        parts.push(
          ...violations.map((v: any) => `G${v.gate} ${v.code}: ${v.message}${v.correction ? `\n  -> ${v.correction}` : ''}`)
        );
      }
      if (e.stderr && e.stderr.trim() && !e.message.includes(e.stderr.trim())) {
        parts.push(e.stderr.trim());
      }
      return parts.join('\n');
    }
    return e?.message || String(e);
  }

  private async exec(args: string[], okMessage?: string): Promise<void> {
    try {
      await this.cli.run(args);
      this.post({ type: 'toast', level: 'info', message: okMessage || '已写入 .reqboard/' });
    } catch (e: any) {
      this.post({ type: 'error', message: this.describeError(e) });
    }
    await this.refresh();
  }

  private async onMessage(msg: any): Promise<void> {
    if (!msg || typeof msg !== 'object') {
      return;
    }
    switch (msg.type) {
      case 'ready':
      case 'refresh':
        await this.refresh();
        return;
      case 'command':
        if (Array.isArray(msg.args)) {
          await this.exec(msg.args.map(String));
        }
        return;
      case 'filter.update': {
        if (msg.reset) {
          await this.exec(['filter', 'set', '--reset', '--actor', 'human'], 'Diff 过滤已重置');
          return;
        }
        const args = ['filter', 'set', '--actor', 'human'];
        if (msg.excludeTests !== undefined) {
          args.push('--exclude-tests', msg.excludeTests ? 'true' : 'false');
        }
        if (msg.includeGlobs !== undefined) {
          args.push('--include', Array.isArray(msg.includeGlobs) ? msg.includeGlobs.join(',') : String(msg.includeGlobs));
        }
        if (msg.ignoreGlobs !== undefined) {
          args.push('--exclude', Array.isArray(msg.ignoreGlobs) ? msg.ignoreGlobs.join(',') : String(msg.ignoreGlobs));
        }
        if (args.length <= 4) {
          return;
        }
        await this.exec(args, 'Diff 过滤已写入 config.json');
        return;
      }
      case 'source.add': {
        const title = String(msg.title || '').trim();
        const text = String(msg.text || '');
        if (!title) {
          this.post({ type: 'error', message: '补充来源标题不能为空' });
          return;
        }
        if (!text.trim()) {
          this.post({ type: 'error', message: '补充来源正文不能为空' });
          return;
        }
        const id = sourceSlug(title);
        const tmp = path.join(
          os.tmpdir(),
          `reqboard-source-${Date.now()}-${Math.random().toString(36).slice(2)}.md`
        );
        try {
          fs.writeFileSync(tmp, text, 'utf8');
          const args = ['source', 'add', tmp, '--id', id, '--title', title, '--kind', String(msg.kind || 'doc'), '--actor', 'human'];
          const url = String(msg.url || '').trim();
          if (url) {
            args.push('--url', url);
          }
          await this.cli.run(args);
          this.post({ type: 'toast', level: 'info', message: `补充来源已保存：${id}` });
          this.post({ type: 'source.added', id });
        } catch (e: any) {
          this.post({ type: 'error', message: this.describeError(e) });
        } finally {
          try {
            fs.unlinkSync(tmp);
          } catch {
            // best-effort temp cleanup
          }
        }
        await this.refresh();
        return;
      }
      case 'source.pick': {
        const picked = await vscode.window.showOpenDialog({
          canSelectFiles: true,
          canSelectFolders: false,
          canSelectMany: false,
          filters: { Markdown: ['md', 'markdown', 'txt'] },
          openLabel: '导入为补充来源',
        });
        if (!picked || picked.length === 0) {
          return;
        }
        const file = picked[0].fsPath;
        const title = path.basename(file).replace(/\.(md|markdown|txt)$/i, '');
        const id = sourceSlug(title);
        try {
          await this.cli.run(['source', 'add', file, '--id', id, '--title', title, '--kind', 'doc', '--actor', 'human']);
          this.post({ type: 'toast', level: 'info', message: `补充来源已导入：${id}` });
          this.post({ type: 'source.added', id });
        } catch (e: any) {
          this.post({ type: 'error', message: this.describeError(e) });
        }
        await this.refresh();
        return;
      }
      case 'ref.add': {
        const title = String(msg.title || '').trim();
        if (!title) {
          this.post({ type: 'error', message: '依据标题不能为空' });
          return;
        }
        const args = ['ref', 'add', String(msg.nodeId), '--title', title, '--kind', String(msg.kind || 'decision'), '--actor', 'human'];
        const url = String(msg.url || '').trim();
        if (url) {
          args.push('--url', url);
        }
        const quote = String(msg.quote || '').trim();
        if (quote) {
          args.push('--quote', quote);
        }
        await this.exec(args, '外部依据已添加');
        return;
      }
      case 'ref.remove':
        await this.exec(['ref', 'remove', String(msg.nodeId), String(msg.refId), '--actor', 'human'], '外部依据已删除');
        return;
      case 'todo.add': {
        const title = String(msg.title || '').trim();
        if (!title) {
          this.post({ type: 'error', message: 'TODO 标题不能为空' });
          return;
        }
        const args = ['todo', 'add', title, '--kind', String(msg.kind || 'other'), '--actor', 'human'];
        if (msg.note) {
          args.push('--note', String(msg.note));
        }
        const blocks: string[] = Array.isArray(msg.blocks) ? msg.blocks.map(String).filter(Boolean) : [];
        if (blocks.length) {
          args.push('--blocks', blocks.join(','));
        }
        await this.exec(args, blocks.length ? `TODO 已创建并阻塞 ${blocks.length} 个下游节点` : 'TODO 已创建');
        return;
      }
      case 'node.create': {
        const args = ['node', 'add', '--title', String(msg.title || '新功能点'), '--actor', 'human'];
        if (msg.scope) {
          args.push('--scope', String(msg.scope));
        }
        for (const ref of msg.prdRefs || []) {
          args.push('--prd-ref', String(ref));
        }
        if (typeof msg.x === 'number') {
          args.push('--x', String(msg.x));
        }
        if (typeof msg.y === 'number') {
          args.push('--y', String(msg.y));
        }
        await this.exec(args, '节点已创建（直写 dag.json）');
        return;
      }
      case 'node.update': {
        const args = ['node', 'update', String(msg.id), '--actor', 'human'];
        const p = msg.patch || {};
        if (p.title !== undefined) {
          args.push('--title', String(p.title));
        }
        if (p.description !== undefined) {
          args.push('--description', String(p.description));
        }
        if (p.scope !== undefined) {
          args.push('--scope', String(p.scope));
        }
        if (p.status !== undefined) {
          args.push('--status', String(p.status));
        }
        if (p.hold !== undefined) {
          args.push('--hold', p.hold ? 'true' : 'false');
        }
        if (p.stale !== undefined) {
          args.push('--stale', p.stale ? 'true' : 'false');
        }
        if (p.tags !== undefined) {
          const tags: string[] = Array.isArray(p.tags) ? p.tags : String(p.tags).split(',').map((s) => s.trim()).filter(Boolean);
          if (tags.length === 0) {
            args.push('--tag', '');
          } else {
            for (const t of tags) {
              args.push('--tag', t);
            }
          }
        }
        if (p.prd_refs !== undefined) {
          const refs: string[] = Array.isArray(p.prd_refs) ? p.prd_refs : [];
          if (refs.length === 0) {
            args.push('--prd-ref', '');
          } else {
            for (const r of refs) {
              args.push('--prd-ref', r);
            }
          }
        }
        if (typeof p.x === 'number') {
          args.push('--x', String(p.x));
        }
        if (typeof p.y === 'number') {
          args.push('--y', String(p.y));
        }
        if (args.length <= 5) {
          return;
        }
        await this.exec(args, '节点已更新（直写 dag.json）');
        return;
      }
      case 'node.deprecate':
        await this.exec(['node', 'deprecate', String(msg.id), '--reason', String(msg.reason || ''), '--actor', 'human']);
        return;
      case 'node.delete': {
        const args = ['node', 'delete', String(msg.id), '--actor', 'human'];
        if (msg.force) {
          args.push('--force');
        }
        await this.exec(args, msg.force ? '节点与 bindings 已强制删除（Ctrl+Z 可撤销）' : '节点已删除（Ctrl+Z 可撤销）');
        return;
      }
      case 'undo':
        await this.exec(['undo', '--actor', 'human'], '已撤销 1 步');
        return;
      case 'redo':
        await this.exec(['redo', '--actor', 'human'], '已重做 1 步');
        return;
      case 'ops.add': {
        const title = String(msg.title || '').trim();
        if (!title) {
          this.post({ type: 'error', message: 'Ops 标题不能为空' });
          return;
        }
        const args = ['ops', 'add', String(msg.nodeId), '--kind', String(msg.kind || 'other'), '--title', title, '--actor', 'human'];
        if (msg.note) {
          args.push('--note', String(msg.note));
        }
        await this.exec(args, 'Ops 已新增');
        return;
      }
      case 'ops.toggle':
        await this.exec(['ops', 'toggle', String(msg.nodeId), String(msg.opId), '--actor', 'human']);
        return;
      case 'ops.remove':
        await this.exec(['ops', 'remove', String(msg.nodeId), String(msg.opId), '--actor', 'human'], 'Ops 已删除');
        return;
      case 'node.approve':
        await this.exec(['node', 'approve', String(msg.id), '--actor', 'human']);
        return;
      case 'node.reject':
        await this.exec(['node', 'reject', String(msg.id), '--reason', String(msg.reason || ''), '--actor', 'human']);
        return;
      case 'node.review': {
        const args = ['node', 'review', String(msg.id), msg.pass ? '--pass' : '--request-changes', '--actor', 'human'];
        if (msg.note) {
          args.push('--note', String(msg.note));
        }
        if (msg.target) {
          args.push('--target', String(msg.target));
        }
        await this.exec(args);
        return;
      }
      case 'node.fail':
        await this.exec(['node', 'fail', String(msg.id), '--error', String(msg.error || 'worker failed')]);
        return;
      case 'edge.add': {
        const args = ['edge', 'add', '--from', String(msg.from), '--to', String(msg.to), '--kind', String(msg.kind || 'hard'), '--actor', 'human'];
        if (msg.note) {
          args.push('--note', String(msg.note));
        }
        await this.exec(args);
        return;
      }
      case 'edge.update': {
        const args = ['edge', 'update', String(msg.id), '--actor', 'human'];
        if (msg.kind !== undefined) {
          args.push('--kind', String(msg.kind));
        }
        if (msg.note !== undefined) {
          args.push('--note', String(msg.note));
        }
        await this.exec(args);
        return;
      }
      case 'edge.remove':
        await this.exec(['edge', 'remove', String(msg.id), '--actor', 'human']);
        return;
      case 'prd.ignore': {
        const args = ['prd', 'ignore', String(msg.anchor), '--actor', 'human'];
        if (msg.undo) {
          args.push('--undo');
        }
        await this.exec(args);
        return;
      }
      case 'layout': {
        const args = ['layout', '--actor', 'human'];
        if (msg.includeExternal) {
          args.push('--include-external');
        }
        await this.exec(args, msg.includeExternal ? '全图模式：外部/协作者/TODO 已排到最左列' : '已按自然拓扑深度重新排版');
        return;
      }
      case 'done': {
        const args = ['done', String(msg.id), '--actor', 'human'];
        if (msg.unmock) {
          args.push('--unmock-downstream');
        }
        await this.exec(args);
        return;
      }
      case 'open.diff':
        await this.openDiff(msg);
        return;
      case 'diff.fetch':
        await this.handleDiffFetch(msg);
        return;
      case 'diff.symbol':
        await this.handleDiffSymbol(msg);
        return;
      case 'diff.openInEditor':
        await this.openInEditor(String(msg.absPath || ''), Number(msg.line || 1));
        return;
      case 'clipboard.write':
        if (msg.text) {
          await vscode.env.clipboard.writeText(String(msg.text));
          this.post({ type: 'toast', level: 'info', message: String(msg.toast || '已复制') });
        }
        return;
      case 'open.external': {
        const url = String(msg.url || '');
        if (!/^https?:\/\//i.test(url)) {
          this.post({ type: 'error', message: '只允许打开 http(s) 外链：' + url });
          return;
        }
        await vscode.env.openExternal(vscode.Uri.parse(url));
        return;
      }
      case 'open.session':
        await this.openSession(msg.sessionId);
        return;
      case 'copy.goal':
        await this.launchAgent({ action: 'push_goal', copyOnly: true });
        return;
      case 'agent.launch':
        await this.launchAgent(msg);
        return;
      case 'launcher.openSettings':
        await vscode.commands.executeCommand('workbench.action.openSettings', 'reqboard.agentCommand');
        return;
      case 'launcher.showStats':
        await vscode.commands.executeCommand('reqboard.showLauncherStats');
        return;
      default:
        return;
    }
  }

  private launcherSummary(): { configured: boolean; counts: Record<string, number> } {
    const cfg = vscode.workspace.getConfiguration('reqboard');
    const configured = Boolean((cfg.get<string>('agentCommand') || '').trim());
    const stats = this.context.globalState.get<LauncherStatsMap>(LAUNCHER_STATS_KEY, {});
    return {
      configured,
      counts: summarizeLauncherCounts(stats),
    };
  }

  private async launchAgent(msg: any): Promise<void> {
    const actionId = String(msg.action || '');
    const meta = findLauncherAction(actionId);
    if (!meta) {
      this.post({ type: 'error', message: `未知发射动作：${actionId}` });
      return;
    }
    const nodeId = msg.nodeId ? String(msg.nodeId) : undefined;
    const nodeTitle = msg.nodeTitle ? String(msg.nodeTitle) : undefined;
    if (meta.scope === 'node' && !nodeId) {
      this.post({ type: 'error', message: '请先在画布选中一个节点' });
      return;
    }
    const cfg = vscode.workspace.getConfiguration('reqboard');
    const overrides = cfg.get<Record<string, string>>('agentPrompts') || {};
    const prompt = renderPrompt(actionId, {
      nodeId,
      nodeTitle,
      root: this.root,
      overrides,
    });
    const cmdTemplate = (cfg.get<string>('agentCommand') || '').trim();
    const copyOnly = Boolean(msg.copyOnly);
    const prevStats = this.context.globalState.get<LauncherStatsMap>(LAUNCHER_STATS_KEY, {});

    if (!copyOnly && cmdTemplate) {
      const cmdLine = buildLaunchCommand(cmdTemplate, prompt, { nodeId, root: this.root });
      const termName = nodeId ? `ReqBoard: ${meta.label} (${nodeId})` : `ReqBoard: ${meta.label}`;
      const term = vscode.window.createTerminal({ name: termName, cwd: this.root });
      term.show(false);
      term.sendText(cmdLine, true);
      const nextStats = recordLauncherUsage(prevStats, actionId, 'terminal');
      await this.context.globalState.update(LAUNCHER_STATS_KEY, nextStats);
      this.post({ type: 'launcher.stats', data: this.launcherSummary() });
      this.post({ type: 'toast', level: 'info', message: `已发射终端：${meta.label}` });
      return;
    }

    await vscode.env.clipboard.writeText(prompt);
    const nextStats = recordLauncherUsage(prevStats, actionId, 'clipboard');
    await this.context.globalState.update(LAUNCHER_STATS_KEY, nextStats);
    this.post({ type: 'launcher.stats', data: this.launcherSummary() });
    if (copyOnly) {
      this.post({ type: 'toast', level: 'info', message: `已复制指令：${meta.label}` });
      return;
    }
    this.post({
      type: 'toast',
      level: 'info',
      message: `未配置 reqboard.agentCommand，已复制「${meta.label}」指令到剪贴板`,
    });
    const pick = await vscode.window.showInformationMessage(
      `未配置 reqboard.agentCommand，已将「${meta.label}」指令复制到剪贴板。配置命令模板后可直接拉起终端。`,
      '去配置 agentCommand'
    );
    if (pick === '去配置 agentCommand') {
      await vscode.commands.executeCommand('workbench.action.openSettings', 'reqboard.agentCommand');
    }
  }

  private async openSession(sessionId: string | undefined): Promise<void> {
    if (!sessionId) {
      this.post({ type: 'error', message: '当前没有 Orchestrator session_id；先运行 reqboard dispatch。' });
      return;
    }
    const template = vscode.workspace.getConfiguration('reqboard').get<string>('sessionLinkTemplate') || '';
    if (!template.trim()) {
      void vscode.window.showInformationMessage(`Orchestrator session: ${sessionId}`);
      return;
    }
    const url = template.replace(/\{session\}/g, encodeURIComponent(sessionId));
    try {
      await vscode.env.openExternal(vscode.Uri.parse(url));
    } catch {
      void vscode.window.showInformationMessage(`Orchestrator session: ${sessionId}`);
    }
  }

  private async openDiff(msg: any): Promise<void> {
    const viewer = vscode.workspace.getConfiguration('reqboard').get<string>('diffViewer', 'builtin');
    if (viewer === 'builtin') {
      this.post({ type: 'diff.open', req: msg });
      return;
    }
    const snap = this.snapshot;
    const repo = (snap?.config?.repos || []).find((r: any) => r.name === msg.repo);
    if (!repo) {
      this.post({ type: 'error', message: `repo ${msg.repo} 不在 config.json 中` });
      return;
    }
    const repoPath = path.isAbsolute(repo.path) ? repo.path : path.join(this.root, repo.path);
    const abs = path.isAbsolute(msg.file) ? msg.file : path.join(repoPath, msg.file);
    let line = typeof msg.line === 'number' && msg.line > 0 ? msg.line : 0;
    if (!line && msg.symbol) {
      try {
        line = findSymbolLine(fs.readFileSync(abs, 'utf8'), String(msg.symbol));
      } catch {
        line = 0;
      }
    }
    const uri = vscode.Uri.file(abs);
    let opened = false;
    if (viewer === 'focused-git') {
      try {
        await vscode.commands.executeCommand('focusedGit.openFileComparison', uri);
        opened = true;
      } catch {
        opened = false;
      }
    }
    if (!opened) {
      try {
        await this.openFallbackDiff(repoPath, repo.base_ref || 'HEAD', String(msg.file), abs, line);
      } catch (e: any) {
        this.post({ type: 'error', message: `无法打开 Diff: ${e?.message || e}` });
        return;
      }
    }
    if (line && vscode.workspace.getConfiguration('reqboard').get<boolean>('revealSymbolAfterOpen', true)) {
      setTimeout(() => this.revealLine(abs, line), 250);
    }
  }

  private resolveConfiguredRepos(): Array<{ name: string; path: string; baseRef: string }> {
    const repos: any[] = this.snapshot?.config?.repos || [];
    return repos.map((r) => ({
      name: String(r.name || ''),
      path: path.resolve(path.isAbsolute(r.path) ? r.path : path.join(this.root, r.path)),
      baseRef: String(r.base_ref || 'HEAD'),
    }));
  }

  private async handleDiffFetch(msg: any): Promise<void> {
    const reqId = Number(msg.reqId || 0);
    const repoName = String(msg.repo || '').trim();
    const relFile = String(msg.file || '').trim();
    const symbol = String(msg.symbol || '').trim();
    let focusLine = Number(msg.line || 0);
    const contextLines = typeof msg.context === 'number' ? msg.context : 5;
    if (!repoName || !relFile) {
      this.post({ type: 'diff.data', reqId, error: 'repo 和 file 不能为空' });
      return;
    }

    try {
      if (repoName === '__external__') {
        const abs = path.resolve(relFile);
        if (!this.externalAllow.has(abs)) {
          this.post({ type: 'diff.data', reqId, error: '外部文件未在 LSP 白名单中' });
          return;
        }
        const raw = fs.readFileSync(abs);
        const fd = buildReadOnlyFileDiff(raw, 'external', 'external', 'external dependency — read-only');
        this.post({
          type: 'diff.data',
          reqId,
          data: {
            ...fd,
            repo: '__external__',
            file: abs,
            abs_path: abs,
            symbol,
            focus_line: focusLine,
          },
        });
        return;
      }

      const repo = this.resolveConfiguredRepos().find((r) => r.name === repoName);
      if (!repo) {
        this.post({ type: 'diff.data', reqId, error: `repo ${repoName} 不在 config.json 中` });
        return;
      }
      const abs = path.resolve(repo.path, relFile);
      if (abs !== repo.path && !abs.startsWith(repo.path + path.sep)) {
        this.post({ type: 'diff.data', reqId, error: '非法路径：越出仓库范围' });
        return;
      }
      const fd = await collectFileDiff(repo.path, repo.baseRef, relFile, contextLines);
      if (focusLine <= 0 && symbol) {
        try {
          focusLine = findSymbolLine(fs.readFileSync(abs, 'utf8'), symbol);
        } catch {
          focusLine = 0;
        }
      }
      this.post({
        type: 'diff.data',
        reqId,
        data: {
          ...fd,
          repo: repoName,
          file: relFile,
          abs_path: abs,
          symbol,
          focus_line: focusLine,
        },
      });
    } catch (e: any) {
      this.post({ type: 'diff.data', reqId, error: `读取 diff 失败: ${e?.message || e}` });
    }
  }

  private async handleDiffSymbol(msg: any): Promise<void> {
    const reqId = Number(msg.reqId || 0);
    const absPath = path.resolve(String(msg.absPath || ''));
    const line = Math.max(0, Number(msg.line || 1) - 1);
    const col = Math.max(0, Number(msg.col || 1) - 1);
    const findRefs = Boolean(msg.references);
    if (!absPath || !fs.existsSync(absPath)) {
      this.post({ type: 'diff.symbols', reqId, error: '源文件不存在' });
      return;
    }
    try {
      const docUri = vscode.Uri.file(absPath);
      // Ensure the document is loaded so VS Code language servers (gopls/tsserver/pyright/etc.) index it.
      await vscode.workspace.openTextDocument(docUri);
      const pos = new vscode.Position(line, col);
      const cmdName = findRefs ? 'vscode.executeReferenceProvider' : 'vscode.executeDefinitionProvider';
      const raw = await vscode.commands.executeCommand<Array<vscode.Location | vscode.LocationLink>>(cmdName, docUri, pos);
      const repos = this.resolveConfiguredRepos();
      const seen = new Set<string>();
      const locations: any[] = [];
      for (const item of raw || []) {
        const isLink = 'targetUri' in item;
        const targetUri = isLink ? item.targetUri : item.uri;
        if (!targetUri || targetUri.scheme !== 'file') {
          continue;
        }
        const range = isLink ? (item.targetSelectionRange || item.targetRange) : item.range;
        const targetAbs = path.resolve(targetUri.fsPath);
        const targetLine = (range?.start?.line ?? 0) + 1;
        const targetCol = (range?.start?.character ?? 0) + 1;
        // For Find References, skip the clicked declaration site itself.
        if (findRefs && targetAbs === absPath && targetLine === line + 1) {
          continue;
        }
        const key = `${targetAbs}:${targetLine}:${targetCol}`;
        if (seen.has(key)) {
          continue;
        }
        seen.add(key);

        let matchedRepo = '__external__';
        let matchedFile = targetAbs;
        for (const r of repos) {
          if (targetAbs === r.path || targetAbs.startsWith(r.path + path.sep)) {
            matchedRepo = r.name;
            matchedFile = path.relative(r.path, targetAbs).split(path.sep).join('/');
            break;
          }
        }
        if (matchedRepo === '__external__') {
          this.externalAllow.add(targetAbs);
        }
        let preview = '';
        try {
          const lines = fs.readFileSync(targetAbs, 'utf8').split(/\r?\n/);
          preview = (lines[targetLine - 1] || '').trim();
        } catch {
          preview = '';
        }
        locations.push({
          repo: matchedRepo,
          file: matchedFile,
          abs_path: targetAbs,
          line: targetLine,
          col: targetCol,
          preview,
          source: 'vscode-lsp',
        });
        if (locations.length >= 50) {
          break;
        }
      }
      this.post({ type: 'diff.symbols', reqId, source: 'vscode-lsp', locations });
    } catch (e: any) {
      this.post({ type: 'diff.symbols', reqId, error: `VS Code LSP 查询失败: ${e?.message || e}` });
    }
  }

  private async openInEditor(absPath: string, line: number): Promise<void> {
    if (!absPath) {
      return;
    }
    try {
      const uri = vscode.Uri.file(path.resolve(absPath));
      const doc = await vscode.workspace.openTextDocument(uri);
      const pos = new vscode.Position(Math.max(0, line - 1), 0);
      await vscode.window.showTextDocument(doc, {
        preview: false,
        selection: new vscode.Range(pos, pos),
      });
    } catch (e: any) {
      this.post({ type: 'error', message: `无法打开文件: ${e?.message || e}` });
    }
  }

  private revealLine(abs: string, line: number): void {
    const editor = vscode.window.visibleTextEditors.find((e) => e.document.uri.fsPath === abs);
    if (!editor) {
      return;
    }
    const pos = new vscode.Position(Math.max(0, line - 1), 0);
    editor.selection = new vscode.Selection(pos, pos);
    editor.revealRange(new vscode.Range(pos, pos), vscode.TextEditorRevealType.InCenter);
  }

  private async openFallbackDiff(repoPath: string, baseRef: string, file: string, abs: string, line: number): Promise<void> {
    let base = '';
    try {
      base = await gitShow(repoPath, baseRef, file);
    } catch {
      base = '';
    }
    const tmp = path.join(
      os.tmpdir(),
      `reqboard-base-${Date.now()}-${Math.random().toString(36).slice(2)}${path.extname(abs)}`
    );
    fs.writeFileSync(tmp, base, 'utf8');
    const opts: vscode.TextDocumentShowOptions = { preview: false };
    if (line > 0) {
      const pos = new vscode.Position(line - 1, 0);
      opts.selection = new vscode.Range(pos, pos);
    }
    await vscode.commands.executeCommand(
      'vscode.diff',
      vscode.Uri.file(tmp),
      vscode.Uri.file(abs),
      `${path.basename(repoPath)}: ${file} (base ↔ working)`,
      opts
    );
  }
}
