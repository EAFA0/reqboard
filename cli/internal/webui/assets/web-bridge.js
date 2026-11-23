/* Standalone browser bridge for `reqboard serve`.
 * Implements the acquireVsCodeApi() transport expected by board.js, plus
 * live SSE hot-reload and a built-in Unified / Side-by-Side Code Diff viewer.
 */
(function () {
  'use strict';

  if (typeof window.acquireVsCodeApi === 'function') {
    return;
  }

  const STATE_KEY = 'reqboard.web.uiState:' + location.pathname;
  const THEME_KEY = 'reqboard.web.theme';
  const DIFF_MODE_KEY = 'reqboard.web.diffMode';
  const LAUNCH_STATS_KEY = 'reqboard.web.launcherStats';

  const DEFAULT_PROMPTS = {
    push_goal: '作为 ReqBoard 协调者（只调度与汇总，不自己串行写业务代码），循环执行 reqboard dispatch 按 P0→P1→P2 并发扇出多个单节点子代理：每个子代理只领 1 个 node_id，走 claim→改码/包内单测（不跑全仓单测）→bind 仅生产与手写 mock→check --node→满足门禁则 done 否则显式更新状态为 implemented 释放租约；每波回收后做整仓校验并继续 dispatch，直到返回 QUIESCENT 输出阻塞账单。',
    review_simplify: '请对照当前工作区 .reqboard/prd.md、补充来源、节点 bindings、Orphan Diff、To-Delete 与 Mock 债，全面 Review 本分支多仓改动：按仓库或模块并发扇出多个只读子代理分片核查，主代理统一汇总冗余、越界与重复代码并先输出精简方案，待我确认后再修改与清理。',
    audit_orphan: '请先运行 reqboard check --json 获取所有 Orphan Diff 与 To-Delete，按仓库或文件分片并发扇出只读子代理核查 git diff 与节点归属，主代理统一归类为【确凿漏绑 / 疑似越界冗余 / 同事合入或生成文件 / DAG 漏建节点】；严禁为了清零乱绑或未经确认删代码，先输出分类清单等我拍板。',
    plan_dag: '请结合当前 .reqboard/prd.md、补充来源与现有代码库，按 ReqBoard 规范规划或校准 DAG：代码调研与已有节点核验阶段按仓库/模块并发扇出只读子代理加速，最终由主代理串行收口写入 .reqboard/（反碎化、无伪硬边、区分 todo 与真实外部依赖、核对 stale 节点）；若已有实现代码，同步核验 bindings 与 4 道门禁，达标则 reqboard done，受外部/mock/todo/人工批注阻塞则标 implemented 并列明缺口，跑独立 Critic 自审后再汇报。',
    node_work: '请执行 reqboard claim {nodeId} 获取节点验收标准、PRD 锚点与外部依据，推进该节点实现并运行包内单测，完成 bind（仅生产实现与手写 mock）与 reqboard check --node {nodeId}，达标则 done 否则显式更新状态释放租约。',
    node_fix: '请执行 reqboard claim {nodeId} 查看该节点的未解决 Review 批注与绑定符号，严格按批注逐条修复代码与单测，更新 bind 并跑 reqboard check --node {nodeId}；人类批注禁止自审关闭，修复后执行 reqboard node update {nodeId} --status implemented 释放租约并回报等我点 Reviewed 通过。',
  };

  let serverCaps = { open_ide_enabled: false };
  let diffState = {
    open: false,
    req: null,
    data: null,
    mode: localStorage.getItem(DIFF_MODE_KEY) || 'unified',
    fullContext: false,
  };

  function emitToBoard(msg) {
    window.dispatchEvent(new MessageEvent('message', { data: msg }));
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    }[c]));
  }

  function readLaunchCounts() {
    try {
      return JSON.parse(localStorage.getItem(LAUNCH_STATS_KEY) || '{}') || {};
    } catch {
      return {};
    }
  }

  function bumpLaunchCount(action) {
    const counts = readLaunchCounts();
    counts[action] = (Number(counts[action]) || 0) + 1;
    try {
      localStorage.setItem(LAUNCH_STATS_KEY, JSON.stringify(counts));
    } catch {
      // ignore storage quota errors
    }
    return counts;
  }

  async function copyClipboard(text) {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
      try {
        await navigator.clipboard.writeText(text);
        return true;
      } catch {
        // fallback for non-HTTPS forwarded ports
      }
    }
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand('copy');
    } catch {
      ok = false;
    }
    ta.remove();
    return ok;
  }

  async function fetchSnapshot() {
    try {
      const resp = await fetch('/api/status', { cache: 'no-store' });
      const data = await resp.json();
      if (!resp.ok || data.ok === false) {
        emitToBoard({ type: 'error', message: data.error || '读取状态失败' });
        return;
      }
      if (data.web) {
        serverCaps = data.web;
      }
      data.launcher = { configured: false, counts: readLaunchCounts() };
      emitToBoard({ type: 'snapshot', data });
    } catch (e) {
      emitToBoard({ type: 'error', message: '无法连接本地 reqboard serve: ' + (e && e.message ? e.message : e) });
    }
  }

  function startSSE() {
    if (typeof EventSource !== 'function') {
      return;
    }
    const es = new EventSource('/api/events');
    es.addEventListener('snapshot', (ev) => {
      try {
        const data = JSON.parse(ev.data);
        if (data.web) {
          serverCaps = data.web;
        }
        data.launcher = { configured: false, counts: readLaunchCounts() };
        emitToBoard({ type: 'snapshot', data });
      } catch {
        // ignore malformed event
      }
    });
  }

  /* ---------------- Built-in Code Diff Viewer ---------------- */

  function ensureDiffModal() {
    if (document.getElementById('web-diff-backdrop')) {
      return;
    }
    const backdrop = document.createElement('div');
    backdrop.id = 'web-diff-backdrop';
    backdrop.className = 'hidden';
    backdrop.innerHTML = `<div id="web-diff-modal" role="dialog" aria-modal="true">
      <div class="wdiff-head">
        <div class="wdiff-title" id="wdiff-title">Code Diff</div>
        <div class="wdiff-actions">
          <button id="wdiff-prev-hunk" title="上一个变更块">↑ 变更</button>
          <button id="wdiff-next-hunk" title="下一个变更块">↓ 变更</button>
          <button id="wdiff-unified" title="Unified 单栏视图">Unified</button>
          <button id="wdiff-split" title="Side-by-Side 双栏对比">Split</button>
          <button id="wdiff-context" title="切换仅看变更片段 / 完整文件上下文">完整上下文</button>
          <button id="wdiff-copy-loc" title="复制 file:line（适合 GoLand Cmd+Shift+O）">复制 file:line</button>
          <button id="wdiff-copy-abs" title="复制文件绝对路径">复制路径</button>
          <button id="wdiff-open-ide" class="hidden" title="调用 reqboard serve --open-cmd 在本地 IDE 打开">在 IDE 打开</button>
          <button id="wdiff-close" title="关闭（Esc）">✕</button>
        </div>
      </div>
      <div class="wdiff-body" id="wdiff-body"></div>
      <div class="wdiff-foot">
        <span id="wdiff-meta"></span>
        <span>点击行号复制 file:line · Esc 关闭 · Unified / Split 切换</span>
      </div>
    </div>`;
    document.body.appendChild(backdrop);

    backdrop.addEventListener('click', (e) => {
      if (e.target === backdrop) {
        closeDiffModal();
      }
    });
    document.getElementById('wdiff-close').onclick = closeDiffModal;
    let hunkIdx = -1;
    const jumpHunk = (delta) => {
      const hunks = [...document.querySelectorAll('#wdiff-body tr.hunk')];
      if (!hunks.length) return;
      hunkIdx = (hunkIdx + delta + hunks.length) % hunks.length;
      hunks[hunkIdx].scrollIntoView({ block: 'start', behavior: 'smooth' });
    };
    document.getElementById('wdiff-prev-hunk').onclick = () => jumpHunk(-1);
    document.getElementById('wdiff-next-hunk').onclick = () => jumpHunk(1);
    document.getElementById('wdiff-body').addEventListener('click', async (e) => {
      const ln = e.target.closest('td.ln');
      if (!ln) return;
      const lineNo = Number(ln.textContent.trim() || 0);
      if (!lineNo || !diffState.data) return;
      const loc = `${diffState.data.file}:${lineNo}`;
      await copyClipboard(loc);
      emitToBoard({ type: 'toast', message: '已复制 ' + loc });
    });
    document.getElementById('wdiff-unified').onclick = () => {
      diffState.mode = 'unified';
      localStorage.setItem(DIFF_MODE_KEY, 'unified');
      renderDiffModal();
    };
    document.getElementById('wdiff-split').onclick = () => {
      diffState.mode = 'split';
      localStorage.setItem(DIFF_MODE_KEY, 'split');
      renderDiffModal();
    };
    document.getElementById('wdiff-context').onclick = () => {
      diffState.fullContext = !diffState.fullContext;
      if (diffState.req) {
        void loadDiff(diffState.req);
      }
    };
    document.getElementById('wdiff-copy-loc').onclick = async () => {
      const d = diffState.data;
      if (!d) return;
      const loc = d.focus_line > 0 ? `${d.file}:${d.focus_line}` : d.file;
      await copyClipboard(loc);
      emitToBoard({ type: 'toast', message: '已复制 ' + loc });
    };
    document.getElementById('wdiff-copy-abs').onclick = async () => {
      const d = diffState.data;
      if (!d) return;
      const loc = d.focus_line > 0 ? `${d.abs_path}:${d.focus_line}` : d.abs_path;
      await copyClipboard(loc);
      emitToBoard({ type: 'toast', message: '已复制绝对路径' });
    };
    document.getElementById('wdiff-open-ide').onclick = async () => {
      const d = diffState.data;
      if (!d) return;
      const resp = await fetch('/api/open-ide', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ file: d.abs_path, line: d.focus_line || 1 }),
      });
      const res = await resp.json();
      if (!resp.ok || res.ok === false) {
        emitToBoard({ type: 'error', message: res.error || '打开本地 IDE 失败' });
      } else {
        emitToBoard({ type: 'toast', message: '已唤起本地 IDE' });
      }
    };
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && diffState.open) {
        e.stopPropagation();
        closeDiffModal();
      }
    }, true);
  }

  function closeDiffModal() {
    diffState.open = false;
    const el = document.getElementById('web-diff-backdrop');
    if (el) {
      el.classList.add('hidden');
    }
  }

  async function loadDiff(req) {
    ensureDiffModal();
    diffState.open = true;
    diffState.req = req;
    const backdrop = document.getElementById('web-diff-backdrop');
    backdrop.classList.remove('hidden');
    document.getElementById('wdiff-body').innerHTML = '<div class="wdiff-empty">正在读取 git diff…</div>';
    const params = new URLSearchParams({
      repo: req.repo || '',
      file: req.file || '',
      symbol: req.symbol || '',
      line: String(req.line || 0),
      context: diffState.fullContext ? '99999' : '5',
    });
    try {
      const resp = await fetch('/api/diff?' + params.toString(), { cache: 'no-store' });
      const data = await resp.json();
      if (!resp.ok || data.ok === false) {
        document.getElementById('wdiff-body').innerHTML = `<div class="wdiff-empty">${esc(data.error || '读取 diff 失败')}</div>`;
        return;
      }
      diffState.data = data;
      renderDiffModal();
    } catch (e) {
      document.getElementById('wdiff-body').innerHTML = `<div class="wdiff-empty">读取 diff 失败：${esc(e && e.message ? e.message : e)}</div>`;
    }
  }

  function renderUnified(hunks, focusLine) {
    let rows = '';
    for (const h of hunks) {
      rows += `<tr class="hunk"><td colspan="4">${esc(h.header)}</td></tr>`;
      for (const l of (h.lines || [])) {
        const cls = [l.kind];
        if (focusLine > 0 && (l.new_line === focusLine || (l.kind === 'del' && l.old_line === focusLine))) {
          cls.push('focus-line');
        }
        const sig = l.kind === 'add' ? '+' : l.kind === 'del' ? '-' : ' ';
        const oldN = l.old_line > 0 ? l.old_line : '';
        const newN = l.new_line > 0 ? l.new_line : '';
        rows += `<tr class="${cls.join(' ')}" data-new-line="${l.new_line || 0}">
          <td class="ln">${oldN}</td>
          <td class="ln">${newN}</td>
          <td class="sig">${sig}</td>
          <td class="code">${esc(l.text)}</td>
        </tr>`;
      }
    }
    return `<table class="wdiff-table"><colgroup><col style="width:52px"><col style="width:52px"><col style="width:22px"><col></colgroup><tbody>${rows}</tbody></table>`;
  }

  function renderSplit(hunks, focusLine) {
    let rows = '';
    for (const h of hunks) {
      rows += `<tr class="hunk"><td colspan="4">${esc(h.header)}</td></tr>`;
      const lines = h.lines || [];
      let i = 0;
      while (i < lines.length) {
        const l = lines[i];
        if (l.kind === 'ctx') {
          const focus = focusLine > 0 && l.new_line === focusLine ? ' focus-line' : '';
          rows += `<tr class="${focus}" data-new-line="${l.new_line || 0}">
            <td class="ln">${l.old_line || ''}</td>
            <td class="code-left">${esc(l.text)}</td>
            <td class="ln">${l.new_line || ''}</td>
            <td class="code-right">${esc(l.text)}</td>
          </tr>`;
          i++;
          continue;
        }
        const dels = [];
        const adds = [];
        while (i < lines.length && lines[i].kind === 'del') { dels.push(lines[i++]); }
        while (i < lines.length && lines[i].kind === 'add') { adds.push(lines[i++]); }
        const n = Math.max(dels.length, adds.length);
        for (let k = 0; k < n; k++) {
          const d = dels[k];
          const a = adds[k];
          const focus = focusLine > 0 && ((a && a.new_line === focusLine) || (d && d.old_line === focusLine)) ? ' focus-line' : '';
          rows += `<tr class="${focus}" data-new-line="${a ? a.new_line : 0}">
            <td class="ln ${d ? 'cell-del' : 'cell-empty'}">${d ? d.old_line : ''}</td>
            <td class="code-left ${d ? 'cell-del' : 'cell-empty'}">${d ? esc(d.text) : ''}</td>
            <td class="ln ${a ? 'cell-add' : 'cell-empty'}">${a ? a.new_line : ''}</td>
            <td class="code-right ${a ? 'cell-add' : 'cell-empty'}">${a ? esc(a.text) : ''}</td>
          </tr>`;
        }
      }
    }
    return `<table class="wdiff-split-table"><colgroup><col style="width:48px"><col style="width:calc(50% - 48px)"><col style="width:48px"><col style="width:calc(50% - 48px)"></colgroup><tbody>${rows}</tbody></table>`;
  }

  function renderDiffModal() {
    const d = diffState.data;
    if (!d) return;
    const sym = d.symbol ? `<span class="wdiff-symbol">:: ${esc(d.symbol)}</span>` : '';
    const stat = `<span class="wdiff-stat"><span class="add">+${d.added || 0}</span><span class="del">-${d.deleted || 0}</span></span>`;
    document.getElementById('wdiff-title').innerHTML = `${esc(d.repo)}:${esc(d.file)} ${sym} ${stat}`;
    document.getElementById('wdiff-unified').classList.toggle('active', diffState.mode !== 'split');
    document.getElementById('wdiff-split').classList.toggle('active', diffState.mode === 'split');
    document.getElementById('wdiff-context').classList.toggle('active', diffState.fullContext);
    document.getElementById('wdiff-open-ide').classList.toggle('hidden', !serverCaps.open_ide_enabled);
    document.getElementById('wdiff-meta').textContent =
      `状态: ${d.status || 'modified'} · base: ${(d.base_ref || '').slice(0, 12)} · 路径: ${d.abs_path || ''}`;

    const body = document.getElementById('wdiff-body');
    if (d.binary) {
      body.innerHTML = '<div class="wdiff-empty">二进制文件，无法文本预览 Diff。</div>';
      return;
    }
    const hunks = d.hunks || [];
    if (!hunks.length) {
      body.innerHTML = '<div class="wdiff-empty">当前文件相对 base 无差异或文件不存在。</div>';
      return;
    }
    body.innerHTML = diffState.mode === 'split'
      ? renderSplit(hunks, d.focus_line || 0)
      : renderUnified(hunks, d.focus_line || 0);

    const focusEl = body.querySelector('.focus-line');
    if (focusEl && typeof focusEl.scrollIntoView === 'function') {
      focusEl.scrollIntoView({ block: 'center' });
    }
  }

  /* ---------------- Browser file picker for source.pick ---------------- */

  function pickMarkdownSource() {
    const input = document.createElement('input');
    input.type = 'file';
    input.accept = '.md,.markdown,.txt,text/markdown,text/plain';
    input.onchange = () => {
      const f = input.files && input.files[0];
      if (!f) return;
      const reader = new FileReader();
      reader.onload = () => {
        const title = f.name.replace(/\.(md|markdown|txt)$/i, '');
        void sendAction({ type: 'source.add', title, kind: 'doc', text: String(reader.result || '') });
      };
      reader.readAsText(f);
    };
    input.click();
  }

  /* ---------------- Action dispatch ---------------- */

  async function sendAction(msg) {
    try {
      const resp = await fetch('/api/action', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(msg),
      });
      const res = await resp.json();
      if (!resp.ok || res.ok === false) {
        emitToBoard({ type: 'error', message: res.error || '操作失败' });
      } else {
        if (res.source_id) {
          emitToBoard({ type: 'source.added', id: res.source_id });
        }
        if (res.toast) {
          emitToBoard({ type: 'toast', message: res.toast });
        }
      }
      if (res.snapshot) {
        if (res.snapshot.web) {
          serverCaps = res.snapshot.web;
        }
        res.snapshot.launcher = { configured: false, counts: readLaunchCounts() };
        emitToBoard({ type: 'snapshot', data: res.snapshot });
      } else {
        await fetchSnapshot();
      }
    } catch (e) {
      emitToBoard({ type: 'error', message: '请求失败: ' + (e && e.message ? e.message : e) });
    }
  }

  async function handleLaunch(msg) {
    const action = String(msg.action || 'push_goal');
    const tpl = DEFAULT_PROMPTS[action] || DEFAULT_PROMPTS.push_goal;
    const prompt = tpl
      .replace(/\{nodeId\}/g, msg.nodeId || '')
      .replace(/\{nodeTitle\}/g, msg.nodeTitle || '');
    const ok = await copyClipboard(prompt);
    const counts = bumpLaunchCount(action);
    emitToBoard({ type: 'launcher.stats', data: { configured: false, counts } });
    emitToBoard({
      type: ok ? 'toast' : 'error',
      message: ok ? '已复制并发执行指令到剪贴板，可直接粘贴到任意终端/Agent' : '复制失败，请检查浏览器剪贴板权限',
    });
  }

  window.acquireVsCodeApi = function () {
    return {
      getState() {
        try {
          return JSON.parse(localStorage.getItem(STATE_KEY) || 'null');
        } catch {
          return null;
        }
      },
      setState(s) {
        try {
          localStorage.setItem(STATE_KEY, JSON.stringify(s));
        } catch {
          // ignore
        }
      },
      postMessage(msg) {
        if (!msg || !msg.type) return;
        switch (msg.type) {
          case 'ready':
            void fetchSnapshot();
            startSSE();
            return;
          case 'refresh':
            void fetchSnapshot();
            return;
          case 'open.diff':
            void loadDiff(msg);
            return;
          case 'open.external':
            if (/^https?:\/\//i.test(String(msg.url || ''))) {
              window.open(msg.url, '_blank', 'noopener,noreferrer');
            }
            return;
          case 'open.session':
            emitToBoard({ type: 'toast', message: msg.sessionId ? 'Session: ' + msg.sessionId : '当前没有 Orchestrator session_id' });
            return;
          case 'source.pick':
            pickMarkdownSource();
            return;
          case 'agent.launch':
          case 'copy.goal':
            void handleLaunch(msg.type === 'copy.goal' ? { action: 'push_goal' } : msg);
            return;
          case 'launcher.openSettings':
            emitToBoard({ type: 'toast', message: 'Web 模式下点击「发射 Agent」默认复制指令到剪贴板；可在任意终端/GoLand/VS Code 粘贴执行' });
            return;
          case 'launcher.showStats': {
            const counts = readLaunchCounts();
            const text = Object.keys(counts).length
              ? Object.entries(counts).map(([k, v]) => `${k}: ${v}次`).join(' · ')
              : '暂无使用记录';
            emitToBoard({ type: 'toast', message: '本地使用统计：' + text });
            return;
          }
          default:
            void sendAction(msg);
        }
      },
    };
  };

  /* ---------------- Theme toggle for standalone browser ---------------- */

  function applyTheme(theme) {
    document.documentElement.classList.toggle('theme-light', theme === 'light');
    document.body.classList.toggle('theme-light', theme === 'light');
    const btn = document.getElementById('btn-theme');
    if (btn) {
      btn.textContent = theme === 'light' ? '☾ 暗色' : '☀ 亮色';
    }
  }

  window.addEventListener('DOMContentLoaded', () => {
    const saved = localStorage.getItem(THEME_KEY) || 'dark';
    applyTheme(saved);
    const btn = document.getElementById('btn-theme');
    if (btn) {
      btn.onclick = () => {
        const next = document.documentElement.classList.contains('theme-light') ? 'dark' : 'light';
        localStorage.setItem(THEME_KEY, next);
        applyTheme(next);
      };
    }
  });
})();
