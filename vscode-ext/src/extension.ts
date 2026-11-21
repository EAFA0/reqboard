import * as path from 'path';
import * as vscode from 'vscode';

import { BoardPanel } from './boardPanel';
import { ReqboardCLI } from './cli';
import { findRootUp, isReqboardRoot, normalizeRoot } from './util';

interface SidebarNode {
  label: string;
  description?: string;
  tooltip?: string;
  icon?: string;
  command?: vscode.Command;
}

class ReqboardSidebarProvider implements vscode.TreeDataProvider<SidebarNode> {
  private readonly emitter = new vscode.EventEmitter<SidebarNode | undefined>();
  readonly onDidChangeTreeData = this.emitter.event;

  constructor(private readonly extensionPath: string) {}

  refresh(): void {
    this.emitter.fire(undefined);
  }

  getTreeItem(element: SidebarNode): vscode.TreeItem {
    const item = new vscode.TreeItem(element.label, vscode.TreeItemCollapsibleState.None);
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

  async getChildren(): Promise<SidebarNode[]> {
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
          description: 'reqboard init',
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
        label: `打开三栏看板`,
        description: name,
        tooltip: root,
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
        const done = nodes.filter((n) => n.status === 'done').length;
        const cov = snap?.prd?.coverage;
        const covText = cov ? `PRD ${Number(cov.percent || 0).toFixed(0)}%` : '';
        items.push({
          label: `节点进度 ${done}/${nodes.length}`,
          description: covText,
          icon: 'checklist',
          command: {
            command: 'reqboard.openBoard',
            title: 'Open Board',
            arguments: [vscode.Uri.file(root)],
          },
        });

        const diff = snap?.diff || {};
        const orphan = (diff.orphan || []).length;
        const toDelete = (diff.to_delete || []).length;
        const broken = (diff.broken_bindings || []).length;
        const alerts = orphan + toDelete + broken;
        items.push({
          label: alerts > 0 ? `Diff 告警 ${alerts}` : 'Diff 干净',
          description: `Orphan ${orphan} · To-Delete ${toDelete} · Broken ${broken}`,
          icon: alerts > 0 ? 'warning' : 'pass',
          command: {
            command: 'reqboard.openBoard',
            title: 'Open Board',
            arguments: [vscode.Uri.file(root)],
          },
        });

        const dp = snap?.dispatch || {};
        if (dp.status) {
          const cnt = (dp.items || []).length;
          items.push({
            label: `调度态 ${dp.status}`,
            description: dp.status === 'QUIESCENT' ? '等待人工拍板/外部依赖' : `${cnt} 项可推进`,
            icon: dp.status === 'QUIESCENT' ? 'clock' : 'play-circle',
            command: {
              command: 'reqboard.openBoard',
              title: 'Open Board',
              arguments: [vscode.Uri.file(root)],
            },
          });
        }
      } catch {
        items.push({
          label: '点击上方打开看板',
          description: '状态读取失败',
          icon: 'warning',
          command: {
            command: 'reqboard.openBoard',
            title: 'Open Board',
            arguments: [vscode.Uri.file(root)],
          },
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
