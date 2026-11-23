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
  <button id="btn-undo"></button>
  <button id="btn-redo"></button>
  <button id="btn-ext-canvas"></button>
  <button id="btn-refresh"></button>
  <button id="btn-fit"></button>
  <button id="btn-layout"></button>
  <button id="btn-agent"></button>
  <button id="btn-orch"></button>
</header>
<main id="columns">
  <section id="prd-pane" class="pane">
    <button id="btn-expand-prd" class="pane-collapsed-rail"></button>
    <div class="pane-head prd-head">
      <div id="source-tabs"></div>
      <span id="coverage-badge" class="badge"></span>
      <button id="btn-collapse-prd"></button>
    </div>
    <div id="prd-list"></div>
  </section>
  <section id="dag-pane" class="pane">
    <button id="btn-add-node"></button>
    <div id="external-dock">
      <button id="btn-dock-toggle"></button>
      <span id="dock-count"></span>
      <button data-dock-type="all" class="active"></button>
      <button data-dock-type="external"></button>
      <button data-dock-type="todo"></button>
      <button data-dock-filter="all" class="active"></button>
      <button data-dock-filter="waiting"></button>
      <button data-dock-filter="ready"></button>
      <button id="btn-add-todo"></button>
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
  </section>
  <section id="diff-pane" class="pane">
    <button id="btn-expand-diff" class="pane-collapsed-rail"></button>
    <div class="pane-head">
      <span>Diff / Review</span>
      <span id="sel-node-badge"></span>
      <button id="btn-collapse-diff"></button>
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
  </section>
</main>
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

function op(id, kind, title, done, note) {
  return { id, kind, title, done: !!done, note: note || '' };
}

const snapshot = {
  config: { repos: [] },
  undo_depth: 2,
  redo_depth: 1,
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
  sources: [
    {
      id: 'meeting-review',
      title: '评审补充口径',
      kind: 'meeting',
      url: 'https://example.com/review',
      blocks: [
        { anchor: 'meeting-review:h1-1', kind: 'heading', level: 1, text: '# 评审补充', node_ids: [] },
        { anchor: 'meeting-review:1-1', kind: 'paragraph', text: '线下单在支付成功后即可见。', node_ids: ['rb-a'] },
        { anchor: 'meeting-review:1-2', kind: 'paragraph', text: '其余口径不变。', node_ids: [] },
      ],
    },
  ],
  bindings: [],
  leases: [],
  filter: { exclude_tests: true, include_globs: [], ignore_globs: ['**/*_mock.go', '**/*_gen.go'] },
  check: { node_stats: {}, buckets: { orphan: [], to_delete: [], broken_bindings: [] }, ignored_files: ['order_api:handler/order_query_mock.go'] },
  dispatch: { status: 'READY', items: [], blocked: {} },
  orchestrator: { state: 'running', session_id: 's-1' },
  events: [],
  dag: {
    nodes: [
      Object.assign(node('rb-a', '下单', 'mine', 'pending', 80, 440), {
        prd_refs: ['meeting-review:1-1'],
        refs: [{
          id: 'rf-01', kind: 'decision', title: '评审拍板',
          url: 'https://example.com/decision', quote: '先按补充口径实现',
          added_at: '2026-11-21T10:00:00+08:00',
        }],
        ops: [op('op-01', 'ddl', '新增可见性索引', false, '上线前执行'), op('op-02', 'flag', '订单可见性开关', true)],
      }),
      node('rb-b', '待批节点', 'mine', 'proposed', 400, 440),
      node('rb-ext', 'CRM 接口', 'external', 'pending', 80, 60),
      node('rb-ext2', '商品侧', 'collaborator', 'done', 360, 60),
      node('rb-ext3', '合同', 'external', 'pending', 640, 60),
      Object.assign(node('rb-todo', 'DDL: orders add confirmation_mode', 'todo', 'pending', 80, 60), {
        tags: ['ddl'],
        description: '上线前执行',
      }),
      node('rb-red', '退订（本期不做）', 'out_of_scope', 'pending', 920, 60),
    ],
    edges: [
      { id: 'ed-1', from: 'rb-ext', to: 'rb-a', kind: 'mocked', note: 'CRM 未确认' },
      { id: 'ed-2', from: 'rb-ext2', to: 'rb-b', kind: 'hard', note: '' },
      { id: 'ed-3', from: 'rb-ext3', to: 'rb-a', kind: 'hard', note: '' },
      { id: 'ed-4', from: 'rb-a', to: 'rb-b', kind: 'hard', note: '' },
      { id: 'ed-5', from: 'rb-todo', to: 'rb-a', kind: 'hard', note: '' },
      { id: 'ed-6', from: 'rb-todo', to: 'rb-b', kind: 'hard', note: '' },
    ],
  },
};

