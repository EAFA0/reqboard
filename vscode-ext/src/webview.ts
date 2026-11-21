import { randomBytes } from 'crypto';
import * as vscode from 'vscode';

/** Build the single-page three-column webview (no CDN, no bundler). */
export function renderHTML(webview: vscode.Webview, extensionUri: vscode.Uri): string {
  const nonce = randomBytes(16).toString('base64');
  const cssUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, 'media', 'board.css'));
  const jsUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, 'media', 'board.js'));
  const csp = [
    `default-src 'none'`,
    `img-src ${webview.cspSource} data:`,
    `style-src ${webview.cspSource} 'unsafe-inline'`,
    `font-src ${webview.cspSource}`,
    `script-src ${webview.cspSource} 'nonce-${nonce}'`,
  ].join('; ');

  return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="${csp}">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="stylesheet" href="${cssUri}">
<title>ReqBoard</title>
</head>
<body>
<header id="topbar">
  <div class="brand">ReqBoard</div>
  <div class="orch">
    <span id="orch-light" class="light"></span>
    <span id="orch-state">—</span>
    <span id="orch-session" class="muted"></span>
  </div>
  <div id="top-stats" class="stats"></div>
  <div class="top-actions">
    <button id="btn-undo" title="撤销上一次写操作（Ctrl+Z / Cmd+Z）">↩ 撤销 (0)</button>
    <button id="btn-redo" title="重做被撤销的操作（Ctrl+Shift+Z / Cmd+Shift+Z）">↪ 重做 (0)</button>
    <button id="btn-ext-canvas" title="切换主画布是否显示 external / collaborator / todo / out_of_scope 节点">画布含前置</button>
    <button id="btn-refresh" title="重新执行 status 与多仓 git diff">刷新 Diff</button>
    <button id="btn-fit" title="缩放以完整显示 DAG">适应画布</button>
    <button id="btn-layout" title="按自然拓扑深度重新排版（milestone/e2e 钉最右）">自动左到右排版</button>
    <button id="btn-agent" class="primary" title="发射本地 Agent 终端或复制任务指令">发射 Agent ▾</button>
    <button id="btn-orch" title="打开 Orchestrator session">跳回 Orchestrator Session</button>
  </div>
</header>
<main id="columns">
  <section id="prd-pane" class="pane">
    <button id="btn-expand-prd" class="pane-collapsed-rail" title="展开左侧 PRD 栏">
      <span class="rail-arrow">▸</span>
      <span class="rail-label">PRD</span>
    </button>
    <div class="pane-head prd-head">
      <div id="source-tabs" class="source-tabs"></div>
      <span class="pane-head-right">
        <span id="coverage-badge" class="badge"></span>
        <button id="btn-collapse-prd" class="pane-collapse-btn" title="折叠左侧 PRD 栏">‹</button>
      </span>
    </div>
    <div id="prd-list" class="pane-body"></div>
  </section>
  <section id="dag-pane" class="pane">
    <div class="pane-head">
      <span>DAG（自然拓扑）</span>
      <span class="pane-actions"><button id="btn-add-node">+ 节点</button></span>
    </div>
    <div id="external-dock">
      <div class="dock-head">
        <button id="btn-dock-toggle" title="隐藏 / 展开前置依赖停靠栏">隐藏</button>
        <span id="dock-count" class="dock-count">前置依赖 0/0</span>
        <span class="dock-filters dock-type-filters" title="前置类型">
          <button data-dock-type="all" class="active">全部</button>
          <button data-dock-type="external">外部</button>
          <button data-dock-type="todo">TODO</button>
        </span>
        <span class="dock-filters dock-status-filters" title="就绪状态">
          <button data-dock-filter="all" class="active">全部</button>
          <button data-dock-filter="waiting">待就绪</button>
          <button data-dock-filter="ready">已就绪</button>
        </span>
        <button id="btn-add-todo" class="todo-add-btn" title="新建前置 TODO（DDL / 网关配置 / Feature Flag）">+ 新增 TODO</button>
        <button id="btn-redline" class="redline-capsule" title="PRD 明确本期不做，禁止 claim/bind">红线 Out-of-Scope (0)</button>
      </div>
      <div id="dock-cards" class="dock-cards"></div>
      <div id="redline-panel" class="redline-panel hidden"></div>
    </div>
    <div id="external-focus" class="external-focus hidden"></div>
    <div id="anchor-links" class="anchor-links hidden"></div>
    <div id="dag-viewport" class="pane-body">
      <div id="dag-world">
        <svg id="dag-edges" xmlns="http://www.w3.org/2000/svg"></svg>
        <div id="dag-nodes"></div>
      </div>
      <div id="dag-hint" class="hint">拖拽平移 · 滚轮缩放 · 双击空白建节点 · 从节点右侧圆点拖拽连线</div>
    </div>
  </section>
  <section id="diff-pane" class="pane">
    <button id="btn-expand-diff" class="pane-collapsed-rail" title="展开右侧 Diff 栏">
      <span class="rail-arrow">◂</span>
      <span class="rail-label">Diff</span>
    </button>
    <div class="pane-head">
      <span>Diff / Review</span>
      <span class="pane-head-right">
        <span id="sel-node-badge" class="badge"></span>
        <button id="btn-collapse-diff" class="pane-collapse-btn" title="折叠右侧 Diff 栏">›</button>
      </span>
    </div>
    <div id="diff-filter" class="diff-filter">
      <label class="df-toggle" title="勾选后单测文件不计入 Diff 审计"><input type="checkbox" id="df-exclude-tests" checked> 排除单测</label>
      <input id="df-include" class="df-glob" placeholder="Include: *.go, src/**" title="只审计命中这些 glob 的文件（逗号分隔，可写 repo:path）">
      <input id="df-exclude" class="df-glob" placeholder="Exclude: vendor/**, *_gen.go" title="忽略这些 glob 的文件（逗号分隔）">
      <button id="df-apply" title="写入 .reqboard/config.json 并刷新 Diff">应用</button>
      <button id="df-reset" class="df-reset" title="恢复默认过滤（排除单测 + 默认生成代码规则）">重置</button>
      <input id="df-search" class="df-search" placeholder="搜索 仓库/文件/符号" title="即时过滤右栏条目，不写入配置">
      <span id="df-ignored" class="df-ignored"></span>
    </div>
    <div id="diff-body" class="pane-body"></div>
  </section>
</main>
<div id="popup-layer"></div>
<div id="toast" class="toast hidden"></div>
<script nonce="${nonce}" src="${jsUri}"></script>
</body>
</html>`;
}
