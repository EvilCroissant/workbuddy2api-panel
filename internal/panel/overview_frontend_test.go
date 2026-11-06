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
  assert.equal(el('sCredits').innerHTML, '<span>15,200</span><small class="credit-total"> / 30,400</small>');
  assert.equal(el('sCredits').title, '15200 / 30400', 'exact totals remain available');
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
  assert.equal(el('sCredits').innerHTML, '<span>0</span>', 'empty pools clear the previous total');
  assert.equal(el('sCredits').title, '0');
  assert.ok(el('accBody').innerHTML.includes('账号池是空的'));
  assert.ok(el('mlBody').innerHTML.includes('当前没有模型级限流'));
  assert.equal(el('btnRemoveDisabled').disabled, true);

  // 删除覆盖全池，取消不写入，重复点击及后台轮询不会重入。
  response = { ...overview, disabled: 1183 };
  await load();
  set('accSearch', 'not-present');
  assert.equal(el('btnRemoveDisabled').disabled, false, 'filters do not hide disabled accounts from bulk removal');
  let confirmation = '', confirmed = false;
  sandbox.confirm = text => { confirmation = text; return confirmed; };
  const beforeCancel = requests;
  await el('btnRemoveDisabled').onclick();
  assert.equal(requests, beforeCancel, 'cancel must not issue a request');
  assert.ok(confirmation.includes('1183'));
  assert.ok(confirmation.includes('不受筛选和分页影响'));
  confirmed = true;
  let finishDelete, deleteCalls = 0;
  const notices = [];
  sandbox.toast = (text, kind) => notices.push({ text, kind });
  sandbox.fetch = async (url, opts) => {
    if (url === '/panel/api/accounts/remove_disabled') {
      assert.equal(opts.method, 'POST');
      deleteCalls++;
      return new Promise(resolve => { finishDelete = body => resolve({ ok: true, status: 200, json: async () => body }); });
    }
    return { ok: true, status: 200, json: async () => response };
  };
  const deleting = el('btnRemoveDisabled').onclick();
  assert.equal(el('btnRemoveDisabled').disabled, true);
  assert.equal(el('btnRemoveDisabled').textContent, '删除中…');
  await el('btnRemoveDisabled').onclick();
  await load();
  assert.equal(el('btnRemoveDisabled').disabled, true, 'polling must not re-enable an active deletion');
  assert.equal(deleteCalls, 1, 'one bulk request rather than one request per account');
  response = { ...overview, total: 1, disabled: 0, accounts: [accounts[0]] };
  finishDelete({ removed: 1183, failed: 0, errors: [] });
  await deleting;
  assert.equal(el('sDisabled').textContent, 0);
  assert.equal(el('btnRemoveDisabled').disabled, true);
  assert.equal(el('btnRemoveDisabled').textContent, '删除已禁用');
  assert.ok(notices[0].text.includes('1183'));

  response = { ...response, disabled: 1 };
  await load();
  const partial = el('btnRemoveDisabled').onclick();
  finishDelete({ removed: 0, failed: 1, errors: [{ uid: 'failed', error: 'permission denied' }] });
  await partial;
  assert.equal(el('btnRemoveDisabled').disabled, false, 'failed accounts can be retried');
  assert.ok(notices[1].text.includes('已保留'));
  assert.ok(notices[1].text.includes('permission denied'));
  assert.equal(notices[1].kind, 'err');

  // 零积分删除仅包含已有余额记录的账号；默认零值、负数和正余额均不纳入。
  response = { ...overview, disabled: 1, accounts: [
    { uid: 'unknown', credits: 0 },
    { uid: 'failed-query', credits: 0, credits_known: false, credits_total: 100 },
    { uid: 'negative', credits: -1, credits_known: true },
    { uid: 'positive', credits: 1, credits_known: true },
  ] };
  await load();
  assert.equal(el('btnRemoveZeroCredits').disabled, true);
  const zeroAccounts = Array.from({ length: 526 }, (_, i) => ({
    uid: 'zero-' + i, credits: 0, credits_known: true, credits_total: i % 2 ? 100 : 0,
    disabled: i % 4 === 1, paused: i % 4 === 2, realm: i % 2 ? 'global' : 'cn',
  }));
  response = { ...response, accounts: [...response.accounts, ...zeroAccounts] };
  await load();
  set('accRealm', 'cn');
  set('accSearch', 'not-present');
  assert.equal(el('btnRemoveZeroCredits').disabled, false);
  assert.ok(el('btnRemoveZeroCredits').title.includes('526'));
  confirmed = false;
  deleteCalls = 0;
  sandbox.fetch = async (url, opts) => {
    if (url.startsWith('/panel/api/accounts/remove_')) {
      assert.equal(url, '/panel/api/accounts/remove_zero_credits');
      assert.equal(opts.method, 'POST');
      deleteCalls++;
      return new Promise(resolve => { finishDelete = body => resolve({ ok: true, status: 200, json: async () => body }); });
    }
    return { ok: true, status: 200, json: async () => response };
  };
  await el('btnRemoveZeroCredits').onclick();
  assert.equal(deleteCalls, 0, 'cancel zero-credit deletion without making a request');
  assert.ok(confirmation.includes('526'));
  assert.ok(confirmation.includes('余额未知的账号会保留'));
  assert.ok(confirmation.includes('不受筛选和分页影响'));
  confirmed = true;
  const zeroDeleting = el('btnRemoveZeroCredits').onclick();
  assert.equal(el('btnRemoveZeroCredits').textContent, '删除中…');
  assert.equal(el('btnRemoveZeroCredits').disabled, true);
  assert.equal(el('btnRemoveDisabled').disabled, true);
  await el('btnRemoveZeroCredits').onclick();
  await el('btnRemoveDisabled').onclick();
  await load();
  assert.equal(el('btnRemoveZeroCredits').disabled, true, 'polling does not re-enable bulk deletion');
  assert.equal(deleteCalls, 1, 'both deletion actions share one busy guard');
  evalJS('lastPackages = {accounts: []}; lastPackagesAt = Date.now(); packagePageData = {}; packagePageAt = Date.now();');
  response = { ...response, accounts: [accounts[0]] };
  finishDelete({ removed: 526, failed: 0, errors: [] });
  await zeroDeleting;
  assert.equal(el('btnRemoveZeroCredits').disabled, true);
  assert.equal(el('btnRemoveZeroCredits').textContent, '删除零积分');
  assert.equal(el('btnRemoveDisabled').disabled, false);
  assert.ok(notices[2].text.includes('526'));
  assert.equal(evalJS('lastPackages === null && packagePageData === null'), true, 'deleted accounts invalidate package caches');

  response = { ...response, accounts: [zeroAccounts[0]] };
  await load();
  const zeroPartial = el('btnRemoveZeroCredits').onclick();
  finishDelete({ removed: 0, failed: 1, errors: [{ uid: 'zero-0', error: 'permission denied' }] });
  await zeroPartial;
  assert.equal(el('btnRemoveZeroCredits').disabled, false);
  assert.ok(notices[3].text.includes('已保留'));
  assert.ok(notices[3].text.includes('permission denied'));
  sandbox.fetch = async () => { throw new Error('network failure'); };
  await el('btnRemoveZeroCredits').onclick();
  assert.equal(el('btnRemoveZeroCredits').disabled, false, 'request failure restores the action');
  assert.equal(el('btnRemoveZeroCredits').textContent, '删除零积分');
  assert.ok(notices[4].text.includes('network failure'));
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
