import * as path from 'path';
import * as vscode from 'vscode';

import { BoardPanel } from './boardPanel';
import { ReqboardCLI } from './cli';
import { LAUNCHER_ACTIONS, LAUNCHER_STATS_KEY, LauncherStatsMap } from './launcher';
import { findRootUp, isReqboardRoot, normalizeRoot } from './util';

interface SidebarNode {
  label: string;
  description?: string;
  tooltip?: string;
  icon?: string;
  command?: vscode.Command;
  children?: SidebarNode[];
  expanded?: boolean;
}

class ReqboardSidebarProvider implements vscode.TreeDataProvider<SidebarNode> {
  private readonly emitter = new vscode.EventEmitter<SidebarNode | undefined>();
  readonly onDidChangeTreeData = this.emitter.event;

  constructor(private readonly extensionPath: string) {}

  refresh(): void {
    this.emitter.fire(undefined);
  }

  getTreeItem(element: SidebarNode): vscode.TreeItem {
    const state = element.children && element.children.length > 0
      ? (element.expanded ? vscode.TreeItemCollapsibleState.Expanded : vscode.TreeItemCollapsibleState.Collapsed)
      : vscode.TreeItemCollapsibleState.None;
    const item = new vscode.TreeItem(element.label, state);
    item.description = element.description;
    item.tooltip = element.tooltip || element.label;
    if (element.icon) {
      item.iconPath = new vscode.ThemeIcon(element.icon);
    }
    if (element.command) {
      item.command = element.command;
    }
    return item;
  }

  async getChildren(element?: SidebarNode): Promise<SidebarNode[]> {
    if (element) {
      return element.children || [];
    }
    const roots = discoverWorkspaceRoots();
    const items: SidebarNode[] = [];

    if (roots.length === 0) {
      const firstFolder = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
      items.push({
        label: '当前工作区未初始化 .reqboard/',
        description: firstFolder ? path.basename(firstFolder) : '未打开目录',
        icon: 'info',
      });
      if (firstFolder) {
        items.push({
          label: '一键初始化 ReqBoard',
          description: 'reqboard init（仅本地建目录，不调 AI）',
          tooltip: `在 ${firstFolder} 下执行 reqboard init 并打开看板`,
          icon: 'add',
          command: {
            command: 'reqboard.initWorkspace',
            title: 'Initialize ReqBoard',
            arguments: [vscode.Uri.file(firstFolder)],
          },
        });
      }
      items.push({
        label: '选择其他工作区打开...',
        icon: 'folder-opened',
        command: {
          command: 'reqboard.openOtherBoard',
          title: 'Open Other Workspace',
        },
      });
      return items;
    }

    for (const root of roots) {
      const name = path.basename(root);
      items.push({
        label: '打开三栏看板',
        description: name,
        tooltip: `打开或切换到 ${root} 的三栏看板`,
        icon: 'type-hierarchy',
        command: {
          command: 'reqboard.openBoard',
          title: 'Open Board',
          arguments: [vscode.Uri.file(root)],
        },
      });

      try {
        const res = await new ReqboardCLI(root, this.extensionPath).run(['status'], true);
        const snap = res.data || {};
        const nodes: any[] = snap?.dag?.nodes || [];
        const repos: any[] = snap?.config?.repos || [];
        const repoMap = new Map<string, string>();
        for (const r of repos) {
          const rp = path.isAbsolute(r.path) ? r.path : path.join(root, r.path);
          repoMap.set(r.name, rp);
        }

        const done = nodes.filter((n) => n.status === 'done').length;
        const impl = nodes.filter((n) => n.status === 'implemented' || n.status === 'reviewed').length;
        const cov = snap?.prd?.coverage;
        const covText = cov ? `PRD ${Number(cov.percent || 0).toFixed(0)}%` : '';
        items.push({
          label: `进度 ${done}/${nodes.length} Done`,
          description: `${impl} 已实现 · ${covText}`,
          tooltip: `仅状态概览：已完成 ${done}，已实现/待审 ${impl}，总计 ${nodes.length}`,
          icon: 'checklist',
        });

        const buckets = snap?.check?.buckets || {};
        const orphan: any[] = buckets.orphan || [];
        const toDelete: any[] = buckets.to_delete || [];
        const broken: any[] = buckets.broken_bindings || [];
        const alerts = orphan.length + toDelete.length + broken.length;
        const alertChildren: SidebarNode[] = [];
        const pushDiffItem = (tag: string, icon: string, entry: any) => {
          const baseDir = repoMap.get(entry.repo);
          const absFile = baseDir && entry.file ? path.join(baseDir, entry.file) : undefined;
          alertChildren.push({
            label: `[${tag}] ${entry.symbol || path.basename(entry.file || '')}`,
            description: `${entry.repo}:${entry.file}`,
            tooltip: entry.correction || entry.reason || `${entry.repo}:${entry.file}::${entry.symbol}`,
            icon,
            command: absFile ? {
              command: 'reqboard.openFileDiff',
              title: 'Open File Diff',
              arguments: [vscode.Uri.file(absFile), Number(entry.start || 0)],
            } : undefined,
          });
        };
        for (const e of orphan.slice(0, 30)) {
          pushDiffItem('Orphan', 'warning', e);
        }
        for (const e of toDelete.slice(0, 20)) {
          pushDiffItem('To-Delete', 'trash', e);
        }
        for (const e of broken.slice(0, 20)) {
          pushDiffItem('Broken', 'error', e);
        }
        items.push({
          label: alerts > 0 ? `Diff 告警 (${alerts})` : 'Diff 干净',
          description: `Orphan ${orphan.length} · To-Delete ${toDelete.length} · Broken ${broken.length}`,
          tooltip: alerts > 0 ? '展开查看具体异常符号，点击直接打开 Diff' : '当前无 Orphan / To-Delete / Broken',
          icon: alerts > 0 ? 'warning' : 'pass',
          children: alertChildren,
        });

        const dp = snap?.dispatch || {};
        if (dp.status) {
          const dpItems: any[] = dp.items || [];
          const dpChildren: SidebarNode[] = dpItems.map((it) => ({
            label: `P${it.priority} ${it.title || it.node_id}`,
            description: it.node_id,
            tooltip: `${it.reason || ''}\n点击复制命令：${it.command || ''}`,
            icon: 'arrow-right',
            command: it.command ? {
              command: 'reqboard.copyText',
              title: 'Copy Command',
              arguments: [String(it.command), `已复制：${it.command}`],
            } : undefined,
          }));
          items.push({
            label: `待推进 ${dp.status}`,
            description: dp.status === 'QUIESCENT' ? '等待人工拍板/外部依赖' : `${dpItems.length} 项可推进（展开复制命令）`,
            tooltip: '展开查看当前可并发推进的 P0/P1/P2 节点，点击复制对应 CLI 命令',
            icon: dp.status === 'QUIESCENT' ? 'clock' : 'play-circle',
            children: dpChildren,
          });
        }
      } catch {
        items.push({
          label: '状态读取失败（点顶部刷新重试）',
          icon: 'warning',
        });
      }
    }

    items.push({
      label: '打开其他工作区...',
      icon: 'folder-opened',
      command: {
        command: 'reqboard.openOtherBoard',
        title: 'Open Other Workspace',
      },
    });
    return items;
  }
}

