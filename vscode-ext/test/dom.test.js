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

function boot(data) {
  const { document, window } = parseHTML(BODY);
  // Each test gets its own snapshot: layout/delete interactions mutate positions.
  const snap = JSON.parse(JSON.stringify(data || snapshot));
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
    fn({ data: { type: 'snapshot', data: snap } });
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

test('node editor delete button routes through the delete guard', () => {
  const { document, window, messages } = boot();
  click(window, document.querySelector('.dag-node[data-node="rb-b"]'));
  const editor = document.querySelector('.popup');
  assert.ok(editor, 'node editor missing');
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
  click(window, document.querySelector('.dag-node[data-node="rb-a"]'));
  const editor = document.querySelector('.popup');
  assert.ok(editor, 'node editor missing');
  click(window, editor.querySelector('[data-editor-op-toggle="op-01"]'));
  assert.strictEqual(messages.pop().type, 'ops.toggle');
  click(window, document.querySelector('.dag-node[data-node="rb-a"]'));
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

console.log('all webview DOM tests passed');
