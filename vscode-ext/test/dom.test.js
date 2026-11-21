const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { parseHTML } = require('linkedom');

function test(name, fn) {
  try {
    fn();
    console.log('ok - ' + name);
  } catch (e) {
    console.error('not ok - ' + name);
    throw e;
  }
}

const BODY = `<!DOCTYPE html><html><body>
<header id="topbar">
  <span id="orch-light" class="light"></span>
  <span id="orch-state"></span>
  <span id="orch-session"></span>
  <div id="top-stats"></div>
  <button id="btn-ext-canvas"></button>
  <button id="btn-refresh"></button>
  <button id="btn-fit"></button>
  <button id="btn-layout"></button>
  <button id="btn-goal"></button>
  <button id="btn-orch"></button>
</header>
<div id="prd-list"></div>
<div id="coverage-badge"></div>
<div id="dag-pane">
  <button id="btn-add-node"></button>
  <div id="external-dock">
    <button id="btn-dock-toggle"></button>
    <span id="dock-count"></span>
    <button data-dock-filter="all" class="active"></button>
    <button data-dock-filter="waiting"></button>
    <button data-dock-filter="ready"></button>
    <button id="btn-redline"></button>
    <div id="dock-cards"></div>
    <div id="redline-panel" class="hidden"></div>
  </div>
  <div id="external-focus" class="hidden"></div>
  <div id="anchor-links" class="anchor-links hidden"></div>
  <div id="dag-viewport">
    <div id="dag-world">
      <svg id="dag-edges"></svg>
      <div id="dag-nodes"></div>
    </div>
    <div id="dag-hint"></div>
  </div>
</div>
<div id="diff-filter">
  <input type="checkbox" id="df-exclude-tests" checked>
  <input id="df-include">
  <input id="df-exclude">
  <button id="df-apply"></button>
  <button id="df-reset"></button>
  <input id="df-search">
  <span id="df-ignored"></span>
</div>
<div id="diff-body"></div>
<div id="sel-node-badge"></div>
<div id="popup-layer"></div>
<div id="toast" class="toast hidden"></div>
</body></html>`;

function node(id, title, scope, status, x, y) {
  return {
    id, title, scope, status, stale: false, prd_refs: [], tags: [],
    self_mocks: [], review_notes: [], hold: false, attempts: 0,
    position: { x, y },
  };
}

const snapshot = {
  config: { repos: [] },
  prd: {
    blocks: [
      {
        anchor: 'p-main',
        kind: 'paragraph',
        text: '下单流程：**提交** 与 `code` 与 [文档](https://example.com/doc)',
        coverage: 'covered',
        node_ids: ['rb-a'],
      },
      {
        anchor: 'p-ext',
        kind: 'paragraph',
        text: '依赖 CRM 接口',
        coverage: 'covered',
        node_ids: ['rb-ext'],
      },
      {
        anchor: 'p-multi',
        kind: 'paragraph',
        text: '多节点段落',
        coverage: 'covered',
        node_ids: ['rb-a', 'rb-b', 'rb-ext'],
      },
      {
        anchor: 'p-red',
        kind: 'paragraph',
        text: '退订本期不做',
        coverage: 'out_of_scope',
        node_ids: ['rb-red'],
      },
      {
        anchor: 'p-table',
        kind: 'table',
        text: '| 名称 | 说明 |\n|---|---|\n| **粗体** | `code` |',
        coverage: 'covered',
        node_ids: [],
      },
    ],
    coverage: { percent: 100, unmapped: 0 },
  },
  bindings: [],
  leases: [],
  filter: { exclude_tests: true, include_globs: [], ignore_globs: ['**/*_mock.go', '**/*_gen.go'] },
  check: { node_stats: {}, buckets: { orphan: [], to_delete: [], broken_bindings: [] }, ignored_files: ['order_api:handler/order_query_mock.go'] },
  dispatch: { status: 'READY', items: [], blocked: {} },
  orchestrator: { state: 'running', session_id: 's-1' },
  events: [],
  dag: {
    nodes: [
      node('rb-a', '下单', 'mine', 'pending', 80, 440),
      node('rb-b', '待批节点', 'mine', 'proposed', 400, 440),
      node('rb-ext', 'CRM 接口', 'external', 'pending', 80, 60),
      node('rb-ext2', '商品侧', 'collaborator', 'done', 360, 60),
      node('rb-ext3', '合同', 'external', 'pending', 640, 60),
      node('rb-red', '退订（本期不做）', 'out_of_scope', 'pending', 920, 60),
    ],
    edges: [
      { id: 'ed-1', from: 'rb-ext', to: 'rb-a', kind: 'mocked', note: 'CRM 未确认' },
      { id: 'ed-2', from: 'rb-ext2', to: 'rb-b', kind: 'hard', note: '' },
      { id: 'ed-3', from: 'rb-ext3', to: 'rb-a', kind: 'hard', note: '' },
      { id: 'ed-4', from: 'rb-a', to: 'rb-b', kind: 'hard', note: '' },
    ],
  },
};