function discoverWorkspaceRoots(): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const f of vscode.workspace.workspaceFolders || []) {
    const p = f.uri.fsPath;
    let found: string | undefined;
    if (isReqboardRoot(p)) {
      found = p;
    } else {
      found = findRootUp(p);
    }
    if (found && !seen.has(found)) {
      seen.add(found);
      out.push(found);
    }
  }
  return out;
}

export function activate(context: vscode.ExtensionContext): void {
  const output = vscode.window.createOutputChannel('ReqBoard');
  context.subscriptions.push(output);

  const sidebar = new ReqboardSidebarProvider(context.extensionPath);
  context.subscriptions.push(
    vscode.window.registerTreeDataProvider('reqboard.sidebar', sidebar),
    vscode.workspace.onDidChangeWorkspaceFolders(() => sidebar.refresh()),
  );

  // Status bar shortcut for one-click access even when sidebar is collapsed.
  const statusBar = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
  statusBar.text = '$(type-hierarchy) ReqBoard';
  statusBar.tooltip = '打开 ReqBoard 三栏需求看板';
  statusBar.command = 'reqboard.openBoard';
  statusBar.show();
  context.subscriptions.push(statusBar);

  context.subscriptions.push(
    vscode.commands.registerCommand('reqboard.refreshSidebar', () => sidebar.refresh()),
    vscode.commands.registerCommand('reqboard.showLauncherStats', async () => {
      const stats = context.globalState.get<LauncherStatsMap>(LAUNCHER_STATS_KEY, {});
      const rows = LAUNCHER_ACTIONS.map((a) => {
        const st = stats[a.id] || { total: 0, terminal: 0, clipboard: 0, lastAt: '' };
        return {
          label: `${a.label}`,
          description: `${st.total} 次（终端 ${st.terminal} · 复制 ${st.clipboard}）`,
          detail: st.lastAt ? `最近使用：${st.lastAt}` : '尚未使用',
          total: st.total,
        };
      }).sort((a, b) => b.total - a.total);
      const resetItem = {
        label: '$(trash) 重置本地使用统计',
        description: '',
        detail: '清空跨工作区的发射器点击计数',
        total: -1,
      };
      const picked = await vscode.window.showQuickPick([...rows, resetItem], {
        title: 'ReqBoard 发射器本地使用统计（跨工作区）',
        placeHolder: '按使用次数从高到低排序，用于评估各入口取舍',
      });
      if (picked && picked.total === -1) {
        await context.globalState.update(LAUNCHER_STATS_KEY, {});
        void vscode.window.showInformationMessage('ReqBoard: 发射器使用统计已重置');
      }
    }),
    vscode.commands.registerCommand('reqboard.copyText', async (text: string, msg?: string) => {
      if (!text) {
        return;
      }
      await vscode.env.clipboard.writeText(text);
      void vscode.window.showInformationMessage(msg || `ReqBoard: 已复制 ${text}`);
    }),
    vscode.commands.registerCommand('reqboard.openFileDiff', async (uri: vscode.Uri, line?: number) => {
      try {
        const cmds = await vscode.commands.getCommands(true);
        if (cmds.includes('focusedGit.openFileComparison')) {
          await vscode.commands.executeCommand('focusedGit.openFileComparison', uri);
        } else {
          await vscode.commands.executeCommand('vscode.open', uri);
        }
        const ed = vscode.window.activeTextEditor;
        if (ed && line && line > 0) {
          const pos = new vscode.Position(Math.max(0, line - 1), 0);
          ed.selection = new vscode.Selection(pos, pos);
          ed.revealRange(new vscode.Range(pos, pos), vscode.TextEditorRevealType.InCenter);
        }
      } catch (e: any) {
        void vscode.window.showErrorMessage(`ReqBoard 打开 Diff 失败: ${e?.message || e}`);
      }
    }),
    vscode.commands.registerCommand('reqboard.openBoard', async (uri?: vscode.Uri) => {
      try {
        const root = await resolveRoot(uri, false);
        if (!root) {
          return;
        }
        output.appendLine(`open board: ${root}`);
        BoardPanel.createOrShow(context, root);
        sidebar.refresh();
      } catch (e: any) {
        void vscode.window.showErrorMessage(`ReqBoard: ${e?.message || e}`);
      }
    }),
    vscode.commands.registerCommand('reqboard.openOtherBoard', async () => {
      try {
        const root = await resolveRoot(undefined, true);
        if (!root) {
          return;
        }
        output.appendLine(`open board: ${root}`);
        BoardPanel.createOrShow(context, root);
        sidebar.refresh();
      } catch (e: any) {
        void vscode.window.showErrorMessage(`ReqBoard: ${e?.message || e}`);
      }
    }),
    vscode.commands.registerCommand('reqboard.initWorkspace', async (uri?: vscode.Uri) => {
      try {
        let dir = uri?.fsPath || vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
        if (!dir) {
          const picked = await vscode.window.showOpenDialog({
            canSelectFiles: false,
            canSelectFolders: true,
            canSelectMany: false,
            openLabel: '选择要初始化 ReqBoard 的需求目录',
          });
          dir = picked?.[0]?.fsPath;
        }
        if (!dir) {
          return;
        }
        const name = path.basename(dir);
        await new ReqboardCLI(dir, context.extensionPath).run(['init', '--name', name], false);
        output.appendLine(`initialized reqboard at ${dir}`);
        sidebar.refresh();
        BoardPanel.createOrShow(context, dir);
      } catch (e: any) {
        void vscode.window.showErrorMessage(`ReqBoard init 失败: ${e?.message || e}`);
      }
    }),
  );
}

