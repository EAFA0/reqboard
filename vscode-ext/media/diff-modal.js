/* Built-in Diff Modal inside VS Code Webview.
 * Uses VS Code's live LSP (via extension host) for Cmd+Click Go-to-Definition
 * and Shift+Cmd+Click Find References, keeping the user continuously inside Diff view.
 */
(function () {
  'use strict';

  const MODE_KEY = 'reqboard.vscode.diffMode';
  const FS_KEY = 'reqboard.vscode.diffFullscreen';

  const st = {
    open: false,
    req: null,
    data: null,
    mode: readPref(MODE_KEY, 'split'),
    fullscreen: readPref(FS_KEY, '0') === '1',
    fullContext: false,
    backStack: [],
    forwardStack: [],
    reqSeq: 0,
    pendingDiffReqId: 0,
    pendingRestoreScroll: undefined,
    pendingSym: null,
  };

  function readPref(k, def) {
    try {
      const v = window.localStorage && window.localStorage.getItem(k);
      return v == null ? def : v;
    } catch {
      return def;
    }
  }

  function writePref(k, v) {
    try {
      if (window.localStorage) {
        window.localStorage.setItem(k, v);
      }
    } catch {
      // ignore storage errors in restricted webviews
    }
  }

  function postHost(msg) {
    if (typeof window.__reqboardPost === 'function') {
      window.__reqboardPost(msg);
    }
  }

  function toast(message, level) {
    window.dispatchEvent(new MessageEvent('message', {
      data: { type: level === 'error' ? 'error' : 'toast', message },
    }));
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    }[c]));
  }

  function renderCodeHTML(text) {
    const s = String(text == null ? '' : text);
    const re = /[A-Za-z_][A-Za-z0-9_]*/g;
    let out = '';
    let last = 0;
    let m;
    while ((m = re.exec(s)) !== null) {
      if (m.index > last) {
        out += esc(s.slice(last, m.index));
      }
      out += `<span class="tok" data-col="${m.index + 1}">${esc(m[0])}</span>`;
      last = m.index + m[0].length;
    }
    if (last < s.length) {
      out += esc(s.slice(last));
    }
    return out;
  }

  function ensureModal() {
    if (document.getElementById('rb-diff-backdrop')) {
      return;
    }
    const backdrop = document.createElement('div');
    backdrop.id = 'rb-diff-backdrop';
    backdrop.className = 'hidden';
    backdrop.innerHTML = `<div id="rb-diff-modal" role="dialog" aria-modal="true">
      <div class="wdiff-head">
        <div class="wdiff-title" id="rb-wdiff-title">Code Diff</div>
        <div class="wdiff-actions">
          <button id="rb-wdiff-back" title="返回上一个 Diff（Alt+←）" disabled>← 返回</button>
          <button id="rb-wdiff-forward" title="前进（Alt+→）" disabled>→</button>
          <button id="rb-wdiff-prev-hunk" title="上一个变更块">↑ 变更</button>
          <button id="rb-wdiff-next-hunk" title="下一个变更块">↓ 变更</button>
          <button id="rb-wdiff-unified" title="Unified 单栏视图">Unified</button>
          <button id="rb-wdiff-split" title="Side-by-Side 双栏对比">Split</button>
          <button id="rb-wdiff-context" title="切换仅看变更片段 / 完整文件上下文">完整上下文</button>
          <button id="rb-wdiff-fullscreen" title="切换全屏 / 窗口模式">⛶ 全屏</button>
          <button id="rb-wdiff-copy-loc" title="复制 file:line">复制 file:line</button>
          <button id="rb-wdiff-open-ide" title="在 VS Code 真实编辑器中打开当前行">在编辑器打开</button>
          <button id="rb-wdiff-close" title="关闭（Esc）">✕</button>
        </div>
      </div>
      <div class="wdiff-body" id="rb-wdiff-body"></div>
      <div class="wdiff-foot">
        <span id="rb-wdiff-meta"></span>
        <span>Cmd/Ctrl+点击 跳定义 · Shift+Cmd/Ctrl+点击 查引用（复用 VS Code LSP）· Alt+← 返回 · Esc 关闭</span>
      </div>
    </div>
    <div id="rb-diff-candidates" class="hidden"></div>`;
    document.body.appendChild(backdrop);

    backdrop.addEventListener('click', (e) => {
      if (e.target === backdrop) {
        closeModal();
      }
    });
    document.getElementById('rb-wdiff-close').onclick = closeModal;
    document.getElementById('rb-wdiff-back').onclick = () => navigateHistory(-1);
    document.getElementById('rb-wdiff-forward').onclick = () => navigateHistory(1);
    let hunkIdx = -1;
    const jumpHunk = (delta) => {
      const hunks = [...document.querySelectorAll('#rb-wdiff-body tr.hunk')];
      if (!hunks.length) return;
      hunkIdx = (hunkIdx + delta + hunks.length) % hunks.length;
      if (typeof hunks[hunkIdx].scrollIntoView === 'function') {
        hunks[hunkIdx].scrollIntoView({ block: 'start', behavior: 'smooth' });
      }
    };
    document.getElementById('rb-wdiff-prev-hunk').onclick = () => jumpHunk(-1);
    document.getElementById('rb-wdiff-next-hunk').onclick = () => jumpHunk(1);
    document.getElementById('rb-wdiff-unified').onclick = () => {
      st.mode = 'unified';
      writePref(MODE_KEY, 'unified');
      renderModal();
    };
    document.getElementById('rb-wdiff-split').onclick = () => {
      st.mode = 'split';
      writePref(MODE_KEY, 'split');
      renderModal();
    };
    document.getElementById('rb-wdiff-context').onclick = () => {
      st.fullContext = !st.fullContext;
      if (st.req) {
        requestDiff(st.req, { pushHistory: false });
      }
    };
    document.getElementById('rb-wdiff-fullscreen').onclick = () => {
      st.fullscreen = !st.fullscreen;
      writePref(FS_KEY, st.fullscreen ? '1' : '0');
      applyFullscreen();
    };
    document.getElementById('rb-wdiff-copy-loc').onclick = () => {
      const d = st.data;
      if (!d) return;
      const loc = d.focus_line > 0 ? `${d.file}:${d.focus_line}` : d.file;
      postHost({ type: 'clipboard.write', text: loc, toast: '已复制 ' + loc });
    };
    document.getElementById('rb-wdiff-open-ide').onclick = () => {
      const d = st.data;
      if (!d || !d.abs_path) return;
      postHost({ type: 'diff.openInEditor', absPath: d.abs_path, line: d.focus_line || 1 });
    };

    const body = document.getElementById('rb-wdiff-body');
    body.addEventListener('mousedown', (e) => {
      if (e.metaKey || e.ctrlKey) {
        e.preventDefault();
      }
    });
    body.addEventListener('click', (e) => {
      hideCandidates();
      const ln = e.target.closest('td.ln');
      if (ln) {
        const lineNo = Number(ln.textContent.trim() || 0);
        if (!lineNo || !st.data) return;
        const loc = `${st.data.file}:${lineNo}`;
        postHost({ type: 'clipboard.write', text: loc, toast: '已复制 ' + loc });
        return;
      }
      if (e.metaKey || e.ctrlKey) {
        const tok = e.target.closest('.tok');
        const tr = e.target.closest('tr');
        if (!tok || !tr || !st.data) return;
        e.preventDefault();
        const newLine = Number(tr.dataset.newLine || 0);
        if (!newLine) {
          toast('删除行没有工作区行号，请在保留或新增行上 Cmd/Ctrl+点击');
          return;
        }
        const word = tok.textContent || '';
        const col = Number(tok.dataset.col || 1);
        const reqId = ++st.reqSeq;
        st.pendingSym = { reqId, word, findRefs: !!e.shiftKey, x: e.clientX, y: e.clientY };
        postHost({
          type: 'diff.symbol',
          reqId,
          repo: st.data.repo,
          file: st.data.file,
          absPath: st.data.abs_path,
          line: newLine,
          col,
          word,
          references: !!e.shiftKey,
        });
      }
    });

    const setMod = (on) => {
      const m = document.getElementById('rb-diff-modal');
      if (m) m.classList.toggle('mod-key', !!on);
    };
    document.addEventListener('keydown', (e) => {
      if (!st.open) return;
      if (e.key === 'Escape') {
        e.stopPropagation();
        closeModal();
        return;
      }
      if (e.key === 'Meta' || e.key === 'Control') setMod(true);
      if (e.altKey && e.key === 'ArrowLeft') {
        e.preventDefault();
        navigateHistory(-1);
      } else if (e.altKey && e.key === 'ArrowRight') {
        e.preventDefault();
        navigateHistory(1);
      }
    }, true);
    document.addEventListener('keyup', (e) => {
      if (e.key === 'Meta' || e.key === 'Control') setMod(false);
    });
    window.addEventListener('blur', () => setMod(false));
  }

  function applyFullscreen() {
    const backdrop = document.getElementById('rb-diff-backdrop');
    if (backdrop) backdrop.classList.toggle('fullscreen', !!st.fullscreen);
    const btn = document.getElementById('rb-wdiff-fullscreen');
    if (btn) {
      btn.classList.toggle('active', !!st.fullscreen);
      btn.textContent = st.fullscreen ? '⛶ 退出全屏' : '⛶ 全屏';
    }
  }

  function hideCandidates() {
    const box = document.getElementById('rb-diff-candidates');
    if (box) {
      box.classList.add('hidden');
      box.innerHTML = '';
    }
  }

  function closeModal() {
    hideCandidates();
    st.open = false;
    st.data = null;
    st.backStack = [];
    st.forwardStack = [];
    const el = document.getElementById('rb-diff-backdrop');
    if (el) el.classList.add('hidden');
  }

  function updateNavButtons() {
    const back = document.getElementById('rb-wdiff-back');
    const fwd = document.getElementById('rb-wdiff-forward');
    if (back) {
      back.disabled = st.backStack.length === 0;
      back.textContent = st.backStack.length ? `← 返回 (${st.backStack.length})` : '← 返回';
    }
    if (fwd) fwd.disabled = st.forwardStack.length === 0;
  }

  function navigateHistory(dir) {
    hideCandidates();
    const body = document.getElementById('rb-wdiff-body');
    const curScroll = body ? body.scrollTop : 0;
    if (dir < 0 && st.backStack.length > 0) {
      if (st.req) st.forwardStack.push({ req: st.req, fullContext: st.fullContext, scrollTop: curScroll });
      const prev = st.backStack.pop();
      st.fullContext = !!prev.fullContext;
      requestDiff(prev.req, { pushHistory: false, restoreScroll: prev.scrollTop });
    } else if (dir > 0 && st.forwardStack.length > 0) {
      if (st.req) st.backStack.push({ req: st.req, fullContext: st.fullContext, scrollTop: curScroll });
      const next = st.forwardStack.pop();
      st.fullContext = !!next.fullContext;
      requestDiff(next.req, { pushHistory: false, restoreScroll: next.scrollTop });
    }
  }

  function requestDiff(req, opts) {
    const o = opts || {};
    ensureModal();
    hideCandidates();
    const wasOpen = st.open && !!st.data;
    if (o.pushHistory && st.open && st.req) {
      const body = document.getElementById('rb-wdiff-body');
      st.backStack.push({ req: st.req, fullContext: st.fullContext, scrollTop: body ? body.scrollTop : 0 });
      st.forwardStack = [];
    } else if (!o.pushHistory && !st.open) {
      st.backStack = [];
      st.forwardStack = [];
    }
    st.open = true;
    st.req = req;
    st.pendingRestoreScroll = o.restoreScroll;
    updateNavButtons();
    const backdrop = document.getElementById('rb-diff-backdrop');
    backdrop.classList.remove('hidden');
    applyFullscreen();
    if (!wasOpen) {
      document.getElementById('rb-wdiff-body').innerHTML = '<div class="wdiff-empty">正在读取 git diff…</div>';
    }
    const reqId = ++st.reqSeq;
    st.pendingDiffReqId = reqId;
    const wantFull = st.fullContext || (o.forceFullContextIfClean && req.line > 0);
    postHost({
      type: 'diff.fetch',
      reqId,
      repo: req.repo || '',
      file: req.file || '',
      symbol: req.symbol || '',
      line: Number(req.line || 0),
      context: wantFull ? 99999 : 5,
    });
  }

  function showCandidates(locs, source, x, y, label) {
    const box = document.getElementById('rb-diff-candidates');
    if (!box) return;
    const rows = locs.map((loc, idx) => {
      const fullPath = loc.repo === '__external__' ? loc.file : `${loc.repo}:${loc.file}`;
      const base = String(loc.file || '').split('/').pop() || loc.file;
      return `<button class="wdiff-cand-item" data-cand-idx="${idx}" title="${esc(fullPath)}:${loc.line}">
        <span class="wdiff-cand-loc"><span class="wdiff-cand-file">${esc(base)}:${loc.line}</span><span class="wdiff-cand-path">${esc(fullPath)}</span></span>
        <span class="wdiff-cand-preview">${esc(loc.preview || '')}</span>
      </button>`;
    }).join('');
    box.innerHTML = `<div class="wdiff-cand-head"><span>找到 ${locs.length} 处${esc(label)}（${esc(source)}）</span><button data-cand-close style="padding:0 5px">×</button></div>${rows}`;
    box.classList.remove('hidden');
    const left = Math.max(12, Math.min((x || 120) + 8, window.innerWidth - 580));
    const top = Math.max(12, Math.min((y || 120) + 10, window.innerHeight - 330));
    box.style.left = left + 'px';
    box.style.top = top + 'px';
    box.onclick = (ev) => {
      if (ev.target.closest('[data-cand-close]')) {
        hideCandidates();
        return;
      }
      const btn = ev.target.closest('[data-cand-idx]');
      if (!btn) return;
      const loc = locs[Number(btn.dataset.candIdx)];
      hideCandidates();
      if (loc) {
        requestDiff(
          { repo: loc.repo, file: loc.file, symbol: loc.word || '', line: loc.line || 1 },
          { pushHistory: true, forceFullContextIfClean: true }
        );
      }
    };
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
        rows += `<tr class="${cls.join(' ')}" data-new-line="${l.new_line || 0}">
          <td class="ln">${l.old_line > 0 ? l.old_line : ''}</td>
          <td class="ln">${l.new_line > 0 ? l.new_line : ''}</td>
          <td class="sig">${sig}</td>
          <td class="code">${renderCodeHTML(l.text)}</td>
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
            <td class="code-left">${renderCodeHTML(l.text)}</td>
            <td class="ln">${l.new_line || ''}</td>
            <td class="code-right">${renderCodeHTML(l.text)}</td>
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
            <td class="code-left ${d ? 'cell-del' : 'cell-empty'}">${d ? renderCodeHTML(d.text) : ''}</td>
            <td class="ln ${a ? 'cell-add' : 'cell-empty'}">${a ? a.new_line : ''}</td>
            <td class="code-right ${a ? 'cell-add' : 'cell-empty'}">${a ? renderCodeHTML(a.text) : ''}</td>
          </tr>`;
        }
      }
    }
    return `<table class="wdiff-split-table"><colgroup><col style="width:48px"><col style="width:calc(50% - 48px)"><col style="width:48px"><col style="width:calc(50% - 48px)"></colgroup><tbody>${rows}</tbody></table>`;
  }

  function renderModal(restoreScroll) {
    const d = st.data;
    if (!d) return;
    updateNavButtons();
    const sym = d.symbol ? `<span class="wdiff-symbol">:: ${esc(d.symbol)}</span>` : '';
    const stat = `<span class="wdiff-stat" title="整个文件的改动行数">文件 <span class="add">+${d.added || 0}</span><span class="del">-${d.deleted || 0}</span></span>`;
    const fileLabel = d.repo === '__external__' ? d.file : `${d.repo}:${d.file}`;
    document.getElementById('rb-wdiff-title').innerHTML = `${esc(fileLabel)} ${stat} ${sym}`;
    document.getElementById('rb-wdiff-unified').classList.toggle('active', st.mode !== 'split');
    document.getElementById('rb-wdiff-split').classList.toggle('active', st.mode === 'split');
    document.getElementById('rb-wdiff-context').classList.toggle('active', st.fullContext);
    document.getElementById('rb-wdiff-meta').textContent =
      `状态: ${d.status || 'modified'} · base: ${(d.base_ref || '').slice(0, 12)} · 路径: ${d.abs_path || ''}`;

    const body = document.getElementById('rb-wdiff-body');
    if (d.binary) {
      body.innerHTML = '<div class="wdiff-empty">二进制文件，无法文本预览 Diff。</div>';
      return;
    }
    const hunks = d.hunks || [];
    if (!hunks.length) {
      body.innerHTML = '<div class="wdiff-empty">当前文件相对 base 无差异或文件不存在。</div>';
      return;
    }
    body.innerHTML = st.mode === 'split'
      ? renderSplit(hunks, d.focus_line || 0)
      : renderUnified(hunks, d.focus_line || 0);

    if (typeof restoreScroll === 'number') {
      body.scrollTop = restoreScroll;
      return;
    }
    const focusEl = body.querySelector('.focus-line');
    if (focusEl && typeof focusEl.scrollIntoView === 'function') {
      focusEl.scrollIntoView({ block: 'center' });
    }
  }

  window.addEventListener('message', (event) => {
    const msg = event.data;
    if (!msg || !msg.type) return;
    if (msg.type === 'diff.open') {
      requestDiff(msg.req || {}, { pushHistory: false });
      return;
    }
    if (msg.type === 'diff.data') {
      if (msg.reqId && msg.reqId !== st.pendingDiffReqId) return;
      if (msg.error) {
        const body = document.getElementById('rb-wdiff-body');
        if (body) body.innerHTML = `<div class="wdiff-empty">${esc(msg.error)}</div>`;
        return;
      }
      st.data = msg.data;
      const scroll = st.pendingRestoreScroll;
      st.pendingRestoreScroll = undefined;
      renderModal(scroll);
      return;
    }
    if (msg.type === 'diff.symbols') {
      const p = st.pendingSym;
      if (!p || (msg.reqId && msg.reqId !== p.reqId)) return;
      st.pendingSym = null;
      const label = p.findRefs ? '引用' : '定义';
      if (msg.error) {
        toast(msg.error, 'error');
        return;
      }
      const locs = msg.locations || [];
      if (!locs.length) {
        toast(`未找到符号 "${p.word}" 的${label}`);
        return;
      }
      if (!p.findRefs && locs.length === 1) {
        requestDiff(
          { repo: locs[0].repo, file: locs[0].file, symbol: p.word, line: locs[0].line || 1 },
          { pushHistory: true, forceFullContextIfClean: true }
        );
        return;
      }
      showCandidates(locs.map((l) => ({ ...l, word: p.word })), msg.source || 'vscode-lsp', p.x, p.y, label);
    }
  });
})();