function boot(data) {
  const { document, window } = parseHTML(BODY);
  const messages = [];
  const listeners = {};
  const fakeWindow = {
    addEventListener(name, fn) {
      (listeners[name] = listeners[name] || []).push(fn);
    },
    innerWidth: 1400,
    innerHeight: 900,
  };
  const context = {
    document,
    window: fakeWindow,
    acquireVsCodeApi: () => ({ postMessage: (m) => messages.push(m) }),
    setTimeout,
    clearTimeout,
    console,
  };
  vm.createContext(context);
  const code = fs.readFileSync(path.join(__dirname, '..', 'media', 'board.js'), 'utf8');
  vm.runInContext(code, context);
  for (const fn of listeners.message || []) {
    fn({ data: { type: 'snapshot', data: data || snapshot } });
  }
  return { document, window, messages };
}

function click(window, el) {
  el.dispatchEvent(new window.Event('click', { bubbles: true }));
}

function parseTransform(el) {
  const m = /translate\(([-\d.]+)px,\s*([-\d.]+)px\)\s*scale\(([-\d.]+)\)/.exec(el.style.transform || '');
  assert.ok(m, 'unexpected transform: ' + el.style.transform);
  return { tx: Number(m[1]), ty: Number(m[2]), scale: Number(m[3]) };
}

function viewportCenter(view) {
  return {
    x: (300 - view.tx) / view.scale,
    y: (250 - view.ty) / view.scale,
  };
}

function wideSnapshot() {
  const snap = JSON.parse(JSON.stringify(snapshot));
  snap.dag.nodes.push(node('rb-far', '远端节点', 'mine', 'pending', 4183, 440));
  snap.prd.blocks = [{
    anchor: 'p-wide',
    kind: 'paragraph',
    text: '远端主画布段落',
    coverage: 'covered',
    node_ids: ['rb-a'],
  }];
  return snap;
}

test('external dock aggregates all external/collaborator nodes', () => {
  const { document } = boot();
  assert.strictEqual(document.getElementById('dock-count').textContent, '外部依赖 1/3');
  const cards = [...document.querySelectorAll('.dock-card')];
  assert.strictEqual(cards.length, 3);
  const crm = cards.find((c) => c.dataset.extId === 'rb-ext');
  assert.ok(crm, 'CRM card missing');
  assert.ok(crm.textContent.includes('→ 1'), 'downstream impact missing: ' + crm.textContent);
  assert.ok(crm.textContent.includes('mock 1'), 'mock badge missing: ' + crm.textContent);
});

test('main canvas renders only mine/proposed and keeps external pills', () => {
  const { document, window } = boot();
  const ids = [...document.querySelectorAll('.dag-node')].map((el) => el.dataset.node).sort();
  assert.deepStrictEqual(ids, ['rb-a', 'rb-b']);
  const mine = document.querySelector('.dag-node[data-node="rb-a"]');
  assert.ok(mine, 'mine node missing');
  const pills = [...mine.querySelectorAll('.ext-pill')];
  assert.strictEqual(pills.length, 2);
  assert.ok(pills.some((p) => p.className.includes('mocked') && p.textContent.includes('mock:')));
  assert.ok(pills.some((p) => p.className.includes('hard') && p.textContent.includes('等:')));
  // Full-graph toggle brings external + out_of_scope nodes back.
  click(window, document.getElementById('btn-ext-canvas'));
  const allIds = [...document.querySelectorAll('.dag-node')].map((el) => el.dataset.node);
  assert.strictEqual(allIds.length, 6);
  assert.ok(allIds.includes('rb-ext'));
  assert.ok(allIds.includes('rb-red'));
});

test('hover/click external card focuses downstream edges', () => {
  const { document, window } = boot();
  const card = document.querySelector('.dock-card[data-ext-id="rb-ext"]');
  click(window, card);
  const focus = document.getElementById('external-focus');
  assert.ok(!focus.classList.contains('hidden'), 'focus bar hidden');
  assert.ok(focus.textContent.includes('CRM 接口'));
  assert.ok(focus.textContent.includes('mocked'));
  assert.ok(focus.textContent.includes('CRM 未确认'));
  const mine = document.querySelector('.dag-node[data-node="rb-a"]');
  assert.ok(mine.classList.contains('ext-linked'), 'dependent mine node not highlighted');
  const other = document.querySelector('.dag-node[data-node="rb-b"]');
  assert.ok(other.classList.contains('dimmed'), 'unrelated node not dimmed');
});