export function deactivate(): void {
  // Panels are disposed by VS Code.
}

async function resolveRoot(uri: vscode.Uri | undefined, forcePicker: boolean): Promise<string | undefined> {
  if (uri && !forcePicker) {
    const direct = normalizeRoot(uri.fsPath);
    if (isReqboardRoot(direct)) {
      return direct;
    }
    const up = findRootUp(uri.fsPath);
    if (up) {
      return up;
    }
    void vscode.window.showErrorMessage(`ReqBoard: ${direct} 下没有 .reqboard/，请先运行 reqboard init。`);
    return undefined;
  }

  if (!forcePicker) {
    const roots = discoverWorkspaceRoots();
    if (roots.length === 1) {
      return roots[0];
    }
    if (roots.length > 1) {
      const pick = await vscode.window.showQuickPick(roots, {
        placeHolder: '选择包含 .reqboard/ 的需求 worktree',
      });
      return pick || undefined;
    }
  }

  const picked = await vscode.window.showOpenDialog({
    canSelectFiles: false,
    canSelectFolders: true,
    canSelectMany: false,
    openLabel: '选择需求 worktree（含 .reqboard/）',
  });
  if (!picked || picked.length === 0) {
    return undefined;
  }
  const p = normalizeRoot(picked[0].fsPath);
  if (isReqboardRoot(p)) {
    return p;
  }
  const up = findRootUp(p);
  if (up) {
    return up;
  }
  void vscode.window.showErrorMessage(`ReqBoard: ${path.basename(p)} 下没有 .reqboard/，请先运行 reqboard init。`);
  return undefined;
}
