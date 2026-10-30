package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 执行整份严格模式脚本及异步 overview 渲染。顶层冒烟的 pending fetch
// 和非严格模式的函数切片都捕获不到「有模型限流记录才整表空白」的问题。
func TestAppJSOverviewRender(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; overview render test skipped")
	}
	const script = `const fs = require('fs');
const vm = require('vm');
const assert = require('assert/strict');
const src = fs.readFileSync('app.js', 'utf8');
const html = fs.readFileSync('index.html', 'utf8');
const elements = new Map();
function element() {
  return {
    innerHTML: '', textContent: '', hidden: false, value: '', dataset: {}, style: {},
    get children() { return this.innerHTML ? [{}] : []; },
    classList: { add() {}, remove() {}, toggle() {} },
    addEventListener() {}, setAttribute() {}, querySelector() { return null; },
    querySelectorAll() { return []; }, appendChild() {},
  };
}
for (const match of html.matchAll(/\bid="([^"]+)"/g)) elements.set(match[1], element());
const sandbox = {
  location: { hash: '#accounts' }, history: { replaceState() {} },
  localStorage: { getItem() { return null; }, setItem() {} },
  document: {
    getElementById: id => elements.get(id) || null,
    querySelectorAll: () => [], querySelector: () => null,
    documentElement: element(), addEventListener() {}, createElement: element,
  },
  fetch: () => new Promise(() => {}),
  addEventListener() {}, matchMedia: () => ({ matches: false }),
  setTimeout() {}, clearTimeout() {}, setInterval() {}, clearInterval() {},
  URLSearchParams, console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
vm.runInContext(src, sandbox, { filename: 'app.js' });
const at = hours => new Date(Date.now() + hours * 3600000).toISOString();
const limits = [
  null,
  [{ model: 'limited-model', kind: 'rate_limit', until: at(1), reset_at: at(2) }],
  [{ model: 'limited-model', kind: 'rate_limit', until: at(2), reset_at: at(2) }],
  [{ model: 'limited-model', kind: 'rate_limit', until: at(1) }],
  [{ model: 'limited-model', kind: 'rate_limit' }],
  [{ model: 'missing-model', kind: 'model_unavailable', until: at(1) }],
];
const accounts = Array.from({ length: 152 }, (_, i) => ({
  uid: 'account-' + i, nickname: i === 0 ? '<test-account>' : '账号 ' + i,
  credits: 100, credits_total: 200, rate_limited_models: limits[i % limits.length],
}));
const overview = {
  total: 152, healthy: 152, cooling: 0, disabled: 0, paused: 0,
  sticky_sessions: 147, version: 'test', uptime_sec: 3600, accounts,
  model_locks: [{ model: 'limited-model', state: 'partial', servable: 100, total: 152, locked: 52 }],
};
let response = overview;
let failed = false;
sandbox.fetch = async () => ({
  ok: !failed, status: failed ? 503 : 200,
  json: async () => failed ? { error: '<upstream unavailable>' } : response,
});
const el = id => elements.get(id);
const load = () => vm.runInContext('loadOverview(true)', sandbox);
(async () => {
  await load();
  assert.equal(el('sTotal').textContent, 152);
  assert.equal(el('sCredits').textContent, '15200 / 30400');
  assert.equal((el('accBody').innerHTML.match(/<tr\b/g) || []).length, 152,
    'every account must render even when some models are rate limited');
  assert.ok(el('accBody').innerHTML.includes('&lt;test-account&gt;'));
  assert.ok(el('accBody').innerHTML.includes('网关 '));
  assert.ok(el('accBody').innerHTML.includes('预计解封时间未知'));
  assert.ok(el('accBody').innerHTML.includes('模型当前不可用'));
  assert.ok(el('mlBody').innerHTML.includes('limited-model'));
  assert.ok(el('expList').innerHTML.includes('无近期到期批次'));
  assert.equal(el('accNote').textContent, '');

  // 自动刷新失败时保留已有行，并把错误显示在表头。
  const rendered = el('accBody').innerHTML;
  failed = true;
  await load();
  assert.equal(el('accBody').innerHTML, rendered);
  assert.ok(el('accNote').textContent.includes('账号池加载失败'));

  // 首次加载失败也要有可见状态，错误内容必须转义。
  el('accBody').innerHTML = '';
  await load();
  assert.ok(el('accBody').innerHTML.includes('&lt;upstream unavailable&gt;'));
  assert.ok(!el('accBody').innerHTML.includes('<upstream unavailable>'));

  // 后续成功刷新应恢复全部行并清除错误状态。
  failed = false;
  await load();
  assert.equal(el('accBody').innerHTML, rendered);
  assert.equal(el('accNote').textContent, '');

  response = { ...overview, total: 0, healthy: 0, accounts: [], model_locks: null };
  await load();
  assert.ok(el('accBody').innerHTML.includes('账号池是空的'));
  assert.ok(el('mlBody').innerHTML.includes('当前没有模型级限流'));
  console.log('OVERVIEW OK');
})().catch(err => { console.error(err); process.exitCode = 1; });`
	path := filepath.Join(t.TempDir(), "overview.cjs")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, path).CombinedOutput(); err != nil {
		t.Fatalf("overview render failed: %v\n%s", err, out)
	}
}