test('done with mocked edges asks whether to unmock downstream', () => {
  const { document, window } = boot();
  const doneBtn = document.querySelector('[data-ext-done="rb-ext"]');
  click(window, doneBtn);
  const popup = document.querySelector('.unmock-popup');
  assert.ok(popup, 'unmock confirm popup missing');
  assert.ok(popup.textContent.includes('Done + 拆 Mock'));
  assert.ok(popup.textContent.includes('ed-1'));
  assert.ok(popup.textContent.includes('CRM 未确认'));
});

test('filters, dock collapse and red-line capsule work', () => {
  const { document, window } = boot();
  click(window, document.querySelector('[data-dock-filter="ready"]'));
  assert.strictEqual(document.querySelectorAll('.dock-card').length, 1);
  click(window, document.querySelector('[data-dock-filter="waiting"]'));
  assert.strictEqual(document.querySelectorAll('.dock-card').length, 2);
  click(window, document.getElementById('btn-dock-toggle'));
  assert.ok(document.getElementById('dock-cards').classList.contains('hidden'));
  assert.strictEqual(document.getElementById('btn-dock-toggle').textContent, '展开');
  click(window, document.getElementById('btn-redline'));
  const panel = document.getElementById('redline-panel');
  assert.ok(!panel.classList.contains('hidden'));
  assert.ok(panel.textContent.includes('退订（本期不做）'));
  assert.strictEqual(document.getElementById('btn-redline').textContent, '红线 Out-of-Scope (1)');
});

test('PRD anchor on a canvas node zooms in and centers it', () => {
  const { document, window } = boot(wideSnapshot());
  const world = document.getElementById('dag-world');
  const before = parseTransform(world);
  assert.ok(before.scale < 0.2, 'expected fitted-out initial scale, got ' + before.scale);
  click(window, document.querySelector('.prd-block[data-anchor="p-wide"]'));
  const after = parseTransform(world);
  assert.ok(after.scale >= 0.8, 'scale not enlarged after anchor focus: ' + after.scale);
  assert.ok(Math.abs(after.scale - 0.9) < 1e-6, 'expected scale 0.9, got ' + after.scale);
  const center = viewportCenter(after);
  assert.ok(Math.abs(center.x - 195) < 1, 'not centered on node x: ' + center.x);
  assert.ok(Math.abs(center.y - 500) < 1, 'not centered on node y: ' + center.y);
  const target = document.querySelector('.dag-node[data-node="rb-a"]');
  assert.ok(target.classList.contains('selected'), 'target node not selected');
  assert.ok(target.classList.contains('locate-pulse'), 'target node not pulsed');
});

test('PRD anchor on a pure external node never pans canvas into blank space', () => {
  const { document, window } = boot();
  click(window, document.querySelector('.prd-block[data-anchor="p-ext"]'));
  const center = viewportCenter(parseTransform(document.getElementById('dag-world')));
  assert.ok(Math.abs(center.x - 195) < 1 && Math.abs(center.y - 500) < 1,
    'canvas should center on downstream rb-a, got ' + JSON.stringify(center));
  assert.ok(!(Math.abs(center.x - 195) < 1 && Math.abs(center.y - 120) < 1),
    'canvas was centered on external node coordinates');
  const ext = document.querySelector('.dock-card[data-ext-id="rb-ext"]');
  assert.ok(ext, 'external card missing');
  assert.ok(ext.classList.contains('linked'), 'external card not selected');
  assert.ok(ext.classList.contains('locate-pulse'), 'external card not pulsed');
  const mine = document.querySelector('.dag-node[data-node="rb-a"]');
  assert.ok(mine.classList.contains('ext-linked'), 'downstream node not highlighted');
  const bar = document.getElementById('anchor-links');
  assert.ok(!bar.classList.contains('hidden'), 'anchor bar hidden');
  assert.ok(bar.textContent.includes('CRM 接口'), 'external chip missing: ' + bar.textContent);
});

