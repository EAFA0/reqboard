(function () {
  'use strict';

  const vscode = acquireVsCodeApi();
  const NODE_W = 230;
  const NODE_H = 120;

  const state = {
    snap: null,
    selectedNode: null,
    selectedAnchor: null,
    view: { scale: 1, tx: 0, ty: 0, initialized: false },
    drag: null,
    pan: null,
    connect: null,
    popup: null,
    bucketOpen: { orphan: true, todelete: true, broken: false },
    dockHidden: false,
    dockFilter: 'all',
    dockType: 'all',
    hoverExternal: null,
    selectedExternal: null,
    redlineOpen: false,
    showExternalCanvas: false,
    anchorLinkedNodes: [],
    anchorBarClosed: false,
    diffSearch: '',
  };
  let justDragged = false;
  let toastTimer = null;
  let prdScroll = 0;

  const $ = (id) => document.getElementById(id);

  function post(msg) {
    vscode.postMessage(msg);
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    }[c]));
  }

  function findNode(id) {
    if (!state.snap || !id) {
      return null;
    }
    return (state.snap.dag.nodes || []).find((n) => n.id === id) || null;
  }

  function findEdge(id) {
    if (!state.snap || !id) {
      return null;
    }
    return (state.snap.dag.edges || []).find((e) => e.id === id) || null;
  }

  function allNodes() {
    return (state.snap && state.snap.dag && state.snap.dag.nodes) || [];
  }

  function allEdges() {
    return (state.snap && state.snap.dag && state.snap.dag.edges) || [];
  }

  function isPrerequisiteNode(n) {
    return !!n && (n.scope === 'external' || n.scope === 'collaborator' || n.scope === 'todo');
  }

  function isTodoNode(n) {
    return !!n && n.scope === 'todo';
  }

  function todoKind(n) {
    const tags = (n && n.tags) || [];
    for (const kind of ['ddl', 'gateway', 'flag']) {
      if (tags.some((t) => String(t).toLowerCase().trim() === kind)) {
        return kind;
      }
    }
    return 'other';
  }

  function todoKindLabel(n) {
    return ({
      ddl: 'DDL',
      gateway: '网关',
      flag: 'Flag',
      other: 'TODO',
    })[todoKind(n)] || 'TODO';
  }

  function isMainNode(n) {
    return !!n && (n.scope === 'mine' || n.status === 'proposed');
  }

  function kindLabel(kind) {
    return ({
      ddl: 'DDL',
      gateway: 'Gateway',
      flag: 'Flag',
      other: 'Other',
    })[kind] || kind || 'Other';
  }

  function nodeOps(n) {
    return (n && Array.isArray(n.ops)) ? n.ops : [];
  }

  function bindingsFor(id) {
    return (state.snap && state.snap.bindings ? state.snap.bindings : []).filter((b) => b.node_id === id);
  }

  function selectValue(el, fallback) {
    if (!el) {
      return fallback;
    }
    const idx = typeof el.selectedIndex === 'number' ? el.selectedIndex : -1;
    if (idx >= 0 && el.options && el.options[idx]) {
      const picked = String(el.options[idx].value || el.options[idx].textContent || '');
      if (picked) {
        return picked;
      }
    }
    if (typeof el.value === 'string' && el.value) {
      return el.value;
    }
    return fallback;
  }

  function isTypingTarget(el) {
    if (!el) {
      return false;
    }
    const tag = String(el.tagName || '').toLowerCase();
    return tag === 'input' || tag === 'textarea' || tag === 'select' || !!el.isContentEditable;
  }

  function blockingIndegree(dag, id) {
    const edges = (dag && dag.edges) || [];
    return edges.filter((e) => e.to === id && (e.kind === 'hard' || e.kind === 'mocked' || e.kind === 'stale')).length;
  }

  // Mirror the CLI layout so the canvas updates instantly; the CLI persists the
  // same coordinates and a later snapshot confirms them. Ordinary nodes keep
  // their natural left-to-right topology depth; only terminal milestone/e2e
  // nodes are pinned to the far-right column.
  function layoutLocal(includeExternal) {
    const nodes = allNodes();
    if (!nodes.length) {
      return;
    }
    const dag = state.snap.dag || {};
    const NODE_GAP_X = 320;
    const NODE_GAP_Y = 180;
    const isSource = (n) => isPrerequisiteNode(n);
    const isRedline = (n) => !!n && n.scope === 'out_of_scope';
    const isMain = (n) => !!n && (n.scope === 'mine' || n.status === 'proposed');
    const blocking = (e) => e.kind === 'hard' || e.kind === 'mocked' || e.kind === 'stale';
    const isMilestone = (n) => !!n && (n.tags || []).some((t) => {
      const v = String(t).toLowerCase().trim();
      return v === 'milestone' || v === 'e2e';
    });

    const computeDepth = (ids) => {
      const set = new Set(ids);
      const depth = {};
      for (let pass = 0; pass < nodes.length; pass++) {
        let changed = false;
        for (const e of (dag.edges || [])) {
          if (!set.has(e.from) || !set.has(e.to) || !blocking(e)) {
            continue;
          }
          const cand = (depth[e.from] || 0) + 1;
          if (cand > (depth[e.to] || 0)) {
            depth[e.to] = cand;
            changed = true;
          }
        }
        if (!changed) {
          break;
        }
      }
      return depth;
    };

    const pinMilestones = (groupNodes, depth) => {
      const outCount = {};
      for (const e of (dag.edges || [])) {
        if (!blocking(e)) {
          continue;
        }
        outCount[e.from] = (outCount[e.from] || 0) + 1;
      }
      let maxFeature = -1;
      for (const n of groupNodes) {
        if (!isMilestone(n)) {
          maxFeature = Math.max(maxFeature, depth[n.id] || 0);
        }
      }
      if (maxFeature < 0) {
        return;
      }
      for (const n of groupNodes) {
        if (!isMilestone(n) || (outCount[n.id] || 0) > 0) {
          continue;
        }
        depth[n.id] = Math.max(depth[n.id] || 0, maxFeature + 1);
      }
    };

    const parkRedlines = (redlines, topY) => {
      redlines.sort((a, b) => String(a.title).localeCompare(String(b.title)));
      const cols = 8;
      for (let i = 0; i < redlines.length; i++) {
        redlines[i].position = {
          x: 80 + (i % cols) * 280,
          y: topY + Math.floor(i / cols) * 150,
        };
      }
      return redlines.length ? Math.ceil(redlines.length / cols) * 150 : 0;
    };

    if (!includeExternal) {
      const main = nodes.filter(isMain);
      const mainSet = new Set(main.map((n) => n.id));
      const depth = computeDepth(main.map((n) => n.id));
      pinMilestones(main, depth);
      const parked = nodes.filter((n) => !mainSet.has(n.id));
      parked.sort((a, b) => {
        if (a.scope !== b.scope) {
          return String(a.scope).localeCompare(String(b.scope));
        }
        return String(a.title).localeCompare(String(b.title));
      });
      const cols = 8;
      const rows = Math.ceil(parked.length / cols);
      for (let i = 0; i < parked.length; i++) {
        parked[i].position = { x: 80 + (i % cols) * 280, y: 60 + Math.floor(i / cols) * 150 };
      }
      const mainTop = rows ? 60 + rows * 150 + 80 : 60;
      const groups = {};
      let maxCount = 1;
      for (const n of main) {
        const d = depth[n.id] || 0;
        (groups[d] = groups[d] || []).push(n);
        maxCount = Math.max(maxCount, groups[d].length);
      }
      for (const key of Object.keys(groups)) {
        const d = Number(key);
        const group = groups[key];
        group.sort((a, b) => {
          const ia = blockingIndegree(dag, a.id);
          const ib = blockingIndegree(dag, b.id);
          if (ia !== ib) {
            return ia - ib;
          }
          return String(a.title).localeCompare(String(b.title));
        });
        const startY = mainTop + (maxCount - group.length) * 90;
        group.forEach((n, i) => {
          n.position = { x: 80 + d * NODE_GAP_X, y: startY + i * NODE_GAP_Y };
        });
      }
      return;
    }

    // Full graph: zero-indegree prerequisites sit in Column 0, while downstream
    // external/collaborator consumers sit to the right of their upstreams.
    const flow = nodes.filter((n) => !isRedline(n));
    const depth = computeDepth(flow.map((n) => n.id));
    for (const n of flow) {
      if (!(n.id in depth)) {
        depth[n.id] = 0;
      }
    }
    pinMilestones(flow, depth);
    const rank = (n) => isSource(n) ? 0 : (n.status === 'proposed' ? 1 : 2);
    const groups = {};
    let maxCount = 1;
    for (const n of flow) {
      const d = depth[n.id] || 0;
      (groups[d] = groups[d] || []).push(n);
      maxCount = Math.max(maxCount, groups[d].length);
    }
    let mainBottom = 0;
    for (const key of Object.keys(groups)) {
      const d = Number(key);
      const group = groups[key];
      group.sort((a, b) => {
        const ra = rank(a);
        const rb = rank(b);
        if (ra !== rb) {
          return ra - rb;
        }
        const ia = blockingIndegree(dag, a.id);
        const ib = blockingIndegree(dag, b.id);
        if (ia !== ib) {
          return ia - ib;
        }
        return String(a.title).localeCompare(String(b.title));
      });
      const startY = 60 + (maxCount - group.length) * 90;
      group.forEach((n, i) => {
        n.position = { x: 80 + d * NODE_GAP_X, y: startY + i * NODE_GAP_Y };
        mainBottom = Math.max(mainBottom, n.position.y + NODE_GAP_Y);
      });
    }
    parkRedlines(nodes.filter(isRedline), mainBottom + 80);
  }

  function dockNodes() {
    return allNodes().filter((n) => isPrerequisiteNode(n) && n.status !== 'deprecated');
  }

  function visibleNodes() {
    return allNodes().filter((n) => state.showExternalCanvas || isMainNode(n));
  }

  function isCanvasNodeId(id) {
    return visibleNodes().some((n) => n.id === id);
  }

  function canvasNode(id) {
    const n = findNode(id);
    return n && isMainNode(n) && isCanvasNodeId(n.id) ? n : null;
  }

  function prerequisiteInEdges(nodeId) {
    return allEdges().filter((e) => {
      if (e.to !== nodeId) {
        return false;
      }
      return isPrerequisiteNode(findNode(e.from));
    });
  }

  function downstreamOfPrerequisite(extId) {
    return allEdges().filter((e) => {
      if (e.from !== extId) {
        return false;
      }
      const to = findNode(e.to);
      return !!to && isMainNode(to);
    });
  }

  function shortLabel(title) {
    const s = String(title || '').replace(/\s+/g, '');
    return s.length > 6 ? s.slice(0, 6) : s;
  }

  function activeExternal() {
    return state.selectedExternal || state.hoverExternal || null;
  }

  function coverageClass(cov) {
    switch (cov) {
      case 'covered': return 'cov-covered';
      case 'ignored': return 'cov-ignored';
      case 'out_of_scope': return 'cov-redline';
      case 'unmapped': return 'cov-unmapped';
      default: return 'cov-heading';
    }
  }

  function scopeLabel(s) {
    return ({
      mine: 'mine',
      collaborator: 'collab',
      external: 'external',
      todo: 'todo',
      out_of_scope: 'out_of_scope',
    })[s] || s || '';
  }

  function stripHashes(text) {
    return String(text || '').replace(/^#+\s*/, '');
  }

  function selEsc(value) {
    return String(value == null ? '' : value).replace(/["\\]/g, '\\$&');
  }

  function scrollIntoViewSafe(el, opts) {
    if (el && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView(opts);
    }
  }

  function clampScale(scale) {
    return Math.max(0.08, Math.min(2.6, Number(scale) || 0));
  }

  function pulseElement(el, cls) {
    if (!el) {
      return;
    }
    const klass = cls || 'locate-pulse';
    el.classList.remove(klass);
    void el.offsetWidth;
    el.classList.add(klass);
    setTimeout(() => el.classList.remove(klass), 1200);
  }

  function inlineMD(text) {
    let s = esc(text);
    s = s.replace(/&lt;br\s*\/?&gt;/gi, '<br>');
    const codes = [];
    s = s.replace(/`([^`]+)`/g, (_m, code) => {
      codes.push(code);
      return '\u0000' + (codes.length - 1) + '\u0000';
    });
    s = s.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    s = s.replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, (_m, label, url) =>
      `<a class="prd-link" href="${url}" title="${url}">${label}</a>`);
    s = s.replace(/\u0000(\d+)\u0000/g, (_m, i) => `<code>${codes[Number(i)]}</code>`);
    return s.replace(/\n/g, '<br>');
  }

  function showToast(message, level) {
    const el = $('toast');
    el.textContent = String(message || '');
    el.className = 'toast ' + (level || 'info');
    el.classList.remove('hidden');
    if (toastTimer) {
      clearTimeout(toastTimer);
    }
    toastTimer = setTimeout(() => {
      el.classList.add('hidden');
    }, level === 'error' ? 9000 : 3500);
  }

  function closePopup() {
    if (state.popup) {
      state.popup.remove();
      state.popup = null;
    }
  }

  function positionPopup(popup, x, y) {
    const w = 340;
    const left = Math.max(8, Math.min(x, window.innerWidth - w - 12));
    popup.style.left = left + 'px';
    popup.style.top = Math.max(8, y) + 'px';
    const h = popup.offsetHeight || 220;
    popup.style.top = Math.max(8, Math.min(y, window.innerHeight - h - 12)) + 'px';
  }

  /* ---------------- top bar ---------------- */

  function renderTop() {
    const snap = state.snap;
    const nodes = snap.dag.nodes || [];
    const done = nodes.filter((n) => n.status === 'done').length;
    const running = nodes.filter((n) => n.status === 'in_progress' || n.status === 'mocked').length;
    const stale = nodes.filter((n) => n.stale).length;
    const orch = snap.orchestrator || {};
    const isRunning = orch.state === 'running';
    $('orch-light').className = 'light ' + (isRunning ? 'on' : 'off');
    $('orch-state').textContent = isRunning ? 'Running' : 'Quiescent';
    $('orch-session').textContent = orch.session_id ? `session ${String(orch.session_id).slice(0, 10)}…` : 'no session';
    let stats = `${done}/${nodes.length} done · ${running} running · ${stale} stale`;
    const dp = snap.dispatch || {};
    if (dp.status === 'QUIESCENT') {
      const b = dp.blocked || {};
      const n = (x) => (x || []).length;
      stats += ` · QUIESCENT (approval ${n(b.waiting_approval)}, review ${n(b.waiting_review)}, external ${n(b.waiting_external)}, todo ${n(b.waiting_todo)}, verify ${n(b.waiting_verification)}, circuit ${n(b.circuit_broken)}, redline ${n(b.out_of_scope)})`;
    } else if (dp.status) {
      stats += ` · ${dp.status} (${(dp.items || []).length} items)`;
    }
    $('top-stats').textContent = stats;
    const undoBtn = $('btn-undo');
    const redoBtn = $('btn-redo');
    const undoDepth = Number(snap.undo_depth || 0);
    const redoDepth = Number(snap.redo_depth || 0);
    if (undoBtn) {
      undoBtn.textContent = `↩ 撤销 (${undoDepth})`;
      undoBtn.disabled = undoDepth <= 0;
    }
    if (redoBtn) {
      redoBtn.textContent = `↪ 重做 (${redoDepth})`;
      redoBtn.disabled = redoDepth <= 0;
    }
    const extBtn = $('btn-ext-canvas');
    if (extBtn) {
      extBtn.classList.toggle('active', state.showExternalCanvas);
      extBtn.title = state.showExternalCanvas
        ? '当前画布含 external / collaborator / todo / out_of_scope；点击恢复只显示我方主干'
        : '当前只显示 mine / proposed 主干；点击显示全图';
    }
  }

  /* ---------------- PRD ---------------- */

  function tableHTML(text) {
    const lines = String(text || '').split('\n').map((s) => s.trim()).filter((s) => s.startsWith('|'));
    const isSep = (l) => /^\|[\s:|-]+\|$/.test(l) && l.indexOf('-') >= 0;
    const head = lines.some(isSep);
    const rows = lines.filter((l) => !isSep(l));
    return rows.map((line, i) => {
      const cells = line.replace(/^\|/, '').replace(/\|$/, '').split('|');
      const cls = 'prd-table-row' + (head && i === 0 ? ' prd-table-head' : '');
      const body = cells.map((c) => `<span class="prd-table-cell">${inlineMD(c.trim())}</span>`).join('');
      return `<div class="${cls}">${body}</div>`;
    }).join('');
  }

  function renderPRD() {
    const list = $('prd-list');
    const blocks = (state.snap.prd && state.snap.prd.blocks) || [];
    const cov = (state.snap.prd && state.snap.prd.coverage) || {};
    $('coverage-badge').textContent = `覆盖 ${Number(cov.percent || 0).toFixed(0)}% · Unmapped ${cov.unmapped || 0}`;
    prdScroll = list.scrollTop;
    const parts = [];
    for (const b of blocks) {
      const cls = ['prd-block', 'kind-' + b.kind, coverageClass(b.coverage)];
      if (b.anchor === state.selectedAnchor) {
        cls.push('selected');
      }
      const nodeIds = b.node_ids || [];
      let inner;
      if (b.kind === 'heading') {
        inner = `<div class="prd-heading lvl${b.level || 1}">${inlineMD(stripHashes(b.text))}</div>`;
      } else if (b.kind === 'table') {
        inner = `<div class="prd-table">${tableHTML(b.text)}</div>`;
      } else if (b.kind === 'code') {
        inner = `<div class="prd-code">${esc(b.text)}</div>`;
      } else {
        inner = `<div>${inlineMD(b.text)}</div>`;
      }
      const actions = `<div class="prd-actions">
        <button data-act="create" title="新建节点并锚定此段">+ 建节点</button>
        <button data-act="ignore" title="标为背景忽略 / 恢复">${b.coverage === 'ignored' ? '恢复' : '忽略'}</button>
      </div>`;
      parts.push(`<div class="${cls.join(' ')}" data-anchor="${esc(b.anchor)}" data-cov="${esc(b.coverage)}" data-nodes="${esc(nodeIds.join(','))}">
        ${inner}${actions}<span class="prd-anchor">@${esc(b.anchor)}</span>
      </div>`);
    }
    list.innerHTML = parts.join('') || '<div class="empty">prd.md 为空；先运行 reqboard prd init。</div>';
    list.scrollTop = prdScroll;
  }

  function highlightPRD() {
    const blocks = document.querySelectorAll('.prd-block');
    for (const el of blocks) {
      const nodes = (el.dataset.nodes || '').split(',').filter(Boolean);
      const on = el.dataset.anchor === state.selectedAnchor ||
        (state.selectedNode && nodes.indexOf(state.selectedNode) >= 0);
      el.classList.toggle('selected', !!on);
    }
  }

  function anchorLinksFor(anchor) {
    const block = document.querySelector(`.prd-block[data-anchor="${selEsc(anchor)}"]`);
    const ids = block ? (block.dataset.nodes || '').split(',').filter(Boolean) : [];
    const links = { all: ids, canvas: [], ext: [], redline: [] };
    for (const id of ids) {
      const n = findNode(id);
      if (!n) {
        continue;
      }
      if (n.scope === 'out_of_scope') {
        links.redline.push(n);
      } else if (isPrerequisiteNode(n)) {
        links.ext.push(n);
      } else if (isMainNode(n)) {
        links.canvas.push(n);
      }
    }
    return links;
  }

  function selectAnchor(anchor, centerDAG) {
    state.selectedAnchor = anchor;
    state.anchorBarClosed = false;
    const block = document.querySelector(`.prd-block[data-anchor="${selEsc(anchor)}"]`);
    const links = anchorLinksFor(anchor);
    state.anchorLinkedNodes = links.all.slice();
    highlightPRD();
    renderAnchorLinks();
    scrollIntoViewSafe(block, { block: 'center', behavior: 'smooth' });
    if (!links.all.length) {
      applyExternalHighlight();
      return;
    }
    if (links.canvas.length) {
      selectNode(links.canvas[0].id, { syncPRD: false, scrollPRD: false, center: false });
      if (centerDAG !== false) {
        focusCanvasNodes(links.canvas);
      }
      if (links.ext.length) {
        ensureDockShows(links.ext);
        for (const n of links.ext) {
          pulseDock(n.id);
        }
      }
      applyExternalHighlight();
      renderAnchorLinks();
      return;
    }
    if (links.ext.length) {
      focusExternalNode(links.ext[0]);
      return;
    }
    focusRedlineNode(links.redline[0]);
  }

  function focusAnchorNode(id) {
    const n = findNode(id);
    if (!n) {
      return;
    }
    if (n.scope === 'out_of_scope') {
      focusRedlineNode(n);
      return;
    }
    if (isPrerequisiteNode(n)) {
      focusExternalNode(n);
      return;
    }
    selectNode(n.id, { syncPRD: false, scrollPRD: false, center: false });
    focusCanvasNodes([n]);
    renderAnchorLinks();
  }

  function focusExternalNode(n) {
    if (!n) {
      return;
    }
    ensureDockShows([n]);
    selectExternal(n.id, { clearNode: true });
    pulseDock(n.id);
    const downstream = downstreamOfPrerequisite(n.id)
      .map((e) => findNode(e.to))
      .filter((x) => x && isMainNode(x));
    if (downstream.length) {
      focusCanvasNodes(downstream);
    }
    applyExternalHighlight();
    renderAnchorLinks();
  }

  function focusRedlineNode(n) {
    if (!n) {
      return;
    }
    selectNode(n.id, { syncPRD: false, scrollPRD: false, center: false });
    state.redlineOpen = true;
    renderRedline();
    applyExternalHighlight();
    const item = document.querySelector(`[data-redline-id="${selEsc(n.id)}"]`);
    if (item) {
      pulseElement(item);
      scrollIntoViewSafe(item, { block: 'center', behavior: 'smooth' });
    }
    renderAnchorLinks();
  }

  function ensureDockShows(nodes) {
    const list = nodes || [];
    state.dockHidden = false;
    const visible = dockNodes();
    let filtered = visible;
    if (state.dockType === 'external') {
      filtered = filtered.filter((n) => !isTodoNode(n));
    } else if (state.dockType === 'todo') {
      filtered = filtered.filter(isTodoNode);
    }
    if (state.dockFilter === 'waiting') {
      filtered = filtered.filter((n) => n.status !== 'done');
    } else if (state.dockFilter === 'ready') {
      filtered = filtered.filter((n) => n.status === 'done');
    }
    const shown = new Set(filtered.map((n) => n.id));
    if (list.some((n) => !shown.has(n.id))) {
      state.dockType = 'all';
      state.dockFilter = 'all';
    }
    renderDock();
  }

  function pulseDock(id) {
    const card = document.querySelector(`.dock-card[data-ext-id="${selEsc(id)}"]`);
    if (!card) {
      return;
    }
    pulseElement(card);
    scrollIntoViewSafe(card, { inline: 'center', block: 'nearest', behavior: 'smooth' });
  }

  function anchorScopeClass(n) {
    if (!n) {
      return 'mine';
    }
    if (n.scope === 'out_of_scope') {
      return 'redline';
    }
    return isPrerequisiteNode(n) ? 'ext' : 'mine';
  }

  function renderAnchorLinks() {
    const el = $('anchor-links');
    if (!el) {
      return;
    }
    const nodes = (state.anchorLinkedNodes || []).map(findNode).filter(Boolean);
    if (!state.selectedAnchor || state.anchorBarClosed || !nodes.length) {
      el.classList.add('hidden');
      el.innerHTML = '';
      return;
    }
    el.classList.remove('hidden');
    const activeId = state.selectedNode || state.selectedExternal;
    const chips = nodes.map((n) => {
      const scope = anchorScopeClass(n);
      const label = scope === 'redline' ? '红线' : isTodoNode(n) ? 'TODO' : scope === 'ext' ? '外部' : '我方';
      const active = n.id === activeId ? ' active' : '';
      return `<button class="anchor-link scope-${scope}${active}" data-anchor-node="${esc(n.id)}" title="${esc(n.title)}"><span class="anchor-link-scope">${label}</span>${esc(n.title)}</button>`;
    }).join('');
    el.innerHTML = `<span class="anchor-links-title">关联节点 ${nodes.length}</span>${chips}<button class="anchor-links-close" data-anchor-links-close title="关闭关联节点条">×</button>`;
  }

  function createNodeFromBlock(anchor, block) {
    const text = (block.innerText || '').split('\n')[0].replace(/^[-*+]\s*/, '').trim();
    const title = text.slice(0, 60) || ('PRD ' + anchor);
    const sel = findNode(state.selectedNode);
    const base = sel && sel.position ? sel.position : { x: 100, y: 100 };
    post({
      type: 'node.create',
      title,
      prdRefs: [anchor],
      x: base.x + 300,
      y: base.y + 80,
    });
    showToast('已创建节点并锚定 ' + anchor, 'info');
  }

  /* ---------------- DAG ---------------- */

  function renderSVG() {
    const edges = allEdges();
    const nodes = visibleNodes();
    const pos = {};
    for (const n of nodes) {
      pos[n.id] = n.position || { x: 0, y: 0 };
    }
    const parts = [];
    for (const e of edges) {
      const from = pos[e.from];
      const to = pos[e.to];
      if (!from || !to) {
        continue;
      }
      const x1 = from.x + NODE_W;
      const y1 = from.y + NODE_H / 2;
      const x2 = to.x;
      const y2 = to.y + NODE_H / 2;
      const dx = Math.max(40, Math.abs(x2 - x1) / 2);
      const d = `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`;
      parts.push(`<path class="edge ${esc(e.kind)}" d="${d}"><title>${esc(`${e.from} → ${e.to} [${e.kind}] ${e.note || ''}`)}</title></path>`);
      parts.push(`<path class="edge-hit" d="${d}" data-edge="${esc(e.id)}"><title>${esc(`${e.from} → ${e.to} [${e.kind}] ${e.note || ''}`)}</title></path>`);
      if (e.kind === 'mocked') {
        const mx = (x1 + x2) / 2;
        const my = (y1 + y2) / 2;
        parts.push(`<g class="mock-pill"><rect x="${mx - 22}" y="${my - 9}" width="44" height="18" rx="9"></rect><text x="${mx}" y="${my + 4}" text-anchor="middle">mock</text></g>`);
      }
    }
    if (state.connect) {
      const c = state.connect;
      const dx = Math.max(30, Math.abs(c.x2 - c.x1) / 2);
      const d = `M ${c.x1} ${c.y1} C ${c.x1 + dx} ${c.y1}, ${c.x2 - dx} ${c.y2}, ${c.x2} ${c.y2}`;
      parts.push(`<path class="edge hard" style="stroke-dasharray:5 4" d="${d}"></path>`);
    }
    $('dag-edges').innerHTML = parts.join('');
  }

  function prerequisiteLabel(n, fallback) {
    if (!n) {
      return shortLabel(fallback);
    }
    return isTodoNode(n) ? todoKindLabel(n) : shortLabel(n.title);
  }

  function extPillHTML(e) {
    const from = findNode(e.from);
    const done = !!from && from.status === 'done';
    const todo = isTodoNode(from);
    let cls = 'hard';
    let prefix = '等:';
    if (done) {
      cls = 'ready';
      prefix = '✓';
    } else if (e.kind === 'mocked') {
      cls = 'mocked';
      prefix = 'mock:';
    } else if (e.kind === 'stale') {
      cls = 'stale';
      prefix = '待:';
    }
    if (todo) {
      cls += ' todo';
    }
    const tip = `${from ? from.title : e.from} · ${done ? '已就绪' : e.kind}${e.note ? ' · ' + e.note : ''}`;
    return `<button class="ext-pill ${cls}" data-ext-edge="${esc(e.id)}" title="${esc(tip)}">${prefix}${esc(prerequisiteLabel(from, e.from))}</button>`;
  }

  function nodeHTML(n) {
    const st = (state.snap.check && state.snap.check.node_stats && state.snap.check.node_stats[n.id]) || {};
    const edges = allEdges();
    const bindings = state.snap.bindings || [];
    const mockDebt = (n.self_mocks || []).filter((m) => !m.resolved).length +
      edges.filter((e) => e.to === n.id && e.kind === 'mocked').length +
      bindings.filter((b) => b.node_id === n.id && b.role === 'mock').length;
    const cls = ['dag-node', 'status-' + n.status];
    if (n.status === 'proposed') { cls.push('proposed'); }
    if (n.status === 'deprecated') { cls.push('deprecated'); }
    if (n.stale) { cls.push('stale'); }
    if (n.id === state.selectedNode) { cls.push('selected'); }
    const p = n.position || { x: 0, y: 0 };
    const badges = [];
    badges.push(`<span class="chip scope-${esc(n.scope)}">${esc(scopeLabel(n.scope))}</span>`);
    badges.push(`<span class="chip ${n.status === 'done' ? 'done' : ''}">${esc(n.status)}</span>`);
    if (n.stale) { badges.push(`<span class="chip stale" title="${esc(n.stale_reason || '')}">stale</span>`); }
    if (mockDebt > 0) { badges.push(`<span class="chip mock" title="Mock 债">mock ${mockDebt}</span>`); }
    if (n.hold) { badges.push('<span class="chip hold">hold</span>'); }
    const extEdges = isMainNode(n) ? prerequisiteInEdges(n.id) : [];
    const extPills = extEdges.length
      ? `<div class="node-ext-pills">${extEdges.map(extPillHTML).join('')}</div>`
      : '';
    const session = n.session_id
      ? `<a class="node-session" data-session="${esc(n.session_id)}" title="打开 Orchestrator session">thread</a>`
      : '';
    const proposed = n.status === 'proposed'
      ? `<div class="node-proposed-actions"><button data-approve="${esc(n.id)}">批准</button><button data-reject="${esc(n.id)}">驳回</button></div>`
      : '';
    const ops = nodeOps(n);
    let opsHTML = '';
    if (ops.length) {
      const chips = ops.slice(0, 4).map((op) => {
        const label = kindLabel(op.kind);
        const tip = `${op.done ? '已完成' : '未完成'} · ${op.title}${op.note ? ' · ' + op.note : ''}`;
        return `<span class="op-chip ${op.done ? 'done' : 'pending'} kind-${esc(op.kind)}" title="${esc(tip)}">${op.done ? '✓' : ''}${esc(label)}</span>`;
      }).join('');
      const more = ops.length > 4 ? `<span class="op-chip more">+${ops.length - 4}</span>` : '';
      opsHTML = `<div class="node-ops">${chips}${more}</div>`;
    }
    return `<div class="${cls.join(' ')}" data-node="${esc(n.id)}" style="left:${p.x}px;top:${p.y}px">
      <div class="node-title" title="${esc(n.title)}">${esc(n.title)}</div>
      <div class="node-badges">${badges.join('')}</div>
      ${extPills}
      <div class="node-meta">
        <span>${esc(scopeLabel(n.scope))}</span>
        <span><span class="diffstat"><span class="add">+${st.added || 0}</span> <span class="del">-${st.deleted || 0}</span></span> ${session}</span>
      </div>
      ${opsHTML}
      ${proposed}
      <div class="node-port" data-port="${esc(n.id)}" title="拖拽到目标节点建边"></div>
    </div>`;
  }

  function applyTransform() {
    $('dag-world').style.transform = `translate(${state.view.tx}px, ${state.view.ty}px) scale(${state.view.scale})`;
  }

  function fitView() {
    const nodes = visibleNodes();
    if (!nodes.length) {
      state.view = { scale: 1, tx: 40, ty: 40, initialized: true };
      applyTransform();
      return;
    }
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const n of nodes) {
      const p = n.position || { x: 0, y: 0 };
      minX = Math.min(minX, p.x);
      minY = Math.min(minY, p.y);
      maxX = Math.max(maxX, p.x + NODE_W);
      maxY = Math.max(maxY, p.y + NODE_H);
    }
    const vp = $('dag-viewport');
    const vw = vp.clientWidth || 600;
    const vh = vp.clientHeight || 500;
    const w = Math.max(200, maxX - minX);
    const h = Math.max(120, maxY - minY);
    const scale = Math.max(0.08, Math.min(1.15, Math.min((vw - 80) / w, (vh - 80) / h)));
    state.view.scale = scale;
    state.view.tx = (vw - w * scale) / 2 - minX * scale;
    state.view.ty = (vh - h * scale) / 2 - minY * scale;
    state.view.initialized = true;
    applyTransform();
  }

  function renderDAG() {
    const nodes = visibleNodes();
    $('dag-nodes').innerHTML = nodes.map(nodeHTML).join('') ||
      '<div class="empty" style="position:absolute;left:20px;top:20px">DAG 为空；双击画布或点 + 节点新建。</div>';
    renderSVG();
    if (!state.view.initialized) {
      fitView();
    } else {
      applyTransform();
    }
    applyExternalHighlight();
  }

  function selectNode(id, opts) {
    const n = findNode(id);
    if (!n) {
      return;
    }
    const o = opts || {};
    state.selectedNode = id;
    state.selectedExternal = null;
    const anchorSet = state.anchorLinkedNodes || [];
    if (anchorSet.length && anchorSet.indexOf(id) < 0) {
      state.anchorLinkedNodes = [];
      state.anchorBarClosed = false;
    }
    if (o.syncPRD !== false && n.prd_refs && n.prd_refs.length) {
      state.selectedAnchor = n.prd_refs[0];
    }
    const cards = document.querySelectorAll('.dag-node');
    for (const el of cards) {
      el.classList.toggle('selected', el.dataset.node === id);
    }
    highlightPRD();
    renderRight();
    applyExternalHighlight();
    renderAnchorLinks();
    if (o.scrollPRD !== false && state.selectedAnchor) {
      const block = document.querySelector(`.prd-block[data-anchor="${selEsc(state.selectedAnchor)}"]`);
      scrollIntoViewSafe(block, { block: 'center', behavior: 'smooth' });
    }
    if (o.center) {
      centerOnNode(n);
    }
  }

  function pulseNode(id) {
    pulseElement(document.querySelector(`.dag-node[data-node="${selEsc(id)}"]`));
  }

  function centerOnRect(minX, minY, maxX, maxY, scale) {
    const vp = $('dag-viewport');
    const vw = vp.clientWidth || 600;
    const vh = vp.clientHeight || 500;
    state.view.scale = clampScale(scale);
    state.view.tx = (vw - (maxX - minX) * state.view.scale) / 2 - minX * state.view.scale;
    state.view.ty = (vh - (maxY - minY) * state.view.scale) / 2 - minY * state.view.scale;
    state.view.initialized = true;
    applyTransform();
  }

  function centerOnNode(n, opts) {
    if (!n || !canvasNode(n.id)) {
      return false;
    }
    const o = opts || {};
    let scale = state.view.scale;
    if (typeof o.scale === 'number') {
      scale = o.scale;
    } else if (o.zoom !== false && scale < 0.8) {
      scale = 0.9;
    }
    const p = n.position || { x: 0, y: 0 };
    centerOnRect(p.x, p.y, p.x + NODE_W, p.y + NODE_H, scale);
    if (o.pulse !== false) {
      pulseNode(n.id);
    }
    return true;
  }

  function focusCanvasNodes(nodes) {
    const list = (nodes || []).filter((n) => n && canvasNode(n.id));
    if (!list.length) {
      return false;
    }
    if (list.length === 1) {
      centerOnNode(list[0], { scale: state.view.scale < 0.8 ? 0.9 : state.view.scale });
      return true;
    }
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const n of list) {
      const p = n.position || { x: 0, y: 0 };
      minX = Math.min(minX, p.x);
      minY = Math.min(minY, p.y);
      maxX = Math.max(maxX, p.x + NODE_W);
      maxY = Math.max(maxY, p.y + NODE_H);
    }
    const vp = $('dag-viewport');
    const vw = vp.clientWidth || 600;
    const vh = vp.clientHeight || 500;
    const w = Math.max(1, maxX - minX);
    const h = Math.max(1, maxY - minY);
    const fit = Math.min((vw - 80) / w, (vh - 80) / h);
    if (fit < 0.45) {
      centerOnNode(list[0], { scale: 0.85 });
      return true;
    }
    centerOnRect(minX, minY, maxX, maxY, Math.max(0.45, Math.min(1.0, fit)));
    for (const n of list) {
      pulseNode(n.id);
    }
    return true;
  }

  function clearSelection() {
    state.selectedNode = null;
    state.selectedAnchor = null;
    state.selectedExternal = null;
    state.hoverExternal = null;
    state.anchorLinkedNodes = [];
    state.anchorBarClosed = false;
    for (const el of document.querySelectorAll('.dag-node')) {
      el.classList.remove('selected', 'anchor-linked', 'ext-linked', 'dimmed');
    }
    highlightPRD();
    applyExternalHighlight();
    renderAnchorLinks();
    renderRight();
  }

  function worldPoint(clientX, clientY) {
    const rect = $('dag-viewport').getBoundingClientRect();
    return {
      x: (clientX - rect.left - state.view.tx) / state.view.scale,
      y: (clientY - rect.top - state.view.ty) / state.view.scale,
    };
  }

  function viewportCenterWorld() {
    const vp = $('dag-viewport');
    const rect = vp.getBoundingClientRect();
    return worldPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
  }

  function createNodeAt(x, y, title, refs) {
    const temp = {
      id: 'tmp-' + Date.now(),
      title,
      scope: 'mine',
      status: 'pending',
      stale: false,
      prd_refs: refs || [],
      self_mocks: [],
      review_notes: [],
      hold: false,
      position: { x, y },
    };
    state.snap.dag.nodes.push(temp);
    renderDAG();
    renderTop();
    post({ type: 'node.create', title, prdRefs: refs || [], x, y });
    showToast('节点已创建（直写 dag.json）', 'info');
  }

  /* ---------------- external dock ---------------- */

  function selectExternal(id, opts) {
    const o = opts || {};
    state.selectedExternal = id;
    state.hoverExternal = null;
    if (o.clearNode) {
      state.selectedNode = null;
      for (const el of document.querySelectorAll('.dag-node')) {
        el.classList.remove('selected');
      }
      renderRight();
    }
    applyExternalHighlight();
    renderDock();
    renderAnchorLinks();
  }

  function renderDock() {
    if (!state.snap) {
      return;
    }
    const all = dockNodes();
    const done = all.filter((n) => n.status === 'done').length;
    $('dock-count').textContent = `前置依赖 ${done}/${all.length}`;
    $('dock-count').title = `外部/协作者 ${all.filter((n) => !isTodoNode(n)).length} · TODO ${all.filter(isTodoNode).length}`;
    $('btn-dock-toggle').textContent = state.dockHidden ? '展开' : '隐藏';
    $('dock-cards').classList.toggle('hidden', state.dockHidden);
    for (const btn of document.querySelectorAll('[data-dock-type]')) {
      btn.classList.toggle('active', btn.dataset.dockType === state.dockType);
    }
    for (const btn of document.querySelectorAll('[data-dock-filter]')) {
      btn.classList.toggle('active', btn.dataset.dockFilter === state.dockFilter);
    }
    const sel = findNode(state.selectedNode);
    const linkedToSelected = new Set();
    if (sel) {
      for (const e of prerequisiteInEdges(sel.id)) {
        linkedToSelected.add(e.from);
      }
    }
    let list = all;
    if (state.dockType === 'external') {
      list = list.filter((n) => !isTodoNode(n));
    } else if (state.dockType === 'todo') {
      list = list.filter(isTodoNode);
    }
    if (state.dockFilter === 'waiting') {
      list = list.filter((n) => n.status !== 'done');
    } else if (state.dockFilter === 'ready') {
      list = list.filter((n) => n.status === 'done');
    }
    const active = activeExternal();
    const cards = list.map((n) => {
      const downstream = downstreamOfPrerequisite(n.id);
      const mocked = downstream.filter((e) => e.kind === 'mocked').length;
      const hard = downstream.filter((e) => e.kind === 'hard').length;
      const isDone = n.status === 'done';
      const linked = linkedToSelected.has(n.id) || active === n.id;
      const todo = isTodoNode(n);
      const typeChip = todo
        ? `<span class="dock-kind kind-${esc(todoKind(n))}" title="TODO 类型">${esc(todoKindLabel(n))}</span>`
        : `<span class="dock-scope scope-${esc(n.scope)}">${esc(scopeLabel(n.scope))}</span>`;
      return `<div class="dock-card ${todo ? 'todo-card' : ''} ${isDone ? 'done' : ''} ${linked ? 'linked' : ''}" data-ext-id="${esc(n.id)}" ${todo ? 'data-todo-id="' + esc(n.id) + '"' : ''} title="${esc(n.title)}">
        <div class="dock-card-top">
          <span class="dock-card-title">${esc(n.title)}</span>
          <span class="dock-impact" title="影响我方下游节点数">→ ${downstream.length}</span>
        </div>
        <div class="dock-card-meta">
          ${typeChip}
          ${n.description ? `<span class="dock-note" title="${esc(n.description)}">${esc(n.description)}</span>` : ''}
          ${mocked ? `<span class="dock-mock" title="mocked 出边">mock ${mocked}</span>` : ''}
          ${!mocked && hard ? `<span class="dock-hard" title="hard 出边">hard ${hard}</span>` : ''}
          <button class="dock-done ${isDone ? 'checked' : ''}" data-ext-done="${esc(n.id)}" title="${isDone ? '点击切回 pending' : '标记 Done'}">✓ Done</button>
        </div>
      </div>`;
    });
    const empty = all.length === 0
      ? '没有前置依赖节点；点「+ 新增 TODO」创建 DDL / 网关 / Flag'
      : state.dockFilter === 'ready' ? '暂无已就绪前置依赖'
        : state.dockFilter === 'waiting' ? '前置依赖已全部就绪'
          : state.dockType === 'todo' ? '暂无 TODO'
            : state.dockType === 'external' ? '暂无外部/协作者依赖'
              : '没有匹配的前置依赖';
    $('dock-cards').innerHTML = cards.join('') || `<div class="empty dock-empty">${empty}</div>`;
    renderRedline();
    applyExternalHighlight();
  }

  function renderRedline() {
    const red = allNodes().filter((n) => n.scope === 'out_of_scope');
    const capsule = $('btn-redline');
    if (capsule) {
      capsule.textContent = `红线 Out-of-Scope (${red.length})`;
    }
    const panel = $('redline-panel');
    if (!panel) {
      return;
    }
    panel.classList.toggle('hidden', !state.redlineOpen);
    if (!state.redlineOpen) {
      panel.innerHTML = '';
      return;
    }
    if (!red.length) {
      panel.innerHTML = '<div class="empty">没有红线节点</div>';
      return;
    }
    panel.innerHTML = `<div class="redline-head"><span>PRD 红线（本期不做，禁止 claim/bind）</span><button class="redline-close" data-redline-close title="关闭">×</button></div>` +
      red.map((n) => `<div class="redline-item" data-redline-id="${esc(n.id)}">
        <span class="redline-title">${esc(n.title)}</span>
        <span class="redline-refs">${(n.prd_refs || []).map((r) => '@' + esc(r)).join(' ') || '无锚点'}</span>
      </div>`).join('');
  }

  function renderExternalFocus() {
    const el = $('external-focus');
    if (!el) {
      return;
    }
    const active = activeExternal();
    const n = active ? findNode(active) : null;
    if (!n) {
      el.classList.add('hidden');
      el.innerHTML = '';
      return;
    }
    const edges = downstreamOfPrerequisite(n.id);
    const edgeHtml = edges.map((e) => {
      const to = findNode(e.to);
      return `<button class="focus-edge ${esc(e.kind)}" data-focus-edge="${esc(e.id)}" title="${esc(e.note || '点击修改边')}">→ ${esc(to ? to.title : e.to)}<span class="kind">${esc(e.kind)}</span>${e.note ? ` · ${esc(e.note)}` : ''}</button>`;
    }).join('') || '<span class="muted">该前置节点没有指向我方节点的出边</span>';
    el.classList.remove('hidden');
    const label = isTodoNode(n) ? `TODO（${todoKindLabel(n)}）：` : '前置：';
    el.innerHTML = `<span class="focus-title">${esc(label)}${esc(n.title)}</span>${edgeHtml}`;
  }

  function applyExternalHighlight() {
    const active = activeExternal();
    const linked = new Set();
    if (active) {
      for (const e of downstreamOfPrerequisite(active)) {
        linked.add(e.to);
      }
    }
    const anchorSet = new Set(state.anchorLinkedNodes || []);
    let anchorOnCanvas = false;
    let anchorExternal = false;
    for (const id of anchorSet) {
      const n = findNode(id);
      if (!n) {
        continue;
      }
      if (isPrerequisiteNode(n)) {
        anchorExternal = true;
        for (const e of downstreamOfPrerequisite(n.id)) {
          linked.add(e.to);
        }
      } else if (isMainNode(n)) {
        anchorOnCanvas = true;
      }
    }
    const focus = !!active || anchorOnCanvas || anchorExternal;
    for (const el of document.querySelectorAll('.dag-node')) {
      const id = el.dataset.node;
      const on = linked.has(id);
      el.classList.toggle('ext-linked', on);
      el.classList.toggle('anchor-linked', anchorSet.has(id));
      el.classList.toggle('dimmed', focus && !on && !anchorSet.has(id) && id !== active);
    }
    const sel = findNode(state.selectedNode);
    const selExt = new Set();
    if (sel) {
      for (const e of prerequisiteInEdges(sel.id)) {
        selExt.add(e.from);
      }
    }
    for (const el of document.querySelectorAll('.dock-card')) {
      const id = el.dataset.extId;
      el.classList.toggle('linked', id === active || selExt.has(id));
      el.classList.toggle('anchor-linked', anchorSet.has(id));
    }
    for (const el of document.querySelectorAll('[data-redline-id]')) {
      el.classList.toggle('anchor-linked', anchorSet.has(el.dataset.redlineId));
    }
    renderExternalFocus();
  }

  function handlePrerequisiteDone(id) {
    const n = findNode(id);
    if (!n) {
      return;
    }
    if (n.status === 'done') {
      post({ type: 'command', args: ['node', 'update', id, '--status', 'pending', '--actor', 'human'] });
      return;
    }
    const mocked = downstreamOfPrerequisite(id).filter((e) => e.kind === 'mocked');
    if (!mocked.length) {
      post({ type: 'done', id });
      return;
    }
    openUnmockConfirm(n, mocked);
  }

  function openUnmockConfirm(n, mocked) {
    closePopup();
    const div = document.createElement('div');
    div.className = 'popup unmock-popup';
    const rows = mocked.map((e) => {
      const to = findNode(e.to);
      return `<li>${esc(e.id)} → ${esc(to ? to.title : e.to)}${e.note ? `：${esc(e.note)}` : ''}</li>`;
    }).join('');
    div.innerHTML = `<button class="close" data-close>×</button>
      <h4>前置依赖已就绪：${esc(n.title)}</h4>
      <div class="unmock-hint">存在 ${mocked.length} 条 mocked 出边。是否同时切回 hard 并标下游 stale？</div>
      <ul class="unmock-list">${rows}</ul>
      <div class="row">
        <button data-only-done>仅标 Done（保留 Mock）</button>
        <button class="primary" data-unmock>Done + 拆 Mock</button>
      </div>`;
    $('popup-layer').appendChild(div);
    positionPopup(div, window.innerWidth / 2 - 170, 140);
    div.querySelector('[data-close]').onclick = closePopup;
    div.querySelector('[data-only-done]').onclick = () => {
      closePopup();
      post({ type: 'done', id: n.id });
    };
    div.querySelector('[data-unmock]').onclick = () => {
      closePopup();
      post({ type: 'done', id: n.id, unmock: true });
    };
    state.popup = div;
  }

  /* ---------------- delete / ops ---------------- */

  function requestDeleteNode(id) {
    const n = findNode(id);
    if (!n) {
      return;
    }
    const owned = bindingsFor(id);
    if (!owned.length) {
      post({ type: 'node.delete', id });
      showToast('节点已删除；Ctrl+Z / Cmd+Z 可一键还原', 'info');
      return;
    }
    openDeleteConfirm(n, owned);
  }

  function openDeleteConfirm(n, bindings) {
    closePopup();
    const div = document.createElement('div');
    div.className = 'popup delete-popup';
    const rows = bindings.slice(0, 6).map((b) => `<li>${esc(b.id)} ${esc(b.repo)}:${esc(b.file)}::${esc(b.symbol)}</li>`).join('');
    div.innerHTML = `<button class="close" data-close>×</button>
      <h4>删除节点：${esc(n.title)}</h4>
      <div class="unmock-hint">该节点仍有 ${bindings.length} 条 bindings。误删会丢失代码归属，请选择处理方式：</div>
      <ul class="unmock-list">${rows}${bindings.length > 6 ? '<li>…</li>' : ''}</ul>
      <div class="row">
        <button data-deprecate>标为废弃（保留 To-Delete）</button>
        <button class="danger" data-force-delete>强制删除（连同 bindings）</button>
      </div>`;
    $('popup-layer').appendChild(div);
    positionPopup(div, window.innerWidth / 2 - 170, 150);
    div.querySelector('[data-close]').onclick = closePopup;
    div.querySelector('[data-deprecate]').onclick = () => {
      closePopup();
      post({ type: 'node.deprecate', id: n.id, reason: '看板删除时改为废弃' });
    };
    div.querySelector('[data-force-delete]').onclick = () => {
      closePopup();
      post({ type: 'node.delete', id: n.id, force: true });
      showToast('节点与 bindings 已强制删除；Ctrl+Z / Cmd+Z 可一键还原', 'info');
    };
    state.popup = div;
  }

  /* ---------------- right panel ---------------- */

  function bucketHTML(key, title, items, renderItem) {
    const open = state.bucketOpen[key] !== false;
    const body = items.length ? items.map(renderItem).join('') : '<div class="empty">无</div>';
    return `<div class="bucket">
      <div class="bucket-head ${key}" data-bucket="${key}"><span>${esc(title)} (${items.length})</span><span>${open ? '▾' : '▸'}</span></div>
      <div class="bucket-body ${open ? '' : 'hidden'}">${body}</div>
    </div>`;
  }

  function diffItemHTML(e) {
    const owner = (e.node_ids || []).length ? `<div class="why">node ${esc((e.node_ids || []).join(', '))}</div>` : '';
    return `<div class="diff-item" data-open-diff data-repo="${esc(e.repo)}" data-file="${esc(e.file)}" data-symbol="${esc(e.symbol)}" data-line="${e.start || 0}" title="${esc(e.correction || '')}">
      <div class="loc">${esc(e.repo)}:${esc(e.file)} :: ${esc(e.symbol)}<span class="stat"><span class="add">+${e.added || 0}</span> <span class="del">-${e.deleted || 0}</span></span></div>
      ${owner}
    </div>`;
  }

  function brokenItemHTML(b) {
    return `<div class="diff-item" data-open-diff data-repo="${esc(b.repo)}" data-file="${esc(b.file)}" data-symbol="${esc(b.symbol)}" data-line="0" title="${esc(b.correction || '')}">
      <div class="loc">${esc(b.repo)}:${esc(b.file)} :: ${esc(b.symbol)}</div>
      <div class="why">${esc(b.reason)}</div>
    </div>`;
  }

  function parseGlobInput(value) {
    return String(value || '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
  }

  function syncFilterControls() {
    const f = (state.snap && state.snap.filter) || {};
    const excludeTests = f.exclude_tests !== false;
    const include = Array.isArray(f.include_globs) ? f.include_globs : [];
    const ignore = Array.isArray(f.ignore_globs) ? f.ignore_globs : [];
    const toggle = $('df-exclude-tests');
    if (toggle) {
      toggle.checked = excludeTests;
    }
    const includeInput = $('df-include');
    if (includeInput && document.activeElement !== includeInput) {
      includeInput.value = include.join(', ');
    }
    const excludeInput = $('df-exclude');
    if (excludeInput && document.activeElement !== excludeInput) {
      excludeInput.value = ignore.join(', ');
    }
    const ignored = (state.snap && state.snap.check && state.snap.check.ignored_files) || [];
    const badge = $('df-ignored');
    if (badge) {
      badge.textContent = ignored.length ? `已过滤 ${ignored.length}` : '';
      badge.title = ignored.length ? ignored.join('\n') : '';
    }
  }

  function applyDiffFilter() {
    post({
      type: 'filter.update',
      excludeTests: !!$('df-exclude-tests').checked,
      includeGlobs: parseGlobInput($('df-include').value),
      ignoreGlobs: parseGlobInput($('df-exclude').value),
    });
  }

  function diffSearchTerm() {
    return String(state.diffSearch || '').trim().toLowerCase();
  }

  function matchesDiffSearch(item) {
    const term = diffSearchTerm();
    if (!term) {
      return true;
    }
    const hay = [
      item.repo || '',
      item.file || '',
      item.symbol || '',
      (item.node_ids || []).join(' '),
    ].join(' ').toLowerCase();
    return hay.indexOf(term) >= 0;
  }

  function opsSectionHTML(n) {
    const ops = nodeOps(n);
    const done = ops.filter((op) => op.done).length;
    let html = `<div class="section-title">Ops 清单 (${done}/${ops.length})</div>`;
    if (!ops.length) {
      html += '<div class="empty">暂无 DDL / 网关配置 / Feature Flag</div>';
    }
    for (const op of ops) {
      html += `<div class="op-row ${op.done ? 'done' : ''}">
        <button class="op-check" data-op-toggle="${esc(op.id)}" data-op-node="${esc(n.id)}" title="${op.done ? '标记未完成' : '标记完成'}">${op.done ? '✓' : '○'}</button>
        <span class="op-kind kind-${esc(op.kind)}">${esc(kindLabel(op.kind))}</span>
        <span class="op-title" title="${esc(op.note || '')}">${esc(op.title)}</span>
        ${op.note ? `<span class="op-note">${esc(op.note)}</span>` : ''}
        <button class="op-remove" data-op-remove="${esc(op.id)}" data-op-node="${esc(n.id)}" title="删除该 Ops">×</button>
      </div>`;
    }
    html += `<div class="ops-add">
      <select id="op-kind-select" title="Ops 类型">
        <option value="ddl">DDL</option>
        <option value="gateway">Gateway</option>
        <option value="flag">Flag</option>
        <option value="other">Other</option>
      </select>
      <input id="op-title-input" placeholder="新增 DDL / 网关配置 / Feature Flag" title="回车或点 + 添加">
      <button data-op-add="${esc(n.id)}" title="新增 Ops">+</button>
    </div>`;
    return html;
  }

  function renderRight() {
    const body = $('diff-body');
    const snap = state.snap;
    const buckets = (snap.check && snap.check.buckets) || {};
    const orphan = (buckets.orphan || []).filter(matchesDiffSearch);
    const todelete = (buckets.to_delete || []).filter(matchesDiffSearch);
    const broken = (buckets.broken_bindings || []).filter(matchesDiffSearch);
    const selected = findNode(state.selectedNode);
    $('sel-node-badge').textContent = selected ? selected.id : '';

    let html = '';
    html += bucketHTML('orphan', 'Orphan Diff', orphan, diffItemHTML);
    html += bucketHTML('todelete', 'To-Delete 残骸', todelete, diffItemHTML);
    html += bucketHTML('broken', 'Broken Binding', broken, brokenItemHTML);

    if (!selected) {
      html += '<div class="empty">在左栏点段落或在 DAG 点节点，查看该功能点的 PRD 与多仓 Diff。</div>';
      body.innerHTML = html;
      return;
    }
    const excludeTests = !state.snap.diff || !state.snap.diff.filter || state.snap.diff.filter.exclude_tests !== false;
    const isTestBinding = (b) => b.role === 'test' || /(?:_test\.go|\.test\.[jt]sx?|\.spec\.[jt]sx?|(?:^|\/)test_[^/]+\.py|_test\.py)$/i.test(String(b.file || ''));
    const allNodeBindings = (snap.bindings || []).filter((b) => b.node_id === selected.id);
    let hiddenTests = 0;
    const bindings = allNodeBindings.filter((b) => {
      if (excludeTests && isTestBinding(b)) {
        hiddenTests++;
        return false;
      }
      return matchesDiffSearch(b);
    });
    const notes = selected.review_notes || [];
    const mocks = selected.self_mocks || [];
    html += `<div class="section-title">功能点</div>
      <div><b>${esc(selected.title)}</b> <span class="badge">${esc(selected.id)}</span></div>
      <div class="prd-refs">${(selected.prd_refs || []).map((r) => `<span class="prd-ref" data-prd-ref="${esc(r)}">@${esc(r)}</span>`).join('') || '<span class="muted">无 PRD 锚点</span>'}</div>`;
    if (selected.description) {
      html += `<div class="why">${esc(selected.description)}</div>`;
    }
    const hiddenHint = hiddenTests ? `，已隐藏 ${hiddenTests} 个单测` : '';
    html += `<div class="section-title">挂载符号 (${bindings.length}${hiddenHint})</div>`;
    if (!bindings.length) {
      html += '<div class="empty">尚未 bind；本节点范围内的改动会进 Orphan Diff。</div>';
    }
    for (const b of bindings) {
      html += `<div class="binding" data-open-diff data-repo="${esc(b.repo)}" data-file="${esc(b.file)}" data-symbol="${esc(b.symbol)}" data-line="0">
        <div class="loc">${esc(b.repo)}:${esc(b.file)} :: ${esc(b.symbol)}<span class="role-chip role-${esc(b.role)}">${esc(b.role)}</span></div>
        <div class="why">${esc(b.why || '')}</div>
      </div>`;
    }
    if (mocks.length) {
      html += '<div class="section-title">Mock 债</div>';
      for (const m of mocks) {
        html += `<div class="review-note ${m.resolved ? 'resolved' : ''}">${esc(m.id)} · ${m.resolved ? '已销账' : '未销账'}：${esc(m.desc)}</div>`;
      }
    }
    if (notes.length) {
      html += '<div class="section-title">Review 批注</div>';
      for (const r of notes) {
        html += `<div class="review-note ${r.status === 'resolved' ? 'resolved' : ''}">${esc(r.id)} · ${esc(r.status)}：${esc(r.comment)}</div>`;
      }
    }
    html += opsSectionHTML(selected);
    html += `<div class="review-actions">
      <button class="primary" data-review-pass="${esc(selected.id)}">Reviewed 通过</button>
      <button data-review-reject="${esc(selected.id)}">打回 + 写批注</button>
      <button class="danger" data-done="${esc(selected.id)}">标记 Done</button>
      <button class="danger" data-delete-node="${esc(selected.id)}" title="删除节点（有 bindings 时需确认）">删除</button>
    </div>
    <div class="reject-box" id="reject-box">
      <textarea id="reject-note" placeholder="写明具体批注，Agent 下一轮会按批注修复"></textarea>
      <button class="primary" data-review-submit="${esc(selected.id)}">提交打回</button>
    </div>`;
    body.innerHTML = html;
  }

  /* ---------------- popups ---------------- */

  function mainCanvasChoices() {
    return allNodes()
      .filter((n) => isMainNode(n) && n.status !== 'deprecated')
      .sort((a, b) => String(a.title).localeCompare(String(b.title)));
  }

  function openTodoAdd(rect) {
    closePopup();
    const div = document.createElement('div');
    div.className = 'popup todo-add-popup';
    const choices = mainCanvasChoices();
    const blocks = choices.map((n) => `<label class="todo-block-row">
        <input type="checkbox" value="${esc(n.id)}">
        <span>${esc(n.title)}</span><span class="muted">${esc(n.id)}</span>
      </label>`).join('') || '<div class="empty">暂无可关联的我方节点；留空即独立需求级 TODO</div>';
    div.innerHTML = `<button class="close" data-close>×</button>
      <h4>新增前置 TODO</h4>
      <label>标题</label><input id="todo-title" placeholder="如 DDL: orders add confirmation_mode">
      <label>类型</label><select id="todo-kind">
        <option value="ddl">DDL</option>
        <option value="gateway">网关</option>
        <option value="flag">Flag</option>
        <option value="other">其他</option>
      </select>
      <label>备注（可选）</label><input id="todo-note" placeholder="如 上线前执行 / 网关白名单">
      <label>阻塞的下游我方节点（可留空 = 独立需求级 TODO）</label>
      <div class="todo-blocks">${blocks}</div>
      <div class="row"><button data-cancel>取消</button><button class="primary" data-create>创建</button></div>`;
    $('popup-layer').appendChild(div);
    positionPopup(div, rect ? rect.left : window.innerWidth / 2 - 170, rect ? rect.bottom + 6 : 140);
    const submit = () => {
      const titleEl = div.querySelector('#todo-title');
      const title = titleEl ? titleEl.value.trim() : '';
      if (!title) {
        showToast('请填写 TODO 标题', 'error');
        if (titleEl) { titleEl.focus(); }
        return;
      }
      const selected = [...div.querySelectorAll('.todo-block-row input')]
        .filter((el) => el.checked)
        .map((el) => el.value);
      const noteEl = div.querySelector('#todo-note');
      post({
        type: 'todo.add',
        title,
        kind: selectValue(div.querySelector('#todo-kind'), 'other'),
        note: noteEl ? noteEl.value.trim() : '',
        blocks: selected,
      });
      closePopup();
      showToast('TODO 已创建' + (selected.length ? `，并连向 ${selected.length} 个下游节点` : ''), 'info');
    };
    div.querySelector('[data-close]').onclick = closePopup;
    div.querySelector('[data-cancel]').onclick = closePopup;
    div.querySelector('[data-create]').onclick = submit;
    for (const id of ['todo-title', 'todo-note']) {
      div.querySelector('#' + id).addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          submit();
        }
      });
    }
    state.popup = div;
  }

  function openTodoEditor(n, rect) {
    closePopup();
    const div = document.createElement('div');
    div.className = 'popup todo-edit-popup';
    const kind = todoKind(n);
    const blockStart = (n.tags || []).some((t) => String(t).toLowerCase().trim() === 'block-start');
    div.innerHTML = `<button class="close" data-close>×</button>
      <h4>编辑 TODO <span class="badge">${esc(n.id)}</span></h4>
      <label>标题</label><input id="te-title" value="${esc(n.title)}">
      <label>类型</label><select id="te-kind">
        <option value="ddl" ${kind === 'ddl' ? 'selected' : ''}>DDL</option>
        <option value="gateway" ${kind === 'gateway' ? 'selected' : ''}>网关</option>
        <option value="flag" ${kind === 'flag' ? 'selected' : ''}>Flag</option>
        <option value="other" ${kind === 'other' ? 'selected' : ''}>其他</option>
      </select>
      <label>备注</label><input id="te-note" value="${esc(n.description || '')}">
      <label class="checkline"><input type="checkbox" id="te-block-start" ${blockStart ? 'checked' : ''}> block-start（未完成时阻塞下游开工）</label>
      <div class="todo-edit-hint">点击卡片下方关联边胶囊可编辑 / 删除具体阻塞连线。</div>
      <div class="row"><button class="primary" data-save>保存</button><button class="danger" data-delete>删除</button></div>`;
    $('popup-layer').appendChild(div);
    positionPopup(div, rect ? rect.left : 220, rect ? rect.bottom + 6 : 150);
    div.querySelector('[data-close]').onclick = closePopup;
    div.querySelector('[data-save]').onclick = () => {
      const title = div.querySelector('#te-title').value.trim();
      if (!title) {
        showToast('TODO 标题不能为空', 'error');
        return;
      }
      const tags = [selectValue(div.querySelector('#te-kind'), 'other')];
      for (const t of (n.tags || [])) {
        const v = String(t).toLowerCase().trim();
        if (v === 'ddl' || v === 'gateway' || v === 'flag' || v === 'other' || v === 'block-start') {
          continue;
        }
        tags.push(t);
      }
      if (div.querySelector('#te-block-start').checked) {
        tags.push('block-start');
      }
      post({
        type: 'node.update',
        id: n.id,
        patch: {
          title,
          description: div.querySelector('#te-note').value.trim(),
          tags,
        },
      });
      closePopup();
    };
    let confirmDelete = false;
    div.querySelector('[data-delete]').onclick = (e) => {
      if (!confirmDelete) {
        confirmDelete = true;
        e.target.textContent = '确认删除?';
        return;
      }
      closePopup();
      requestDeleteNode(n.id);
    };
    state.popup = div;
  }

  function openNodeEditor(n, rect) {
    closePopup();
    const div = document.createElement('div');
    div.className = 'popup';
    const scopes = ['mine', 'collaborator', 'external', 'out_of_scope'];
    const statuses = ['pending', 'in_progress', 'mocked', 'implemented', 'changes_requested', 'reviewed'];
    if (statuses.indexOf(n.status) < 0) {
      statuses.unshift(n.status);
    }
    div.innerHTML = `<button class="close" data-close>×</button>
      <h4>${esc(n.title)} <span class="badge">${esc(n.id)}</span></h4>
      <label>标题</label><input id="ne-title" value="${esc(n.title)}">
      <label>描述</label><textarea id="ne-desc">${esc(n.description || '')}</textarea>
      <label>scope</label><select id="ne-scope">${scopes.map((s) => `<option value="${s}" ${n.scope === s ? 'selected' : ''}>${s}</option>`).join('')}</select>
      <label>status</label><select id="ne-status">${statuses.map((s) => `<option value="${s}" ${n.status === s ? 'selected' : ''}>${s}</option>`).join('')}</select>
      <label>tags（逗号分隔）</label><input id="ne-tags" value="${esc((n.tags || []).join(', '))}">
      <label>PRD refs（逗号分隔）</label><input id="ne-refs" value="${esc((n.prd_refs || []).join(', '))}">
      <label class="checkline"><input type="checkbox" id="ne-hold" ${n.hold ? 'checked' : ''}> hold 暂停自动推进</label>
      <label>Ops 清单（DDL / 网关 / Flag）</label>
      <div class="editor-ops">${nodeOps(n).map((op) => `<div class="op-row ${op.done ? 'done' : ''}">
        <button class="op-check" data-editor-op-toggle="${esc(op.id)}" title="${op.done ? '标记未完成' : '标记完成'}">${op.done ? '✓' : '○'}</button>
        <span class="op-kind kind-${esc(op.kind)}">${esc(kindLabel(op.kind))}</span>
        <span class="op-title">${esc(op.title)}</span>
        <button class="op-remove" data-editor-op-remove="${esc(op.id)}" title="删除">×</button>
      </div>`).join('') || '<div class="empty">暂无 Ops</div>'}</div>
      <div class="ops-add">
        <select id="ne-op-kind"><option value="ddl">DDL</option><option value="gateway">Gateway</option><option value="flag">Flag</option><option value="other">Other</option></select>
        <input id="ne-op-title" placeholder="新增 Ops 标题">
        <button data-editor-op-add>+</button>
      </div>
      <div class="row"><button class="primary" data-save>保存</button><button class="danger" data-deprecate>设 deprecated</button><button class="danger" data-delete>删除</button></div>`;
    $('popup-layer').appendChild(div);
    positionPopup(div, rect ? rect.right + 8 : 200, rect ? rect.top : 120);

    const val = (id) => div.querySelector('#' + id).value.trim();
    const checked = (id) => div.querySelector('#' + id).checked;
    const split = (id) => val(id).split(',').map((s) => s.trim()).filter(Boolean);
    const initial = { scope: n.scope, status: n.status };
    const patch = () => {
      const p = {
        title: val('ne-title'),
        description: val('ne-desc'),
        hold: checked('ne-hold'),
        tags: split('ne-tags'),
        prd_refs: split('ne-refs'),
      };
      if (val('ne-scope') !== initial.scope) { p.scope = val('ne-scope'); }
      if (val('ne-status') !== initial.status) { p.status = val('ne-status'); }
      return p;
    };
    let blurTimer = null;
    const schedule = () => {
      if (blurTimer) { clearTimeout(blurTimer); }
      blurTimer = setTimeout(() => post({ type: 'node.update', id: n.id, patch: patch() }), 400);
    };
    div.querySelector('[data-save]').onclick = () => {
      if (blurTimer) { clearTimeout(blurTimer); }
      post({ type: 'node.update', id: n.id, patch: patch() });
      closePopup();
    };
    div.querySelector('[data-close]').onclick = closePopup;
    div.querySelector('#ne-scope').onchange = () => post({ type: 'node.update', id: n.id, patch: { scope: val('ne-scope') } });
    div.querySelector('#ne-status').onchange = () => post({ type: 'node.update', id: n.id, patch: { status: val('ne-status') } });
    div.querySelector('#ne-hold').onchange = () => post({ type: 'node.update', id: n.id, patch: { hold: checked('ne-hold') } });
    for (const id of ['ne-title', 'ne-desc', 'ne-tags', 'ne-refs']) {
      div.querySelector('#' + id).addEventListener('blur', schedule);
    }
    const dep = div.querySelector('[data-deprecate]');
    dep.onclick = () => {
      if (dep.dataset.confirm !== '1') {
        dep.dataset.confirm = '1';
        dep.textContent = '确认 deprecated?';
        return;
      }
      post({ type: 'node.deprecate', id: n.id, reason: '看板操作' });
      closePopup();
    };
    div.querySelector('[data-delete]').onclick = () => {
      closePopup();
      requestDeleteNode(n.id);
    };
    for (const btn of div.querySelectorAll('[data-editor-op-toggle]')) {
      btn.onclick = () => {
        post({ type: 'ops.toggle', nodeId: n.id, opId: btn.dataset.editorOpToggle });
        closePopup();
      };
    }
    for (const btn of div.querySelectorAll('[data-editor-op-remove]')) {
      btn.onclick = () => {
        post({ type: 'ops.remove', nodeId: n.id, opId: btn.dataset.editorOpRemove });
        closePopup();
      };
    }
    div.querySelector('[data-editor-op-add]').onclick = () => {
      const titleEl = div.querySelector('#ne-op-title');
      const title = titleEl ? titleEl.value.trim() : '';
      if (!title) {
        showToast('请填写 Ops 标题', 'error');
        return;
      }
      post({
        type: 'ops.add',
        nodeId: n.id,
        kind: selectValue(div.querySelector('#ne-op-kind'), 'other'),
        title,
      });
      closePopup();
    };
    state.popup = div;
  }

  function openEdgeEditor(edge, x, y) {
    closePopup();
    const div = document.createElement('div');
    div.className = 'popup';
    const kinds = ['hard', 'mocked', 'stale', 'discovered'];
    div.innerHTML = `<button class="close" data-close>×</button>
      <h4>边 <span class="badge">${esc(edge.id)}</span></h4>
      <label>类型</label><select id="ee-kind">${kinds.map((k) => `<option value="${k}" ${edge.kind === k ? 'selected' : ''}>${k}</option>`).join('')}</select>
      <label>备注</label><textarea id="ee-note">${esc(edge.note || '')}</textarea>
      <div class="row"><button class="primary" data-save>保存</button><button class="danger" data-del>删除边</button></div>`;
    $('popup-layer').appendChild(div);
    positionPopup(div, x, y);
    div.querySelector('[data-close]').onclick = closePopup;
    div.querySelector('[data-save]').onclick = () => {
      post({
        type: 'edge.update',
        id: edge.id,
        kind: div.querySelector('#ee-kind').value,
        note: div.querySelector('#ee-note').value,
      });
      closePopup();
    };
    div.querySelector('[data-del]').onclick = () => {
      post({ type: 'edge.remove', id: edge.id });
      closePopup();
    };
    state.popup = div;
  }

  /* ---------------- events ---------------- */

  $('prd-list').addEventListener('click', (e) => {
    const link = e.target.closest('a.prd-link');
    if (link) {
      if (e.preventDefault) {
        e.preventDefault();
      }
      e.stopPropagation();
      post({ type: 'open.external', url: link.getAttribute('href') });
      return;
    }
    const block = e.target.closest('.prd-block');
    if (!block) {
      return;
    }
    const btn = e.target.closest('button[data-act]');
    if (btn) {
      e.stopPropagation();
      if (btn.dataset.act === 'create') {
        createNodeFromBlock(block.dataset.anchor, block);
      } else {
        post({
          type: 'prd.ignore',
          anchor: block.dataset.anchor,
          undo: block.dataset.cov === 'ignored',
        });
      }
      return;
    }
    selectAnchor(block.dataset.anchor, true);
  });

  $('anchor-links').addEventListener('click', (e) => {
    if (e.target.closest('[data-anchor-links-close]')) {
      state.anchorBarClosed = true;
      renderAnchorLinks();
      return;
    }
    const chip = e.target.closest('[data-anchor-node]');
    if (chip) {
      focusAnchorNode(chip.dataset.anchorNode);
    }
  });

  $('dag-viewport').addEventListener('mousedown', (e) => {
    if (e.target.closest('.dag-node') || e.target.closest('[data-edge]') || e.button !== 0) {
      return;
    }
    state.pan = { sx: e.clientX, sy: e.clientY, tx: state.view.tx, ty: state.view.ty, moved: false };
    $('dag-viewport').classList.add('panning');
  });

  $('dag-viewport').addEventListener('wheel', (e) => {
    e.preventDefault();
    const rect = $('dag-viewport').getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const my = e.clientY - rect.top;
    const factor = e.deltaY < 0 ? 1.12 : 0.89;
    const old = state.view.scale;
    const next = Math.max(0.08, Math.min(2.6, old * factor));
    state.view.tx = mx - (mx - state.view.tx) * (next / old);
    state.view.ty = my - (my - state.view.ty) * (next / old);
    state.view.scale = next;
    applyTransform();
  }, { passive: false });

  $('dag-viewport').addEventListener('dblclick', (e) => {
    if (e.target.closest('.dag-node')) {
      return;
    }
    const p = worldPoint(e.clientX, e.clientY);
    createNodeAt(p.x - NODE_W / 2, p.y - NODE_H / 2, '新功能点', []);
  });

  $('dag-nodes').addEventListener('mousedown', (e) => {
    const port = e.target.closest('.node-port');
    if (port) {
      const n = findNode(port.dataset.port);
      if (n) {
        const p = n.position || { x: 0, y: 0 };
        state.connect = { from: n.id, x1: p.x + NODE_W, y1: p.y + NODE_H / 2, x2: p.x + NODE_W, y2: p.y + NODE_H / 2 };
        renderSVG();
      }
      e.preventDefault();
      e.stopPropagation();
      return;
    }
    const nodeEl = e.target.closest('.dag-node');
    if (!nodeEl || e.target.closest('button') || e.target.closest('a') || e.button !== 0) {
      return;
    }
    const n = findNode(nodeEl.dataset.node);
    if (!n) {
      return;
    }
    const p = n.position || { x: 0, y: 0 };
    state.drag = { id: n.id, el: nodeEl, sx: e.clientX, sy: e.clientY, ox: p.x, oy: p.y, moved: false };
    e.preventDefault();
    e.stopPropagation();
  });

  $('dag-nodes').addEventListener('click', (e) => {
    const pill = e.target.closest('[data-ext-edge]');
    if (pill) {
      e.stopPropagation();
      const edge = findEdge(pill.dataset.extEdge);
      if (edge) {
        openEdgeEditor(edge, e.clientX + 6, e.clientY + 6);
      }
      return;
    }
    const approve = e.target.closest('[data-approve]');
    if (approve) {
      post({ type: 'node.approve', id: approve.dataset.approve });
      return;
    }
    const reject = e.target.closest('[data-reject]');
    if (reject) {
      post({ type: 'node.reject', id: reject.dataset.reject, reason: '看板驳回' });
      return;
    }
    const session = e.target.closest('[data-session]');
    if (session) {
      post({ type: 'open.session', sessionId: session.dataset.session });
      return;
    }
    if (justDragged) {
      return;
    }
    const nodeEl = e.target.closest('.dag-node');
    if (!nodeEl) {
      return;
    }
    const n = findNode(nodeEl.dataset.node);
    if (!n) {
      return;
    }
    selectNode(n.id, { center: false });
    openNodeEditor(n, nodeEl.getBoundingClientRect());
  });

  $('dag-edges').addEventListener('click', (e) => {
    const hit = e.target.closest('[data-edge]');
    if (!hit) {
      return;
    }
    const edge = findEdge(hit.dataset.edge);
    if (edge) {
      openEdgeEditor(edge, e.clientX + 6, e.clientY + 6);
    }
  });

  window.addEventListener('mousemove', (e) => {
    if (state.drag) {
      const dx = (e.clientX - state.drag.sx) / state.view.scale;
      const dy = (e.clientY - state.drag.sy) / state.view.scale;
      const n = findNode(state.drag.id);
      if (n) {
        n.position = { x: state.drag.ox + dx, y: state.drag.oy + dy };
        state.drag.el.style.left = n.position.x + 'px';
        state.drag.el.style.top = n.position.y + 'px';
      }
      if (Math.abs(dx) > 2 || Math.abs(dy) > 2) {
        state.drag.moved = true;
      }
      renderSVG();
      return;
    }
    if (state.pan) {
      const dx = e.clientX - state.pan.sx;
      const dy = e.clientY - state.pan.sy;
      state.view.tx = state.pan.tx + dx;
      state.view.ty = state.pan.ty + dy;
      if (Math.abs(dx) > 2 || Math.abs(dy) > 2) {
        state.pan.moved = true;
      }
      applyTransform();
      return;
    }
    if (state.connect) {
      const p = worldPoint(e.clientX, e.clientY);
      state.connect.x2 = p.x;
      state.connect.y2 = p.y;
      renderSVG();
    }
  });

  window.addEventListener('mouseup', (e) => {
    if (state.drag) {
      const d = state.drag;
      state.drag = null;
      if (d.moved) {
        justDragged = true;
        setTimeout(() => { justDragged = false; }, 60);
        const n = findNode(d.id);
        if (n && n.position) {
          post({ type: 'node.update', id: d.id, patch: { x: n.position.x, y: n.position.y } });
        }
      }
    }
    if (state.pan) {
      const moved = state.pan.moved;
      state.pan = null;
      $('dag-viewport').classList.remove('panning');
      if (!moved) {
        clearSelection();
      }
    }
    if (state.connect) {
      const c = state.connect;
      state.connect = null;
      const el = document.elementFromPoint(e.clientX, e.clientY);
      const target = el && el.closest ? el.closest('.dag-node') : null;
      if (target && target.dataset.node && target.dataset.node !== c.from) {
        post({ type: 'edge.add', from: c.from, to: target.dataset.node, kind: 'hard' });
      } else {
        renderSVG();
      }
    }
  });

  /* ---------------- diff filter bar ---------------- */

  $('df-exclude-tests').addEventListener('change', (e) => {
    post({ type: 'filter.update', excludeTests: !!e.target.checked });
  });

  $('df-apply').addEventListener('click', applyDiffFilter);
  $('df-reset').addEventListener('click', () => {
    post({ type: 'filter.update', reset: true });
  });

  for (const id of ['df-include', 'df-exclude']) {
    $(id).addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        applyDiffFilter();
      }
    });
  }

  $('df-search').addEventListener('input', (e) => {
    state.diffSearch = e.target.value || '';
    renderRight();
  });

  $('diff-body').addEventListener('click', (e) => {
    const opToggle = e.target.closest('[data-op-toggle]');
    if (opToggle) {
      post({ type: 'ops.toggle', nodeId: opToggle.dataset.opNode, opId: opToggle.dataset.opToggle });
      return;
    }
    const opRemove = e.target.closest('[data-op-remove]');
    if (opRemove) {
      post({ type: 'ops.remove', nodeId: opRemove.dataset.opNode, opId: opRemove.dataset.opRemove });
      return;
    }
    const opAdd = e.target.closest('[data-op-add]');
    if (opAdd) {
      const kindEl = $('op-kind-select');
      const titleEl = $('op-title-input');
      const title = titleEl ? titleEl.value.trim() : '';
      if (!title) {
        showToast('请填写 Ops 标题', 'error');
        return;
      }
      post({ type: 'ops.add', nodeId: opAdd.dataset.opAdd, kind: selectValue(kindEl, 'other'), title });
      return;
    }
    const del = e.target.closest('[data-delete-node]');
    if (del) {
      requestDeleteNode(del.dataset.deleteNode);
      return;
    }
    const head = e.target.closest('[data-bucket]');
    if (head) {
      const key = head.dataset.bucket;
      state.bucketOpen[key] = state.bucketOpen[key] === false;
      renderRight();
      return;
    }
    const open = e.target.closest('[data-open-diff]');
    if (open) {
      post({
        type: 'open.diff',
        repo: open.dataset.repo,
        file: open.dataset.file,
        symbol: open.dataset.symbol,
        line: Number(open.dataset.line || 0),
      });
      return;
    }
    const ref = e.target.closest('[data-prd-ref]');
    if (ref) {
      selectAnchor(ref.dataset.prdRef, true);
      return;
    }
    const pass = e.target.closest('[data-review-pass]');
    if (pass) {
      post({ type: 'node.review', id: pass.dataset.reviewPass, pass: true });
      return;
    }
    const reject = e.target.closest('[data-review-reject]');
    if (reject) {
      const box = $('reject-box');
      if (box) {
        box.classList.add('open');
        const ta = $('reject-note');
        if (ta) { ta.focus(); }
      }
      return;
    }
    const submit = e.target.closest('[data-review-submit]');
    if (submit) {
      const ta = $('reject-note');
      const note = ta ? ta.value.trim() : '';
      if (!note) {
        showToast('请填写具体批注再提交打回', 'error');
        return;
      }
      post({ type: 'node.review', id: submit.dataset.reviewSubmit, pass: false, note });
      return;
    }
    const done = e.target.closest('[data-done]');
    if (done) {
      post({ type: 'done', id: done.dataset.done });
    }
  });

  /* ---------------- external dock events ---------------- */

  $('dock-cards').addEventListener('click', (e) => {
    const doneBtn = e.target.closest('[data-ext-done]');
    if (doneBtn) {
      e.stopPropagation();
      handlePrerequisiteDone(doneBtn.dataset.extDone);
      return;
    }
    const card = e.target.closest('.dock-card');
    if (!card) {
      return;
    }
    const id = card.dataset.extId;
    const n = findNode(id);
    if (isTodoNode(n)) {
      const rect = card.getBoundingClientRect();
      selectExternal(id);
      openTodoEditor(n, rect);
      return;
    }
    selectExternal(state.selectedExternal === id ? null : id);
  });

  $('dock-cards').addEventListener('mouseover', (e) => {
    const card = e.target.closest('.dock-card');
    if (!card) {
      return;
    }
    const id = card.dataset.extId;
    if (state.hoverExternal === id) {
      return;
    }
    state.hoverExternal = id;
    applyExternalHighlight();
  });

  $('dock-cards').addEventListener('mouseout', (e) => {
    const card = e.target.closest('.dock-card');
    if (!card) {
      return;
    }
    if (e.relatedTarget && card.contains(e.relatedTarget)) {
      return;
    }
    if (state.hoverExternal === card.dataset.extId) {
      state.hoverExternal = null;
      applyExternalHighlight();
    }
  });

  $('btn-dock-toggle').onclick = () => {
    state.dockHidden = !state.dockHidden;
    renderDock();
  };

  for (const btn of document.querySelectorAll('[data-dock-filter]')) {
    btn.onclick = () => {
      state.dockFilter = btn.dataset.dockFilter;
      renderDock();
    };
  }

  for (const btn of document.querySelectorAll('[data-dock-type]')) {
    btn.onclick = () => {
      state.dockType = btn.dataset.dockType;
      renderDock();
    };
  }

  const addTodoBtn = $('btn-add-todo');
  if (addTodoBtn) {
    addTodoBtn.onclick = () => openTodoAdd(addTodoBtn.getBoundingClientRect());
  }

  $('btn-redline').onclick = () => {
    state.redlineOpen = !state.redlineOpen;
    renderRedline();
  };

  $('redline-panel').addEventListener('click', (e) => {
    if (e.target.closest('[data-redline-close]')) {
      state.redlineOpen = false;
      renderRedline();
      return;
    }
    const item = e.target.closest('[data-redline-id]');
    if (!item) {
      return;
    }
    const n = findNode(item.dataset.redlineId);
    if (!n) {
      return;
    }
    selectNode(n.id, { center: false });
    if (n.prd_refs && n.prd_refs.length) {
      selectAnchor(n.prd_refs[0], false);
    }
  });

  $('external-focus').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-focus-edge]');
    if (!btn) {
      return;
    }
    const edge = findEdge(btn.dataset.focusEdge);
    if (edge) {
      openEdgeEditor(edge, e.clientX + 6, e.clientY + 6);
    }
  });

  $('btn-undo').onclick = () => {
    if (Number((state.snap && state.snap.undo_depth) || 0) <= 0) {
      showToast('没有可撤销的操作', 'info');
      return;
    }
    post({ type: 'undo' });
  };
  $('btn-redo').onclick = () => {
    if (Number((state.snap && state.snap.redo_depth) || 0) <= 0) {
      showToast('没有可重做的操作', 'info');
      return;
    }
    post({ type: 'redo' });
  };

  $('btn-ext-canvas').onclick = () => {
    state.showExternalCanvas = !state.showExternalCanvas;
    layoutLocal(state.showExternalCanvas);
    renderTop();
    renderDAG();
    fitView();
    post({ type: 'layout', includeExternal: state.showExternalCanvas });
  };

  $('btn-refresh').onclick = () => post({ type: 'refresh' });
  $('btn-fit').onclick = () => fitView();
  $('btn-layout').onclick = () => {
    state.view.initialized = false;
    post({ type: 'layout', includeExternal: state.showExternalCanvas });
  };
  $('btn-goal').onclick = () => post({ type: 'copy.goal' });
  $('btn-orch').onclick = () => post({
    type: 'open.session',
    sessionId: state.snap && state.snap.orchestrator ? state.snap.orchestrator.session_id : '',
  });
  $('btn-add-node').onclick = () => {
    const p = viewportCenterWorld();
    createNodeAt(p.x - NODE_W / 2, p.y - NODE_H / 2, '新功能点', []);
  };

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      if (state.popup) {
        closePopup();
      } else {
        clearSelection();
      }
      return;
    }
    const typing = isTypingTarget(e.target);
    if (!typing && (e.ctrlKey || e.metaKey) && !e.altKey) {
      const key = String(e.key || '').toLowerCase();
      if (key === 'z' && !e.shiftKey) {
        e.preventDefault();
        post({ type: 'undo' });
        return;
      }
      if ((key === 'z' && e.shiftKey) || key === 'y') {
        e.preventDefault();
        post({ type: 'redo' });
        return;
      }
    }
    if (!typing && !state.popup && (e.key === 'Delete' || e.key === 'Backspace') && state.selectedNode) {
      e.preventDefault();
      requestDeleteNode(state.selectedNode);
    }
  });

  window.addEventListener('resize', () => {
    if (state.snap) {
      applyTransform();
    }
  });

  window.addEventListener('message', (event) => {
    const msg = event.data;
    if (!msg || !msg.type) {
      return;
    }
    if (msg.type === 'snapshot') {
      state.snap = msg.data;
      if (state.selectedNode && !findNode(state.selectedNode)) {
        state.selectedNode = null;
      }
      if (state.selectedExternal && !findNode(state.selectedExternal)) {
        state.selectedExternal = null;
      }
      if (state.hoverExternal && !findNode(state.hoverExternal)) {
        state.hoverExternal = null;
      }
      state.anchorLinkedNodes = (state.anchorLinkedNodes || []).filter((id) => !!findNode(id));
      render();
    } else if (msg.type === 'toast') {
      showToast(msg.message, 'info');
    } else if (msg.type === 'error') {
      showToast(msg.message, 'error');
    }
  });

  function render() {
    if (!state.snap) {
      return;
    }
    syncFilterControls();
    renderTop();
    renderPRD();
    renderDAG();
    renderDock();
    renderAnchorLinks();
    renderRight();
  }

  post({ type: 'ready' });
})();
