import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

import { CLIError, ReqboardCLI, gitShow } from './cli';
import { findSymbolLine } from './util';
import { renderHTML } from './webview';

const WATCHED = ['events.jsonl', 'dag.json', 'bindings.json', 'prd.md', 'config.json'];

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
      this.post({ type: 'snapshot', data: res.data });
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
      case 'layout':
        await this.exec(['layout', '--actor', 'human'], '已按左细右粗重新排版');
        return;
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
        await vscode.env.clipboard.writeText(
          '/goal 读取 .reqboard/ 当前状态，循环执行 `reqboard dispatch` 并按 P0→P1→P2 清单并发派发 Worker；每个 Worker 只领 1 个 node_id，走 claim → 改码 → bind → check --node → done；无项时输出 QUIESCENT 阻塞账单并停止。'
        );
        this.post({ type: 'toast', level: 'info', message: '已复制 /goal 推进指令' });
        return;
      default:
        return;
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
    try {
      await vscode.commands.executeCommand('focusedGit.openFileComparison', uri);
      opened = true;
    } catch {
      opened = false;
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