test('multi-node PRD anchor renders quick-jump bar and chips switch focus', () => {
  const { document, window } = boot();
  click(window, document.querySelector('.prd-block[data-anchor="p-multi"]'));
  const bar = document.getElementById('anchor-links');
  assert.ok(!bar.classList.contains('hidden'), 'anchor bar hidden');
  const chips = [...bar.querySelectorAll('[data-anchor-node]')];
  assert.deepStrictEqual(chips.map((c) => c.dataset.anchorNode), ['rb-a', 'rb-b', 'rb-ext']);
  assert.ok(document.querySelector('.dag-node[data-node="rb-a"]').classList.contains('selected'));
  click(window, bar.querySelector('[data-anchor-node="rb-b"]'));
  assert.ok(document.querySelector('.dag-node[data-node="rb-b"]').classList.contains('selected'));
  assert.ok(!document.querySelector('.dag-node[data-node="rb-a"]').classList.contains('selected'));
  assert.ok(bar.querySelector('[data-anchor-node="rb-b"]').classList.contains('active'));
  click(window, bar.querySelector('[data-anchor-node="rb-ext"]'));
  const ext = document.querySelector('.dock-card[data-ext-id="rb-ext"]');
  assert.ok(ext.classList.contains('linked'), 'external chip did not select dock card');
  assert.ok(document.querySelector('.dag-node[data-node="rb-a"]').classList.contains('ext-linked'),
    'external chip did not highlight downstream');
  click(window, bar.querySelector('[data-anchor-links-close]'));
  assert.ok(bar.classList.contains('hidden'), 'anchor bar not closed');
});

test('left PRD pane renders inline markdown safely', () => {
  const { document } = boot();
  const main = document.querySelector('.prd-block[data-anchor="p-main"]');
  const strong = main.querySelector('strong');
  assert.ok(strong, 'bold not rendered');
  assert.strictEqual(strong.textContent, '提交');
  assert.strictEqual(main.querySelector('code').textContent, 'code');
  const link = main.querySelector('a.prd-link');
  assert.ok(link, 'markdown link not rendered');
  assert.strictEqual(link.getAttribute('href'), 'https://example.com/doc');
  assert.ok(!main.textContent.includes('**'), 'raw bold markers leaked');
  const table = document.querySelector('.prd-block[data-anchor="p-table"]');
  assert.ok(table.querySelector('strong'), 'table bold not rendered');
  assert.ok(table.querySelector('code'), 'table code not rendered');
});

test('diff filter bar reflects snapshot and posts updates', () => {
  const { document, window, messages } = boot();
  const toggle = document.getElementById('df-exclude-tests');
  assert.strictEqual(toggle.checked, true);
  assert.strictEqual(document.getElementById('df-exclude').value, '**/*_mock.go, **/*_gen.go');
  assert.strictEqual(document.getElementById('df-ignored').textContent, '已过滤 1');

  toggle.checked = false;
  toggle.dispatchEvent(new window.Event('change', { bubbles: true }));
  let msg = messages.pop();
  assert.strictEqual(msg.type, 'filter.update');
  assert.strictEqual(msg.excludeTests, false);
  assert.strictEqual(msg.includeGlobs, undefined);

  document.getElementById('df-include').value = '*.go, src/**';
  document.getElementById('df-exclude').value = 'vendor/**, *_gen.go';
  click(window, document.getElementById('df-apply'));
  msg = messages.pop();
  assert.strictEqual(msg.type, 'filter.update');
  assert.strictEqual(msg.excludeTests, false);
  assert.deepStrictEqual([...msg.includeGlobs], ['*.go', 'src/**']);
  assert.deepStrictEqual([...msg.ignoreGlobs], ['vendor/**', '*_gen.go']);

  click(window, document.getElementById('df-reset'));
  msg = messages.pop();
  assert.strictEqual(msg.type, 'filter.update');
  assert.strictEqual(msg.reset, true);
});

function diffSnapshot() {
  const snap = JSON.parse(JSON.stringify(snapshot));
  snap.check.buckets.orphan = [
    { repo: 'order_api', file: 'pkg/ratelimit/order_limiter.go', symbol: 'NewOrderRateLimiter', added: 5, deleted: 0 },
    { repo: 'order_service', file: 'internal/precheck/precheck.go', symbol: 'PrecheckOrder', added: 2, deleted: 1 },
  ];
  snap.check.buckets.to_delete = [
    { repo: 'order_api', file: 'service/visibility.go', symbol: 'LegacyVisibilityScope', added: 0, deleted: 3 },
  ];
  return snap;
}

test('keyword search filters right pane without writing config', () => {
  const { document, window, messages } = boot(diffSnapshot());
  assert.strictEqual(document.querySelectorAll('#diff-body .diff-item').length, 3);
  const before = messages.length;
  const search = document.getElementById('df-search');
  search.value = 'precheck';
  search.dispatchEvent(new window.Event('input', { bubbles: true }));
  const items = [...document.querySelectorAll('#diff-body .diff-item')];
  assert.strictEqual(items.length, 1);
  assert.ok(items[0].textContent.includes('PrecheckOrder'));
  assert.strictEqual(messages.length, before, 'search must not post config updates');
  search.value = 'order_api';
  search.dispatchEvent(new window.Event('input', { bubbles: true }));
  assert.strictEqual(document.querySelectorAll('#diff-body .diff-item').length, 2);
});

console.log('all webview DOM tests passed');
