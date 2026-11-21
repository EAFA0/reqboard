import * as path from 'path';
import * as vscode from 'vscode';

import { BoardPanel } from './boardPanel';
import { findRootUp, isReqboardRoot, normalizeRoot } from './util';

export function activate(context: vscode.ExtensionContext): void {
  const output = vscode.window.createOutputChannel('ReqBoard');
  context.subscriptions.push(output);

  context.subscriptions.push(
    vscode.commands.registerCommand('reqboard.openBoard', async (uri?: vscode.Uri) => {
      try {
        const root = await resolveRoot(uri);
        if (!root) {
          return;
        }
        output.appendLine(`open board: ${root}`);
        BoardPanel.createOrShow(context, root);
      } catch (e: any) {
        void vscode.window.showErrorMessage(`ReqBoard: ${e?.message || e}`);
      }
    })
  );
}

export function deactivate(): void {
  // Panels are disposed by VS Code.
}

async function resolveRoot(uri?: vscode.Uri): Promise<string | undefined> {
  if (uri) {
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

  const folders = (vscode.workspace.workspaceFolders || []).map((f) => f.uri.fsPath);
  const roots = folders.filter(isReqboardRoot);
  if (roots.length === 1) {
    return roots[0];
  }
  if (roots.length > 1) {
    const pick = await vscode.window.showQuickPick(roots, {
      placeHolder: '选择包含 .reqboard/ 的需求 worktree',
    });
    return pick || undefined;
  }
  for (const folder of folders) {
    const up = findRootUp(folder);
    if (up) {
      return up;
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