function boot(data, initialUiState) {
  const { document, window } = parseHTML(BODY);
  // Each test gets its own snapshot: layout/delete interactions mutate positions.
  const snap = JSON.parse(JSON.stringify(data || snapshot));
  const messages = [];
  let uiState = initialUiState ? JSON.parse(JSON.stringify(initialUiState)) : undefined;
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
    acquireVsCodeApi: () => ({
      postMessage: (m) => messages.push(m),
      getState: () => uiState,
      setState: (s) => { uiState = s; },
    }),
    setTimeout,
    clearTimeout,
    console,
  };
  vm.createContext(context);
  const code = fs.readFileSync(path.join(__dirname, '..', 'media', 'board.js'), 'utf8');
  vm.runInContext(code, context);
  for (const fn of listeners.message || []) {
    fn({ data: { type: 'snapshot', data: snap } });
  }
  return { document, window, messages, getUiState: () => uiState };
}

function click(window, el) {
  el.dispatchEvent(new window.Event('click', { bubbles: true }));
}

function dblclick(window, el) {
  el.dispatchEvent(new window.Event('dblclick', { bubbles: true }));
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

test('prerequisite dock aggregates external, collaborator and todo nodes', () => {
  const { document } = boot();
  assert.strictEqual(document.getElementById('dock-count').textContent, '前置依赖 1/4');
  const cards = [...document.querySelectorAll('.dock-card')];
  assert.strictEqual(cards.length, 4);
  const crm = cards.find((c) => c.dataset.extId === 'rb-ext');
  assert.ok(crm, 'CRM card missing');
  assert.ok(crm.textContent.includes('→ 1'), 'downstream impact missing: ' + crm.textContent);
  assert.ok(crm.textContent.includes('mock 1'), 'mock badge missing: ' + crm.textContent);
  const todo = cards.find((c) => c.dataset.todoId === 'rb-todo');
  assert.ok(todo, 'todo card missing');
  assert.ok(todo.classList.contains('todo-card'));
  assert.ok(todo.textContent.includes('DDL'), 'todo kind chip missing: ' + todo.textContent);
  assert.ok(todo.textContent.includes('→ 2'), 'todo downstream impact missing: ' + todo.textContent);
});

test('main canvas renders only mine/proposed and keeps prerequisite pills', () => {
  const { document, window } = boot();
  const ids = [...document.querySelectorAll('.dag-node')].map((el) => el.dataset.node).sort();
  assert.deepStrictEqual(ids, ['rb-a', 'rb-b']);
  const mine = document.querySelector('.dag-node[data-node="rb-a"]');
  assert.ok(mine, 'mine node missing');
  const pills = [...mine.querySelectorAll('.ext-pill')];
  assert.strictEqual(pills.length, 3);
  assert.ok(pills.some((p) => p.className.includes('mocked') && p.textContent.includes('mock:')));
  assert.ok(pills.some((p) => p.className.includes('hard') && p.textContent.includes('等:')));
  assert.ok(pills.some((p) => p.className.includes('todo') && p.textContent.includes('等:DDL')), 'todo pill missing: ' + pills.map((p) => p.textContent));
  // Full-graph toggle brings prerequisite + out_of_scope nodes back.
  click(window, document.getElementById('btn-ext-canvas'));
  const allIds = [...document.querySelectorAll('.dag-node')].map((el) => el.dataset.node);
  assert.strictEqual(allIds.length, 7);
  assert.ok(allIds.includes('rb-ext'));
  assert.ok(allIds.includes('rb-todo'));
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

test('type/status filters, dock collapse and red-line capsule work', () => {
  const { document, window } = boot();
  click(window, document.querySelector('[data-dock-type="todo"]'));
  assert.strictEqual(document.querySelectorAll('.dock-card').length, 1);
  assert.ok(document.querySelector('.dock-card[data-todo-id="rb-todo"]'));
  click(window, document.querySelector('[data-dock-type="external"]'));
  assert.strictEqual(document.querySelectorAll('.dock-card').length, 3);
  click(window, document.querySelector('[data-dock-type="all"]'));
  assert.strictEqual(document.querySelectorAll('.dock-card').length, 4);
  click(window, document.querySelector('[data-dock-filter="ready"]'));
  assert.strictEqual(document.querySelectorAll('.dock-card').length, 1);
  click(window, document.querySelector('[data-dock-filter="waiting"]'));
  assert.strictEqual(document.querySelectorAll('.dock-card').length, 3);
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
  assert.ok(after.scale >= 1.05 && after.scale <= 1.55, 'expected adaptive scale in [1.05, 1.55], got ' + after.scale);
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

function keydown(document, window, target, props) {
  const ev = new window.Event('keydown', { bubbles: true, cancelable: true });
  Object.assign(ev, props);
  target.dispatchEvent(ev);
}

test('undo/redo toolbar buttons expose depth and post history commands', () => {
  const { document, window, messages } = boot();
  const undo = document.getElementById('btn-undo');
  const redo = document.getElementById('btn-redo');
  assert.strictEqual(undo.textContent, '↩ 撤销 (2)');
  assert.strictEqual(redo.textContent, '↪ 重做 (1)');
  assert.strictEqual(undo.disabled, false);
  assert.strictEqual(redo.disabled, false);
  click(window, undo);
  assert.strictEqual(messages.pop().type, 'undo');
  click(window, redo);
  assert.strictEqual(messages.pop().type, 'redo');
});

test('Ctrl/Cmd+Z and Ctrl/Cmd+Shift+Z work outside inputs and are not hijacked in inputs', () => {
  const { document, window, messages } = boot();
  const before = messages.length;
  keydown(document, window, document.body, { key: 'z', ctrlKey: true });
  assert.strictEqual(messages[messages.length - 1].type, 'undo');
  keydown(document, window, document.body, { key: 'z', metaKey: true, shiftKey: true });
  assert.strictEqual(messages[messages.length - 1].type, 'redo');
  const input = document.getElementById('df-search');
  input.value = 'abc';
  keydown(document, window, input, { key: 'z', ctrlKey: true });
  assert.strictEqual(messages.length, before + 2, 'input native undo must not be intercepted');
});

test('Delete removes an unbound node and asks before force-deleting a bound node', () => {
  const { document, window, messages } = boot();
  click(window, document.querySelector('.prd-block[data-anchor="p-main"]'));
  keydown(document, window, document.body, { key: 'Delete' });
  const direct = messages.filter((m) => m.type === 'node.delete').pop();
  assert.ok(direct, 'node.delete message missing');
  assert.strictEqual(direct.id, 'rb-a');
  assert.ok(!direct.force, 'unbound delete must not force');

  const boundSnap = JSON.parse(JSON.stringify(snapshot));
  boundSnap.bindings = [{
    id: 'bd-1', node_id: 'rb-a', repo: 'order_api', file: 'handler/order_query.go',
    symbol: 'QueryOrder', role: 'impl', why: 'PRD 3',
  }];
  const bound = boot(boundSnap);
  click(bound.window, bound.document.querySelector('.prd-block[data-anchor="p-main"]'));
  keydown(bound.document, bound.window, bound.document.body, { key: 'Delete' });
  const popup = bound.document.querySelector('.delete-popup');
  assert.ok(popup, 'delete confirm popup missing for bound node');
  assert.ok(popup.textContent.includes('标为废弃'));
  assert.ok(popup.textContent.includes('强制删除'));
  click(bound.window, popup.querySelector('[data-force-delete]'));
  const forced = bound.messages.filter((m) => m.type === 'node.delete').pop();
  assert.strictEqual(forced.force, true);
});

test('node single-click selects without popup; double-click and right-pane edit button open editor', () => {
  const { document, window, messages } = boot();
  click(window, document.querySelector('.dag-node[data-node="rb-b"]'));
  assert.strictEqual(document.querySelector('.popup'), null, 'single click must not open floating editor popup');
  assert.strictEqual(document.getElementById('sel-node-badge').textContent, 'rb-b');

  click(window, document.querySelector('#diff-body [data-edit-node="rb-b"]'));
  assert.ok(document.querySelector('.popup'), 'right-pane edit button should open node editor');

  dblclick(window, document.querySelector('.dag-node[data-node="rb-b"]'));
  const editor = document.querySelector('.popup');
  assert.ok(editor, 'node editor missing on dblclick');
  click(window, editor.querySelector('[data-delete]'));
  const msg = messages.filter((m) => m.type === 'node.delete').pop();
  assert.ok(msg && msg.id === 'rb-b');
});

test('hovering a todo card highlights its blocked downstream nodes', () => {
  const snap = JSON.parse(JSON.stringify(snapshot));
  snap.dag.nodes.push(node('rb-iso', '独立节点', 'mine', 'pending', 720, 440));
  const { document, window } = boot(snap);
  const card = document.querySelector('.dock-card[data-todo-id="rb-todo"]');
  card.dispatchEvent(new window.Event('mouseover', { bubbles: true }));
  assert.ok(document.querySelector('.dag-node[data-node="rb-a"]').classList.contains('ext-linked'));
  assert.ok(document.querySelector('.dag-node[data-node="rb-b"]').classList.contains('ext-linked'));
  assert.ok(document.querySelector('.dag-node[data-node="rb-iso"]').classList.contains('dimmed'));
  card.dispatchEvent(new window.Event('mouseout', { bubbles: true }));
  assert.ok(!document.querySelector('.dag-node[data-node="rb-iso"]').classList.contains('dimmed'));
});

test('todo card click opens editor and saves kind/tags', () => {
  const { document, window, messages } = boot();
  click(window, document.querySelector('.dock-card[data-todo-id="rb-todo"]'));
  const editor = document.querySelector('.todo-edit-popup');
  assert.ok(editor, 'todo editor missing');
  assert.strictEqual(editor.querySelector('#te-title').value, 'DDL: orders add confirmation_mode');
  editor.querySelector('#te-title').value = 'DDL: orders add confirmation_mode v2';
  editor.querySelector('#te-kind').selectedIndex = 2;
  editor.querySelector('#te-block-start').checked = true;
  click(window, editor.querySelector('[data-save]'));
  const msg = messages.filter((m) => m.type === 'node.update').pop();
  assert.ok(msg, 'node.update message missing');
  assert.strictEqual(msg.id, 'rb-todo');
  assert.strictEqual(msg.patch.title, 'DDL: orders add confirmation_mode v2');
  assert.deepStrictEqual([...msg.patch.tags], ['flag', 'block-start']);
});

test('new TODO popup creates a todo and links selected downstream nodes', () => {
  const { document, window, messages } = boot();
  click(window, document.getElementById('btn-add-todo'));
  const popup = document.querySelector('.todo-add-popup');
  assert.ok(popup, 'todo add popup missing');
  popup.querySelector('#todo-title').value = 'Gateway: allow visibility query';
  popup.querySelector('#todo-kind').selectedIndex = 1;
  popup.querySelector('#todo-note').value = '网关白名单';
  popup.querySelector('.todo-block-row input[value="rb-a"]').checked = true;
  popup.querySelector('.todo-block-row input[value="rb-b"]').checked = true;
  click(window, popup.querySelector('[data-create]'));
  const msg = messages.filter((m) => m.type === 'todo.add').pop();
  assert.ok(msg, 'todo.add message missing');
  assert.strictEqual(msg.title, 'Gateway: allow visibility query');
  assert.strictEqual(msg.kind, 'gateway');
  assert.strictEqual(msg.note, '网关白名单');
  assert.deepStrictEqual([...msg.blocks], ['rb-a', 'rb-b']);
});

test('todo Done button posts done and unlocks downstream', () => {
  const { document, window, messages } = boot();
  click(window, document.querySelector('[data-ext-done="rb-todo"]'));
  const msg = messages.filter((m) => m.type === 'done').pop();
  assert.ok(msg, 'done message missing');
  assert.strictEqual(msg.id, 'rb-todo');
});

test('toolbar layout keeps full-graph mode when external canvas is on', () => {
  const { document, window, messages } = boot();
  click(window, document.getElementById('btn-ext-canvas'));
  click(window, document.getElementById('btn-layout'));
  let layout = messages.filter((m) => m.type === 'layout').pop();
  assert.strictEqual(layout.includeExternal, true);
  click(window, document.getElementById('btn-ext-canvas'));
  click(window, document.getElementById('btn-layout'));
  layout = messages.filter((m) => m.type === 'layout').pop();
  assert.strictEqual(layout.includeExternal, false);
});

test('node editor supports adding and toggling ops', () => {
  const { document, window, messages } = boot();
  dblclick(window, document.querySelector('.dag-node[data-node="rb-a"]'));
  const editor = document.querySelector('.popup');
  assert.ok(editor, 'node editor missing');
  click(window, editor.querySelector('[data-editor-op-toggle="op-01"]'));
  assert.strictEqual(messages.pop().type, 'ops.toggle');
  dblclick(window, document.querySelector('.dag-node[data-node="rb-a"]'));
  const editor2 = document.querySelector('.popup');
  editor2.querySelector('#ne-op-title').value = '编辑器新增 Flag';
  editor2.querySelector('#ne-op-kind').selectedIndex = 2;
  click(window, editor2.querySelector('[data-editor-op-add]'));
  const add = messages.pop();
  assert.strictEqual(add.type, 'ops.add');
  assert.strictEqual(add.kind, 'flag');
  assert.strictEqual(add.title, '编辑器新增 Flag');
});

test('right pane adds and toggles node ops', () => {
  const { document, window, messages } = boot();
  click(window, document.querySelector('.prd-block[data-anchor="p-main"]'));
  const input = document.getElementById('op-title-input');
  assert.ok(input, 'ops add input missing');
  input.value = '网关白名单';
  document.getElementById('op-kind-select').selectedIndex = 1;
  click(window, document.querySelector('[data-op-add="rb-a"]'));
  const add = messages.pop();
  assert.strictEqual(add.type, 'ops.add');
  assert.strictEqual(add.kind, 'gateway');
  assert.strictEqual(add.title, '网关白名单');
  click(window, document.querySelector('#diff-body [data-op-toggle="op-01"]'));
  const toggle = messages.pop();
  assert.strictEqual(toggle.type, 'ops.toggle');
  assert.strictEqual(toggle.opId, 'op-01');
});

test('full-graph mode lays prerequisite sources in column 0', () => {
  const { document, window, messages } = boot();
  click(window, document.getElementById('btn-ext-canvas'));
  const layout = messages.filter((m) => m.type === 'layout').pop();
  assert.ok(layout && layout.includeExternal === true, 'layout includeExternal message missing');
  for (const id of ['rb-ext', 'rb-ext2', 'rb-ext3', 'rb-todo']) {
    const el = document.querySelector(`.dag-node[data-node="${id}"]`);
    assert.ok(el, id + ' missing from full canvas');
    assert.strictEqual(el.style.left, '80px', id + ' not in column 0: ' + el.style.left);
  }
  const mine = document.querySelector('.dag-node[data-node="rb-a"]');
  assert.ok(parseFloat(mine.style.left) > 80, 'mine node should be right of sources: ' + mine.style.left);

  click(window, document.getElementById('btn-ext-canvas'));
  const back = messages.filter((m) => m.type === 'layout').pop();
  assert.strictEqual(back.includeExternal, false);
  assert.strictEqual(document.querySelector('.dag-node[data-node="rb-a"]').style.left, '80px');
});

test('left pane renders main PRD plus supplemental source tabs', () => {
  const { document, window } = boot();
  const tabs = [...document.querySelectorAll('#source-tabs [data-source-tab]')];
  assert.deepStrictEqual(tabs.map((t) => t.dataset.sourceTab), ['p', 'meeting-review']);
  assert.ok(tabs[0].classList.contains('active'), 'main PRD tab should be active by default');
  assert.ok(tabs[1].textContent.includes('评审补充口径'));
  assert.ok(tabs[1].querySelector('.tab-count'), 'referenced block count badge missing');
  assert.strictEqual(tabs[1].querySelector('.tab-count').textContent, '1');
  assert.ok(document.querySelector('[data-source-add]'), 'add-source button missing');

  click(window, tabs[1]);
  const active = document.querySelector('#source-tabs [data-source-tab="meeting-review"]');
  assert.ok(active.classList.contains('active'));
  const blocks = [...document.querySelectorAll('#prd-list .prd-block')];
  assert.strictEqual(blocks.length, 3);
  const referenced = document.querySelector('.prd-block[data-anchor="meeting-review:1-1"]');
  const quiet = document.querySelector('.prd-block[data-anchor="meeting-review:1-2"]');
  assert.ok(referenced.classList.contains('source-referenced'), 'referenced source block not highlighted');
  assert.ok(!referenced.classList.contains('cov-unmapped'), 'source block must never be marked Unmapped');
  assert.ok(quiet.classList.contains('source-quiet'), 'unreferenced source block should stay quiet');
  assert.ok(referenced.textContent.includes('1 节点'));
  assert.ok(document.getElementById('coverage-badge').textContent.includes('引用 1/2'));

  click(window, document.querySelector('#source-tabs [data-source-tab="p"]'));
  assert.ok(document.querySelector('.prd-block[data-anchor="p-main"]'), 'main PRD blocks not restored');
});

test('pasting meeting notes creates a supplemental source', () => {
  const { document, window, messages } = boot();
  click(window, document.querySelector('[data-source-add]'));
  const popup = document.querySelector('.source-add-popup');
  assert.ok(popup, 'source add popup missing');
  popup.querySelector('#src-title').value = '群聊拍板';
  popup.querySelector('#src-kind').selectedIndex = 1;
  popup.querySelector('#src-url').value = 'https://example.com/chat';
  popup.querySelector('#src-text').value = '# 群聊拍板\n\n- 先做可见性。';
  click(window, popup.querySelector('[data-save]'));
  const msg = messages.filter((m) => m.type === 'source.add').pop();
  assert.ok(msg, 'source.add message missing');
  assert.strictEqual(msg.title, '群聊拍板');
  assert.strictEqual(msg.kind, 'chat');
  assert.strictEqual(msg.url, 'https://example.com/chat');
  assert.ok(msg.text.includes('先做可见性'));
  assert.ok(!document.querySelector('.source-add-popup'), 'popup should close after save');
});

test('source block can attach its anchor to the selected node', () => {
  const { document, window, messages } = boot();
  click(window, document.querySelector('.prd-block[data-anchor="p-main"]'));
  click(window, document.querySelector('#source-tabs [data-source-tab="meeting-review"]'));
  const block = document.querySelector('.prd-block[data-anchor="meeting-review:1-2"]');
  assert.ok(block, 'source block missing');
  click(window, block.querySelector('[data-act="attach"]'));
  const msg = messages.filter((m) => m.type === 'node.update').pop();
  assert.ok(msg, 'node.update message missing');
  assert.strictEqual(msg.id, 'rb-a');
  assert.deepStrictEqual([...msg.patch.prd_refs], ['meeting-review:1-1', 'meeting-review:1-2']);
});

test('clicking a supplemental anchor on the canvas switches the left tab', () => {
  const { document, window } = boot();
  const chip = document.querySelector('.dag-node[data-node="rb-a"] [data-anchor-chip="meeting-review:1-1"]');
  assert.ok(chip, 'node source anchor chip missing');
  click(window, chip);
  const tab = document.querySelector('#source-tabs [data-source-tab="meeting-review"]');
  assert.ok(tab.classList.contains('active'), 'left pane did not switch to source tab');
  const block = document.querySelector('.prd-block[data-anchor="meeting-review:1-1"]');
  assert.ok(block.classList.contains('selected'), 'source block not highlighted');
});

test('node card shows a ref micro badge for refs and supplemental anchors', () => {
  const { document } = boot();
  const chip = document.querySelector('.dag-node[data-node="rb-a"] .chip.ref');
  assert.ok(chip, 'ref micro badge missing');
  assert.strictEqual(chip.textContent, 'ref 2');
});

test('right pane lists, adds and removes node refs', () => {
  const { document, window, messages } = boot();
  click(window, document.querySelector('.prd-block[data-anchor="p-main"]'));
  const rows = [...document.querySelectorAll('#diff-body .ref-row')];
  assert.strictEqual(rows.length, 1);
  assert.ok(rows[0].textContent.includes('评审拍板'));
  assert.ok(rows[0].textContent.includes('先按补充口径实现'));
  assert.ok(rows[0].querySelector('[data-ref-url]'), 'ref URL link missing');

  document.getElementById('ref-kind-select').selectedIndex = 1;
  document.getElementById('ref-title-input').value = '评审会议纪要';
  document.getElementById('ref-url-input').value = 'https://example.com/meeting';
  document.getElementById('ref-quote-input').value = '确认可见性口径';
  click(window, document.querySelector('[data-ref-add="rb-a"]'));
  const add = messages.filter((m) => m.type === 'ref.add').pop();
  assert.ok(add, 'ref.add message missing');
  assert.strictEqual(add.nodeId, 'rb-a');
  assert.strictEqual(add.kind, 'meeting');
  assert.strictEqual(add.title, '评审会议纪要');
  assert.strictEqual(add.url, 'https://example.com/meeting');
  assert.strictEqual(add.quote, '确认可见性口径');

  click(window, document.querySelector('#diff-body [data-ref-remove="rf-01"]'));
  const remove = messages.filter((m) => m.type === 'ref.remove').pop();
  assert.ok(remove, 'ref.remove message missing');
  assert.strictEqual(remove.nodeId, 'rb-a');
  assert.strictEqual(remove.refId, 'rf-01');
});

test('selected node diff renders above global orphan alerts with attributed stats and resets scroll', () => {
  const snap = diffSnapshot();
  snap.bindings = [
    { id: 'b-1', node_id: 'rb-a', repo: 'order_api', file: 'service/create.go', symbol: 'CreateOrder', role: 'impl', why: 'main entry' },
  ];
  snap.check.buckets.attributed = [
    { repo: 'order_api', file: 'service/create.go', symbol: 'CreateOrder', start: 42, added: 18, deleted: 3, node_ids: ['rb-a'] },
  ];
  const { document, window } = boot(snap);
  const diffBody = document.getElementById('diff-body');
  diffBody.scrollTop = 260;

  click(window, document.querySelector('.prd-block[data-anchor="p-main"]'));
  assert.strictEqual(diffBody.scrollTop, 0, 'diff pane should scroll to top on node select');

  const html = diffBody.innerHTML;
  const bindingIdx = html.indexOf('本节点 Diff (1 文件 · 1 符号)');
  const orphanIdx = html.indexOf('Orphan Diff (2 文件 · 2 符号)');
  assert.ok(bindingIdx >= 0, 'node diff section missing');
  assert.ok(orphanIdx > bindingIdx, 'selected node diff must render above global Orphan Diff');

  // File groups start collapsed; clicking chevron expands child symbol cards.
  const fgBody = diffBody.querySelector('.file-group-body');
  assert.ok(fgBody && fgBody.classList.contains('hidden'), 'file group should default collapsed');
  click(window, diffBody.querySelector('[data-file-toggle]'));
  assert.ok(!diffBody.querySelector('.file-group-body').classList.contains('hidden'), 'file group should expand on chevron click');

  const bindingEl = diffBody.querySelector('.binding');
  assert.ok(bindingEl, 'binding card missing');
  assert.strictEqual(bindingEl.dataset.line, '42');
  assert.ok(bindingEl.textContent.includes('+18'), 'attributed +added missing');
  assert.ok(bindingEl.textContent.includes('-3'), 'attributed -deleted missing');
});

test('agent launcher menu dispatches workspace actions, copy-only, orphan audit and node work/fix', () => {
  const snap = diffSnapshot();
  snap.launcher = { configured: true, counts: { push_goal: 4, review_simplify: 2 } };
  snap.dag.nodes[0].review_notes = [{ id: 'rn-1', status: 'open', comment: 'fix nil check' }];
  const { document, window, messages } = boot(snap);

  click(window, document.getElementById('btn-agent'));
  const popup = document.querySelector('.launcher-popup');
  assert.ok(popup, 'launcher menu did not open');
  assert.ok(popup.textContent.includes('4 次'), 'usage count badge missing');
  assert.ok(popup.textContent.includes('2 次'), 'second usage count badge missing');

  click(window, popup.querySelector('[data-menu-launch="review_simplify"]'));
  let msg = messages.pop();
  assert.strictEqual(msg.type, 'agent.launch');
  assert.strictEqual(msg.action, 'review_simplify');
  assert.strictEqual(msg.copyOnly, undefined);

  click(window, document.getElementById('btn-agent'));
  click(window, document.querySelector('[data-menu-copy="push_goal"]'));
  msg = messages.pop();
  assert.strictEqual(msg.type, 'agent.launch');
  assert.strictEqual(msg.action, 'push_goal');
  assert.strictEqual(msg.copyOnly, true);

  // Orphan header quick audit button does not collapse the bucket.
  const orphanBtn = document.querySelector('#diff-body .bucket-action-btn[data-launch-action="audit_orphan"]');
  assert.ok(orphanBtn, 'orphan quick audit button missing');
  click(window, orphanBtn);
  msg = messages.pop();
  assert.strictEqual(msg.type, 'agent.launch');
  assert.strictEqual(msg.action, 'audit_orphan');
  assert.ok(!document.querySelector('#diff-body .bucket-body').classList.contains('hidden'), 'bucket collapsed on audit click');

  // Selected node with open review note dispatches node_fix; without open notes dispatches node_work.
  click(window, document.querySelector('.prd-block[data-anchor="p-main"]'));
  const fixBtn = document.querySelector('#diff-body [data-launch-node="rb-a"]');
  assert.ok(fixBtn, 'node launch button missing');
  assert.strictEqual(fixBtn.dataset.launchAction, 'node_fix');
  click(window, fixBtn);
  msg = messages.pop();
  assert.strictEqual(msg.type, 'agent.launch');
  assert.strictEqual(msg.action, 'node_fix');
  assert.strictEqual(msg.nodeId, 'rb-a');
});

test('left PRD and right Diff panes collapse/expand, persist state, and PRD anchor auto-expands PRD pane', () => {
  const { document, window, getUiState } = boot(undefined, { prdCollapsed: false, diffCollapsed: false });
  const cols = document.getElementById('columns');
  const prd = document.getElementById('prd-pane');
  const diff = document.getElementById('diff-pane');

  click(window, document.getElementById('btn-collapse-prd'));
  assert.ok(cols.classList.contains('prd-collapsed'));
  assert.ok(prd.classList.contains('collapsed'));
  assert.strictEqual(getUiState().prdCollapsed, true);

  click(window, document.getElementById('btn-collapse-diff'));
  assert.ok(cols.classList.contains('diff-collapsed'));
  assert.ok(diff.classList.contains('collapsed'));
  assert.strictEqual(getUiState().diffCollapsed, true);

  // Clicking a PRD anchor chip on canvas auto-expands the collapsed PRD pane while keeping Diff collapsed.
  click(window, document.querySelector('.node-anchor-chip[data-anchor-chip="meeting-review:1-1"]'));
  assert.ok(!cols.classList.contains('prd-collapsed'), 'PRD pane should auto-expand on anchor jump');
  assert.ok(cols.classList.contains('diff-collapsed'), 'Diff pane should stay collapsed');

  click(window, document.getElementById('btn-expand-diff'));
  assert.ok(!cols.classList.contains('diff-collapsed'));
  assert.ok(!diff.classList.contains('collapsed'));
  assert.strictEqual(getUiState().diffCollapsed, false);
});

test('clicking a TODO dock card zooms in and centers on its downstream canvas node', () => {
  const { document, window } = boot(wideSnapshot());
  const world = document.getElementById('dag-world');
  const before = parseTransform(world);
  assert.ok(before.scale < 0.2, 'expected fitted-out initial scale, got ' + before.scale);
  click(window, document.querySelector('.dock-card[data-todo-id="rb-todo"]'));
  const after = parseTransform(world);
  assert.ok(after.scale >= 0.78, 'expected zoomed-in readable scale on TODO click, got ' + after.scale);
  assert.ok(after.scale > before.scale * 4, 'expected strong zoom-in from fitted-out view');
  const center = viewportCenter(after);
  assert.ok(center.x >= 190 && center.x <= 560 && Math.abs(center.y - 500) < 1,
    'expected canvas centered on downstream nodes, got ' + JSON.stringify(center));
});

test('top prerequisite dock sorts unfinished nodes before done nodes', () => {
  const snap = wideSnapshot();
  // Put a done prerequisite first in the raw snapshot to ensure UI reorders unfinished ahead of done.
  snap.dag.nodes = [
    { id: 'rb-done-first', title: '已完成外部依赖', scope: 'external', status: 'done', position: { x: 0, y: 0 } },
    ...snap.dag.nodes,
  ];
  const { document } = boot(snap);
  const ids = [...document.querySelectorAll('.dock-card')].map((el) => el.dataset.extId);
  const doneIdx = ids.indexOf('rb-done-first');
  const pendingIdx = ids.indexOf('rb-todo');
  assert.ok(pendingIdx >= 0 && doneIdx >= 0, 'expected both pending and done cards');
  assert.ok(pendingIdx < doneIdx, 'unfinished prerequisite should appear before done: ' + JSON.stringify(ids));
});

test('selecting a canvas node highlights it in blue, neighbors in amber, and dims unrelated nodes/edges', () => {
  const snap = wideSnapshot();
  snap.dag.nodes.push({ id: 'rb-far2', title: '远端节点2', scope: 'mine', status: 'pending', position: { x: 4500, y: 440 } });
  snap.dag.edges.push({ id: 'e-far', from: 'rb-far', to: 'rb-far2', kind: 'hard' });
  const { document, window } = boot(snap);
  click(window, document.querySelector('.dag-node[data-node="rb-a"]'));
  assert.ok(document.querySelector('.dag-node[data-node="rb-a"]').classList.contains('selected'));
  assert.ok(document.querySelector('.dag-node[data-node="rb-b"]').classList.contains('neighbor-linked'));
  assert.ok(document.querySelector('.dag-node[data-node="rb-far"]').classList.contains('dimmed'));
  const incident = document.querySelector('#dag-edges path.edge[data-from="rb-a"][data-to="rb-b"]');
  const unrelated = document.querySelector('#dag-edges path.edge[data-from="rb-far"][data-to="rb-far2"]');
  assert.ok(incident && incident.classList.contains('edge-highlighted'), 'incident edge should be highlighted');
  assert.ok(unrelated && unrelated.classList.contains('edge-dimmed'), 'unrelated edge should be dimmed');
  const jumpBtn = document.querySelector('#diff-body [data-jump-node="rb-b"]');
  assert.ok(jumpBtn, 'right pane should list outgoing neighbor rb-b');
  click(window, jumpBtn);
  assert.ok(document.querySelector('.dag-node[data-node="rb-b"]').classList.contains('selected'),
    'clicking neighbor link in right pane should select and jump to rb-b');
});

test('left PRD pane safely renders HTML tables, mentions, callouts, and inline images with lightbox', () => {
  const snap = wideSnapshot();
  snap._imageUris = { './arch.svg': 'vscode-webview://test/arch.svg' };
  snap.prd.blocks = [
    {
      anchor: 'p-html',
      kind: 'paragraph',
      text: '<table><colgroup><col/></colgroup><tbody><tr><td><b>模块</b></td><td><b>详情</b></td></tr><tr><td rowspan="2">入口</td><td><ol><li>支持代理 <cite type="user" user-id="u1" user-name="Alice"></cite></li></ol><img name="order.png" alt="控制台截图"/></td></tr></tbody></table><script>window.__xss=1</script>',
      coverage: 'covered',
      node_ids: ['rb-a'],
    },
    {
      anchor: 'p-img',
      kind: 'paragraph',
      text: '架构图：![系统架构](./arch.svg)',
      coverage: 'covered',
      node_ids: ['rb-a'],
    },
    {
      anchor: 'p-call',
      kind: 'paragraph',
      text: '> 核心边界：不改旧接口',
      coverage: 'covered',
      node_ids: ['rb-a'],
    },
  ];
  const { document, window } = boot(snap);
  const htmlBlock = document.querySelector('.prd-block[data-anchor="p-html"]');
  assert.ok(htmlBlock.querySelector('table.prd-html-table'), 'expected real HTML table');
  assert.ok(htmlBlock.querySelector('td[rowspan="2"]'), 'expected rowspan preserved');
  assert.strictEqual(htmlBlock.querySelector('.prd-mention').textContent, '@Alice');
  assert.ok(htmlBlock.querySelector('.prd-img-chip'), 'expected compact image chip for unresolved img');
  assert.ok(!htmlBlock.querySelector('script') && !htmlBlock.textContent.includes('__xss'), 'script must be stripped');
  const img = document.querySelector('.prd-block[data-anchor="p-img"] img.prd-img');
  assert.ok(img && img.getAttribute('src') === 'vscode-webview://test/arch.svg', 'expected resolved local image');
  click(window, img);
  const lb = document.querySelector('.prd-lightbox');
  assert.ok(lb, 'clicking inline image should open lightbox');
  click(window, lb);
  assert.ok(!document.querySelector('.prd-lightbox'), 'clicking lightbox should close it');
  assert.ok(document.querySelector('.prd-block[data-anchor="p-call"] .prd-callout'), 'expected callout rendering');
});

(async () => {
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));
  // 1. Hovering a dock card peeks (zooms & centers), leaving restores original view.
  {
    const { document, window } = boot(wideSnapshot());
    const world = document.getElementById('dag-world');
    const before = parseTransform(world);
    const ext = document.querySelector('.dock-card[data-ext-id="rb-ext"]');
    ext.dispatchEvent(new window.Event('mouseover', { bubbles: true }));
    await wait(140);
    const during = parseTransform(world);
    assert.ok(during.scale >= 1.05 && during.scale <= 1.55, 'expected adaptive hover-peek scale, got ' + during.scale);
    assert.ok(world.classList.contains('camera-anim'), 'expected smooth camera animation on hover peek');
    const center = viewportCenter(during);
    assert.ok(Math.abs(center.x - 195) < 1 && Math.abs(center.y - 500) < 1,
      'hover peek did not center on downstream node: ' + JSON.stringify(center));
    ext.dispatchEvent(new window.Event('mouseout', { bubbles: true }));
    await wait(160);
    const after = parseTransform(world);
    assert.ok(Math.abs(after.scale - before.scale) < 1e-6 && Math.abs(after.tx - before.tx) < 1e-4 && Math.abs(after.ty - before.ty) < 1e-4,
      'expected view restored after mouseout: before=' + JSON.stringify(before) + ' after=' + JSON.stringify(after));
    console.log('ok - hovering dock card adaptively zooms & centers and restores view on leave');
  }
  // 2. Hovering a PRD block peeks and restores; clicking commits so leave does not snap back.
  {
    const { document, window } = boot(wideSnapshot());
    const world = document.getElementById('dag-world');
    const before = parseTransform(world);
    const block = document.querySelector('.prd-block[data-anchor="p-wide"]');
    block.dispatchEvent(new window.Event('mouseover', { bubbles: true }));
    await wait(180);
    const during = parseTransform(world);
    assert.ok(during.scale >= 1.05, 'expected PRD hover-peek zoom, got ' + during.scale);
    click(window, block);
    block.dispatchEvent(new window.Event('mouseout', { bubbles: true }));
    await wait(160);
    const afterClickLeave = parseTransform(world);
    assert.ok(afterClickLeave.scale >= 1.05 && afterClickLeave.scale !== before.scale,
      'expected clicked PRD block to keep zoomed view after mouseout');
    console.log('ok - hovering PRD block peeks and clicking commits zoom');
  }
  console.log('all webview DOM tests passed');
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
