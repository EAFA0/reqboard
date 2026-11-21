const assert = require('assert');
const fs = require('fs');
const path = require('path');

const util = require('../dist/util.js');
const launcher = require('../dist/launcher.js');

function test(name, fn) {
  try {
    fn();
    console.log('ok - ' + name);
  } catch (e) {
    console.error('not ok - ' + name);
    throw e;
  }
}

test('normalizeRoot strips .reqboard', () => {
  assert.strictEqual(util.normalizeRoot('/tmp/req/.reqboard'), '/tmp/req');
  assert.strictEqual(util.normalizeRoot('/tmp/req'), '/tmp/req');
});

test('coverageClass maps all states', () => {
  assert.strictEqual(util.coverageClass('covered'), 'cov-covered');
  assert.strictEqual(util.coverageClass('unmapped'), 'cov-unmapped');
  assert.strictEqual(util.coverageClass('out_of_scope'), 'cov-redline');
  assert.strictEqual(util.coverageClass('ignored'), 'cov-ignored');
  assert.strictEqual(util.coverageClass('heading'), 'cov-heading');
});

test('formatDiffStat', () => {
  assert.strictEqual(util.formatDiffStat(3, 1), '+3/-1');
  assert.strictEqual(util.formatDiffStat(undefined, undefined), '+0/-0');
});

test('findSymbolLine handles func/method/type', () => {
  const src = [
    'package demo',
    '',
    'type Order struct {',
    '\tID string',
    '}',
    '',
    'func (o *Order) Create() string {',
    '\treturn o.ID',
    '}',
    '',
    'func Cancel() {}',
  ].join('\n');
  assert.strictEqual(util.findSymbolLine(src, 'Order.Create'), 7);
  assert.strictEqual(util.findSymbolLine(src, 'Create'), 7);
  assert.strictEqual(util.findSymbolLine(src, 'Cancel'), 11);
  assert.strictEqual(util.findSymbolLine(src, 'Order'), 3);
  assert.strictEqual(util.findSymbolLine(src, 'Missing'), 0);
});

test('package.json declares the board command and main entry', () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'package.json'), 'utf8'));
  assert.strictEqual(pkg.main, './dist/extension.js');
  assert.strictEqual(pkg.version, '0.3.0');
  const commands = (pkg.contributes.commands || []).map((c) => c.command);
  assert.ok(commands.includes('reqboard.openBoard'));
  assert.ok(fs.existsSync(path.join(__dirname, '..', 'dist', 'extension.js')));
});

test('webview renders PRD table rows and reserves long-title height', () => {
  const js = fs.readFileSync(path.join(__dirname, '..', 'media', 'board.js'), 'utf8');
  assert.ok(js.includes('function tableHTML'));
  assert.ok(js.includes('prd-table-row'));
  assert.ok(js.includes('NODE_H = 120'));
  assert.ok(js.includes('waiting_verification'));
  assert.ok(js.includes("btn-fit"));
  assert.ok(js.includes('Math.max(0.08'));
  const webview = fs.readFileSync(path.join(__dirname, '..', 'dist', 'webview.js'), 'utf8');
  assert.ok(webview.includes('btn-fit'));
});

test('prerequisite dock aggregates external/collaborator/todo and keeps main canvas clean', () => {
  const js = fs.readFileSync(path.join(__dirname, '..', 'media', 'board.js'), 'utf8');
  assert.ok(js.includes('function renderDock'));
  assert.ok(js.includes('data-ext-done'));
  assert.ok(js.includes('data-ext-edge'));
  assert.ok(js.includes('dockFilter'));
  assert.ok(js.includes('dockType'));
  assert.ok(js.includes('showExternalCanvas'));
  assert.ok(js.includes('function applyExternalHighlight'));
  assert.ok(js.includes('function openUnmockConfirm'));
  assert.ok(js.includes('function openTodoAdd'));
  assert.ok(js.includes('todo.add'));
  assert.ok(js.includes('external-focus'));
  assert.ok(!js.includes('function renderOpsPanel'), 'legacy ops drawer should be removed');
  const webview = fs.readFileSync(path.join(__dirname, '..', 'dist', 'webview.js'), 'utf8');
  assert.ok(webview.includes('external-dock'));
  assert.ok(webview.includes('btn-ext-canvas'));
  assert.ok(webview.includes('btn-add-todo'));
  assert.ok(webview.includes('data-dock-type'));
  assert.ok(webview.includes('btn-redline'));
  assert.ok(!webview.includes('btn-ops'), 'topbar ops drawer button should be removed');
  const panel = fs.readFileSync(path.join(__dirname, '..', 'dist', 'boardPanel.js'), 'utf8');
  assert.ok(panel.includes('unmock-downstream'));
  assert.ok(panel.includes('todo.add'));
});

test('webview assets are self-contained (no CDN)', () => {
  const js = fs.readFileSync(path.join(__dirname, '..', 'media', 'board.js'), 'utf8');
  const css = fs.readFileSync(path.join(__dirname, '..', 'media', 'board.css'), 'utf8');
  assert.ok(js.includes('acquireVsCodeApi'));
  assert.ok(js.includes('open.diff'));
  assert.ok(css.includes('.dag-node'));
  assert.ok(!/https?:\/\//.test(js), 'board.js must not reference external URLs');
  assert.ok(!/https?:\/\//.test(css), 'board.css must not reference external URLs');
});

test('launcher builds safe shell commands, prompt overrides and local stats', () => {
  assert.strictEqual(launcher.shellQuote("a'b c"), "'a'\\''b c'");
  assert.strictEqual(launcher.buildLaunchCommand('', 'hi'), '');
  assert.strictEqual(
    launcher.buildLaunchCommand('my-cli -- {prompt}', "hello 'world'"),
    "my-cli -- 'hello '\\''world'\\'''"
  );
  assert.strictEqual(
    launcher.buildLaunchCommand('my-cli -m fast', 'go'),
    "my-cli -m fast 'go'"
  );
  const p = launcher.renderPrompt('node_fix', {
    nodeId: 'rb-7',
    overrides: { node_fix: 'fix {nodeId}\nnow' },
  });
  assert.strictEqual(p, 'fix rb-7 now');

  let stats = launcher.recordLauncherUsage({}, 'push_goal', 'terminal', '2026-11-21T00:00:00Z');
  stats = launcher.recordLauncherUsage(stats, 'push_goal', 'clipboard', '2026-11-21T00:01:00Z');
  assert.strictEqual(stats.push_goal.total, 2);
  assert.strictEqual(stats.push_goal.terminal, 1);
  assert.strictEqual(stats.push_goal.clipboard, 1);
  const counts = launcher.summarizeLauncherCounts(stats);
  assert.strictEqual(counts.push_goal, 2);
  assert.strictEqual(counts.audit_orphan, 0);
});

console.log('all basic extension tests passed');
