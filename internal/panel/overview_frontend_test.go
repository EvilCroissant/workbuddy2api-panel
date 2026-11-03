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
  realm: i % 2 ? 'global' : 'cn', disabled: i % 7 === 1, paused: i % 7 === 2,
  cool_remaining_sec: i % 7 === 3 ? 100 : 0,
}));
const overview = {
  total: 152, healthy: 152, cooling: 0, disabled: 0, paused: 0,
  sticky_sessions: 147, version: 'test', uptime_sec: 3600, accounts,
  model_locks: [{ model: 'limited-model', state: 'partial', servable: 100, total: 152, locked: 52 }],
};
let response = overview;
let failed = false, requests = 0;
sandbox.fetch = async () => { requests++; return ({
  ok: !failed, status: failed ? 503 : 200,
  json: async () => failed ? { error: '<upstream unavailable>' } : response,
}); };
const el = id => elements.get(id);
const load = () => vm.runInContext('loadOverview(true)', sandbox);
(async () => {
  await load();
  assert.equal(el('sTotal').textContent, 152);
  assert.equal(el('sCredits').textContent, '15200 / 30400');
  assert.equal((el('accBody').innerHTML.match(/<tr\b/g) || []).length, 20,
    'only the first page renders even when some models are rate limited');
  assert.equal(el('accPageNumber').textContent, '1 / 8');
  assert.equal(el('accPrev').disabled, true);
  assert.equal(el('accNext').disabled, false);
  assert.equal((el('expList').innerHTML.match(/class="exp-row"/g) || []).length, 10);
  assert.equal(el('expPageNumber').textContent, '1 / 16');
  assert.ok(el('accBody').innerHTML.includes('&lt;test-account&gt;'));
  assert.ok(el('accBody').innerHTML.includes('网关 '));
  assert.ok(el('accBody').innerHTML.includes('预计解封时间未知'));
  assert.ok(el('accBody').innerHTML.includes('模型当前不可用'));
  assert.ok(el('mlBody').innerHTML.includes('limited-model'));
  assert.ok(el('expList').innerHTML.includes('无近期到期批次'));
  assert.equal(el('accNote').textContent, '');

  const accountIDs = () => [...el('accBody').innerHTML.matchAll(/title="uid: ([^"]+)"/g)].map(m => m[1]);
  const set = (id, value) => { el(id).value = value; (el(id).oninput || el(id).onchange)(); };
  const evalJS = code => vm.runInContext(code, sandbox);
  const seen = [...accountIDs()];
  for (let i = 1; i < 8; i++) { el('accNext').onclick(); seen.push(...accountIDs()); }
  assert.equal(new Set(seen).size, 152, 'all accounts remain reachable across pages');
  assert.equal(accountIDs().length, 12);
  assert.equal(el('accNext').disabled, true);
  await load();
  assert.equal(el('accPageNumber').textContent, '8 / 8', 'polling preserves the page');
  el('accReset').onclick();
  set('accSearch', 'ACCOUNT-151');
  assert.deepEqual(accountIDs(), ['account-151'], 'UID search is case insensitive across all pages');
  set('accSearch', '<test-account>');
  assert.deepEqual(accountIDs(), ['account-0']);
  set('accSearch', 'not-present');
  assert.ok(el('accBody').innerHTML.includes('没有符合筛选条件'));
  assert.ok(!el('accBody').innerHTML.includes('账号池是空的'));
  el('accReset').onclick();
  set('accRealm', 'global'); set('accStatus', 'disabled');
  assert.equal(accountIDs().length, accounts.filter(a => a.realm === 'global' && a.disabled).length);
  assert.equal(el('sTotal').textContent, 152, 'filters do not change pool totals');
  set('accStatus', 'healthy');
  assert.equal(accountIDs().length, Math.min(20, accounts.filter(a => a.realm === 'global' && !a.disabled && !a.paused && !a.cool_remaining_sec).length));
  el('accReset').onclick();
  set('accPageSize', '50');
  assert.equal(accountIDs().length, 50);
  assert.equal(el('accPageNumber').textContent, '1 / 4');
  set('accPageSize', '20');
  evalJS("storeRemarks({ 'account-': '本机备注' })");
  set('accSearch', '本机备注');
  assert.ok(el('accCount').textContent.includes('匹配 152'));
  evalJS('storeRemarks({})'); el('accReset').onclick();
  assert.equal(requests, 2, 'pagination and filters never fetch upstream');

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

  // 后续成功刷新应恢复当前页并清除错误状态。
  failed = false;
  await load();
  assert.equal(el('accBody').innerHTML, rendered);
  assert.equal(el('accNote').textContent, '');

  // 冷却筛选包含熔断和降权，禁用/暂停状态优先。
  sandbox.statusAccounts = [
    { ...accounts[0], uid: 'breaker', breaker_until: at(1), cool_remaining_sec: 0 },
    { ...accounts[0], uid: 'degrade', degrade_until: at(1), cool_remaining_sec: 0 },
    { ...accounts[0], uid: 'paused', paused: true, breaker_until: at(1) },
  ];
  evalJS('renderAccounts(statusAccounts)');
  set('accStatus', 'cooling');
  assert.deepEqual(accountIDs(), ['breaker', 'degrade']);
  set('accStatus', 'paused');
  assert.deepEqual(accountIDs(), ['paused']);
  el('accReset').onclick();

  // 到期列表必须先全局排序再分页；快照/实时明细切换保留同一页与筛选。
  const isoDay = n => {
    const d = new Date(); d.setDate(d.getDate() + n);
    const pad = v => String(v).padStart(2, '0');
    return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate());
  };
  const expiryAccounts = accounts.map((a, i) => ({ ...a,
    credits_expiry_buckets: i < 150 ? [{ date: isoDay(1 + (149 - i) % 30), remain: 100 }] : [],
  }));
  response = { ...overview, accounts: expiryAccounts, balance_refresh_sec: 300 };
  await load();
  const expiryNames = () => [...el('expList').innerHTML.matchAll(/<span class="exp-nm">([^<]*)<\/span>/g)].map(m => m[1]);
  const snapshotFirst = el('expList').innerHTML;
  assert.equal(expiryNames().length, 10);
  assert.ok(el('expList').innerHTML.includes(isoDay(1)));
  const snapshotNote = el('expNote').textContent;
  el('expNext').onclick();
  assert.equal(el('expPageNumber').textContent, '2 / 16');
  assert.equal(el('expNote').textContent, snapshotNote, 'paging preserves snapshot freshness note');
  await load();
  assert.equal(el('expPageNumber').textContent, '2 / 16');
  assert.equal(el('accPageNumber').textContent, '1 / 8', 'the two lists paginate independently');
  el('expPrev').onclick();
  assert.equal(el('expList').innerHTML, snapshotFirst);
  const live = { accounts: expiryAccounts.map(a => ({ ...a, packages: a.credits_expiry_buckets.map(b => ({ end_time: b.date + 'T00:00:00', remain: b.remain })) })) };
  sandbox.live = live;
  evalJS('renderExpiry(live)');
  assert.equal(el('expList').innerHTML, snapshotFirst, 'live and snapshot pagination have identical sorted rows');
  set('expRange', '3');
  assert.equal(expiryNames().length, 10);
  assert.equal(el('expPageNumber').textContent, '1 / 2');
  el('expNext').onclick();
  assert.equal(expiryNames().length, 5);
  set('expRealm', 'global');
  assert.equal(expiryNames().length, 10);
  set('expRange', 'none');
  assert.deepEqual(expiryNames(), ['账号 151']);
  el('expReset').onclick();
  set('expSearch', 'account-149');
  assert.deepEqual(expiryNames(), ['账号 149']);
  set('expSearch', '<not-found>');
  assert.ok(el('expList').innerHTML.includes('没有符合筛选条件'));
  assert.equal(el('expNext').disabled, true);
  el('expReset').onclick();
  set('expPageSize', '20');
  assert.equal(expiryNames().length, 20);
  assert.equal(el('expPageNumber').textContent, '1 / 8');
  set('expPageSize', '10');
  sandbox.live = { accounts: [{ uid: 'error-user', nickname: '<err>', error: '<offline>' }, ...live.accounts] };
  evalJS('renderExpiry(live)');
  set('expRange', 'error');
  assert.ok(el('expList').innerHTML.includes('&lt;offline&gt;'));
  assert.ok(!el('expList').innerHTML.includes('<offline>'));
  el('expReset').onclick();

  // 旧状态文件只有最早批次，分页和日期/地区筛选同样生效。
  sandbox.legacy = { accounts: expiryAccounts.map((a, i) => ({ ...a,
    credits_expiry_buckets: [], credits_earliest_expiry: isoDay(1 + i % 30) + 'T12:00:00',
    credits_earliest_remaining: i === 151 ? 0 : 100,
  })) };
  evalJS('renderExpirySnapshot(legacy)');
  assert.equal(expiryNames().length, 10);
  set('expRange', '7');
  assert.ok(el('expList').innerHTML.includes('仅最早一批'));
  assert.equal(el('expPageNumber').textContent, '1 / 4');
  set('expRange', 'none');
  assert.deepEqual(expiryNames(), ['账号 151'], 'zero remaining balance is not an expiring batch');
  el('expReset').onclick();

  for (let i = 0; i < 20; i++) { el('accNext').onclick(); el('expNext').onclick(); }
  response = { ...overview, total: 1, accounts: [accounts[0]] };
  await load();
  assert.equal(el('accPageNumber').textContent, '1 / 1', 'removed accounts clamp the page');
  assert.equal(el('expPageNumber').textContent, '1 / 1');
  assert.equal(accountIDs().length, 1);
  assert.equal(el('expPrev').disabled, true);

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
