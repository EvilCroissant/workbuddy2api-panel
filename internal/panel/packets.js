'use strict';
let redPackets = [], packetSelected = null, packetLoadSeq = 0, packetDetailSeq = 0, packetReconcileKey = '';
let packetListState = { q: '', status: '', page: 1, size: 20 }, packetKeysState = { page: 1, size: 20 };
let packetRefreshTimer = null;
function packetAmount(units, kind) { return Number(units || 0) / (kind === 'credit' ? 1000000 : 1); }
function packetNumber(n) { return Number(n || 0).toLocaleString('zh-CN', { maximumFractionDigits: 6 }); }
function packetUnit(kind) { return kind === 'credit' ? '积分' : 'Tokens'; }
function packetQuotaLabel(q) {
  const used = packetAmount(q.used_units, q.kind), limit = packetAmount(q.limit_units, q.kind);
  return packetNumber(used) + ' / ' + packetNumber(limit) + ' ' + packetUnit(q.kind) +
    (!q.claimed ? ' · 待领取' : q.uncertain ? ' · 待核对' : q.used_units >= q.limit_units ? ' · 已用完' : '');
}
function packetStatus(p) {
  if (p.revoked_at) return 'revoked';
  if (Date.parse(p.expires_at) <= Date.now()) return 'expired';
  return Number(p.remaining) > 0 ? 'active' : 'claimed';
}
const PACKET_STATUSES = { active: '领取中', claimed: '已领完', expired: '已过期', revoked: '已收回' };
function packetClaimLink(p) { return location.origin + '/panel/claim#' + p.claim_token; }
function packetKeyState(k) {
  const q = k.quota;
  if (!k.enabled) return '已停用';
  if (Date.parse(q.expires_at) <= Date.now()) return '已过期';
  if (!q.claimed) return '待领取';
  if (q.uncertain) return '待核对';
  if (q.used_units >= q.limit_units) return '已用完';
  if (q.pending) return '使用中';
  return '可用';
}
function dismissPacketSecret() { $('packetSecretBox').hidden = true; $('packetSecret').value = ''; }
function dismissPacketView() {
  packetLoadSeq++; packetDetailSeq++;
  clearInterval(packetRefreshTimer); packetRefreshTimer = null;
  dismissPacketSecret(); $('packetReconcile').hidden = true;
}
async function loadPackets() {
  if (view !== 'redpackets') return;
  const seq = ++packetLoadSeq;
  try {
    const d = await api('red-packets');
    if (view !== 'redpackets' || seq !== packetLoadSeq) return;
    redPackets = Array.isArray(d.packets) ? d.packets : [];
    $('btnPacketOpen').disabled = !d.available || !d.admin_configured;
    $('packetsNote').textContent = !d.available ? '红包存储未装配。' : !d.admin_configured ? '请先在配置页设置管理员密钥。' : '领取链接按 IP 一人一份；同一 IP 再次打开可取回原 Key。';
    renderPackets();
    if (!packetRefreshTimer) packetRefreshTimer = setInterval(() => {
      if (view === 'redpackets' && !document.hidden) { loadPackets(); if (packetSelected) loadPacketDetail(packetSelected.id, true); }
    }, 15000);
  } catch (e) {
    if (view === 'redpackets' && seq === packetLoadSeq) { $('packetsNote').textContent = '读取失败：' + e.message; $('btnPacketOpen').disabled = true; }
  }
}
function renderPackets() {
  $('packetTotal').textContent = redPackets.length;
  $('packetActive').textContent = redPackets.filter(p => packetStatus(p) === 'active').length;
  $('packetClaimed').textContent = redPackets.reduce((n,p) => n + Number(p.claimed || 0), 0);
  const rows = redPackets.filter(p => (!packetListState.q || (p.title + ' ' + p.id).toLowerCase().includes(packetListState.q)) && (!packetListState.status || packetStatus(p) === packetListState.status));
  const paged = pageItems(rows, packetListState, 'packets', redPackets.length, '红包');
  $('packetsBody').innerHTML = paged.length ? paged.map(p => {
    const status = packetStatus(p);
    return '<tr><td><b>' + esc(p.title) + '</b><div class="hint">' + (p.mode === 'random' ? '拼手气' : '均分') + ' · ' + esc(fmtLocalDateTime(parseAPITime(p.created_at))) + '</div></td>' +
      '<td><span class="tag ' + (status === 'active' ? 'ok' : '') + '">' + PACKET_STATUSES[status] + '</span></td>' +
      '<td class="num">' + esc(packetNumber(p.total)) + ' ' + packetUnit(p.kind) + '<div class="hint">已用 ' + esc(packetNumber(p.used)) + '</div></td>' +
      '<td class="num">' + Number(p.claimed) + ' / ' + Number(p.shares) + '<div class="hint">剩余 ' + Number(p.remaining) + ' 份</div></td>' +
      '<td class="num">' + esc(fmtLocalDateTime(parseAPITime(p.expires_at))) + '</td>' +
      '<td class="acts"><button class="xs ghost" data-packet-id="' + esc(p.id) + '" data-packet-action="detail">详情 / 领取链接</button>' +
      (!p.revoked_at ? '<button class="xs ghost danger" data-packet-id="' + esc(p.id) + '" data-packet-action="revoke">收回</button>' : '') + '</td></tr>';
  }).join('') : '<tr><td colspan="6"><div class="empty"><div class="big">' + (redPackets.length ? '没有匹配的红包' : '还没有红包') + '</div>' + (redPackets.length ? '试试其他关键词或状态。' : '点击「发红包」创建领取链接，领取时分配额度并生成 Key。') + '</div></td></tr>';
}
async function loadPacketDetail(id, quiet = false) {
  const seq = ++packetDetailSeq;
  try {
    const d = await api('red-packets/' + encodeURIComponent(id));
    if (view !== 'redpackets' || seq !== packetDetailSeq) return;
    if (!quiet || !packetSelected || packetSelected.id !== id) { packetKeysState.page = 1; dismissPacketSecret(); $('packetReconcile').hidden = true; }
    packetSelected = d;
    renderPacketDetail();
  } catch (e) { if (view === 'redpackets' && seq === packetDetailSeq) toast('读取红包失败：' + e.message, 'err'); }
}
function renderPacketDetail() {
  const p = packetSelected; if (!p) return;
  $('packetDetail').hidden = false;
  $('packetDetailTitle').textContent = p.title;
  $('packetDetailMeta').textContent = PACKET_STATUSES[packetStatus(p)] + ' · 已领 ' + p.claimed + '/' + p.shares + ' 份 · 总额度 ' + packetNumber(p.total) + ' ' + packetUnit(p.kind) + ' · 已用 ' + packetNumber(p.used) + ' · 模型：' + ((p.allowed_models || []).join('、') || '全部模型');
  $('packetLink').value = packetClaimLink(p);
  $('btnPacketRevoke').disabled = !!p.revoked_at;
  $('btnPacketExport').disabled = !p.keys.length;
  const emptyText = p.revoked_at ? '红包已收回，关联 Key 已删除。' : Number(p.claimed) > 0 ? '已领取的 Key 已全部删除。' : packetStatus(p) === 'active' ? '暂无领取，领取时分配额度并生成 Key。' : '尚未有人领取，红包已结束。';
  const keys = pageItems(p.keys || [], packetKeysState, 'packetKeys', (p.keys || []).length, '密钥');
  $('packetKeysBody').innerHTML = keys.length ? keys.map(k => {
    const q = k.quota, label = packetKeyState(k);
    return '<tr><td><b>' + esc(k.name) + '</b><div class="hint"><code>' + esc(k.prefix) + '…</code></div></td><td><span class="tag ' + (label === '可用' ? 'ok' : label === '待核对' ? 'warn' : '') + '">' + label + '</span></td>' +
      '<td class="num">' + esc(packetNumber(packetAmount(q.limit_units,q.kind))) + ' ' + packetUnit(q.kind) + '</td>' +
      '<td class="num">' + esc(packetNumber(packetAmount(q.used_units,q.kind))) + '</td>' +
      '<td class="num">' + esc(packetNumber(Math.max(0,packetAmount(q.limit_units-q.used_units,q.kind)))) + '</td>' +
      '<td class="acts"><button class="xs ghost" data-packet-key="' + esc(k.id) + '" data-packet-key-action="reveal">查看 Key</button>' +
      (q.uncertain ? '<button class="xs ghost" data-packet-key="' + esc(k.id) + '" data-packet-key-action="reconcile">核对用量</button>' : '') + '</td></tr>';
  }).join('') : '<tr><td colspan="6"><div class="empty">' + emptyText + '</div></td></tr>';
}
function updatePacketPreview() {
  const kind = $('packetKind').value, tokens = kind === 'tokens';
  $('packetAmountLabel').textContent = tokens ? '总 Token 数' : '总积分';
  $('packetAmount').min = tokens ? '1' : '0.000001'; $('packetAmount').step = tokens ? '1' : '0.000001';
  if (tokens) setKeyModelMode('packet', 'selected');
  const total = Number($('packetAmount').value), shares = Number($('packetShares').value);
  $('packetPreview').textContent = total > 0 && shares > 0 ? shares + ' 份 · ' + ($('packetMode').value === 'equal' ? '每份约 ' : '平均每份 ') + packetNumber(total/shares) + ' ' + packetUnit(kind) : '填写总额与份数';
}
$('btnPacketOpen').onclick = () => { $('packetComposer').hidden = false; $('packetCreateError').hidden = true; updatePacketPreview(); loadKeyModelCatalog(); $('packetTitle').focus(); };
$('btnPacketCancel').onclick = () => { $('packetComposer').hidden = true; };
for (const id of ['packetKind','packetMode','packetAmount','packetShares']) $(id).addEventListener('input', updatePacketPreview);
$('packetForm').onsubmit = async ev => {
  ev.preventDefault(); $('packetCreateError').hidden = true;
  const kind = $('packetKind').value, models = keyModelPayload('packet');
  if ((kind === 'tokens' && !models.length) || !keyModelSelectionValid('packet')) { $('packetCreateError').textContent = '请至少选择一个模型；积分红包可切换为全部模型。'; $('packetCreateError').hidden = false; return; }
  const button = $('btnPacketCreate'); button.disabled = true;
  try {
    const data = await api('red-packets', { method: 'POST', body: JSON.stringify({ title: $('packetTitle').value.trim(), kind, mode: $('packetMode').value, total: Number($('packetAmount').value), shares: Number($('packetShares').value), days: Number($('packetDays').value), allowed_models: models }) });
    if (view !== 'redpackets') return;
    $('packetComposer').hidden = true; packetSelected = data; packetKeysState.page = 1; packetListState.page = 1; dismissPacketSecret();
    renderPacketDetail(); await loadPackets(); toast('红包已生成，复制领取链接即可分享', 'ok');
  } catch (e) { $('packetCreateError').textContent = e.message; $('packetCreateError').hidden = false; }
  finally { button.disabled = false; }
};
async function revokePacket(id) {
  const p = redPackets.find(p => p.id === id) || packetSelected;
  if (!confirm('收回「' + (p ? p.title : '红包') + '」？此批全部 Key 将被删除，已用额度保留。')) return;
  await api('red-packets/' + encodeURIComponent(id) + '/revoke', { method:'POST' });
  dismissPacketSecret(); await loadPackets();
  if (packetSelected && packetSelected.id === id) await loadPacketDetail(id);
  toast('红包已收回', 'ok');
}
$('packetsBody').onclick = async ev => {
  const b = ev.target.closest('button[data-packet-id]'); if (!b || b.disabled) return;
  b.disabled = true;
  try { if (b.dataset.packetAction === 'detail') await loadPacketDetail(b.dataset.packetId); else await revokePacket(b.dataset.packetId); }
  catch (e) { toast(e.message, 'err'); } finally { b.disabled = false; }
};
$('btnPacketDetailClose').onclick = () => { packetDetailSeq++; packetSelected = null; $('packetDetail').hidden = true; dismissPacketSecret(); $('packetReconcile').hidden = true; };
$('btnPacketLinkCopy').onclick = async () => { try { await copyText($('packetLink').value); toast('领取链接已复制', 'ok'); } catch (e) { toast('复制失败：' + e.message, 'err'); } };
$('btnPacketRevoke').onclick = async () => { if (packetSelected) try { await revokePacket(packetSelected.id); } catch (e) { toast(e.message, 'err'); } };
$('btnPacketExport').onclick = async () => {
  const p = packetSelected; if (!p) return; const button = $('btnPacketExport'); button.disabled = true;
  try {
    const data = await api('red-packets/' + encodeURIComponent(p.id) + '/export');
    const blob = new Blob([JSON.stringify({ title:p.title, base_url:location.origin+'/v1', keys:data.keys }, null, 2)], { type:'application/json' });
    const url = URL.createObjectURL(blob), a = document.createElement('a'); a.href = url; a.download = 'red-packet-' + p.id + '.json'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch (e) { toast('导出失败：' + e.message, 'err'); } finally { button.disabled = false; }
};
$('packetKeysBody').onclick = async ev => {
  const b = ev.target.closest('button[data-packet-key]'); if (!b || b.disabled || !packetSelected) return;
  const k = packetSelected.keys.find(k => k.id === b.dataset.packetKey); if (!k) return;
  if (b.dataset.packetKeyAction === 'reconcile') {
    packetReconcileKey = k.id; $('packetReconcile').hidden = false; $('packetReconcileUsed').value = packetAmount(k.quota.used_units,k.quota.kind);
    $('packetReconcileUsed').step = k.quota.kind === 'tokens' ? '1' : '0.000001';
    $('packetReconcileLabel').textContent = k.name + ' · 核对后的累计已用' + packetUnit(k.quota.kind); $('packetReconcileUsed').focus(); return;
  }
  const seq = packetDetailSeq; b.disabled = true;
  try { const data = await api('keys/' + encodeURIComponent(k.id) + '/secret'); if (view !== 'redpackets' || seq !== packetDetailSeq) return; $('packetSecret').value = data.secret; $('packetSecretBox').hidden = false; }
  catch (e) { toast('读取失败：' + e.message, 'err'); } finally { b.disabled = false; }
};
$('packetReconcileForm').onsubmit = async ev => {
  ev.preventDefault(); if (!packetSelected) return;
  const id = packetSelected.id, key = packetReconcileKey; const button = $('btnPacketReconcileSave'); button.disabled = true;
  try { await api('red-packets/' + encodeURIComponent(id) + '/reconcile', { method:'POST', body:JSON.stringify({ key_id:key, used:Number($('packetReconcileUsed').value) }) }); $('packetReconcile').hidden = true; await loadPacketDetail(id); await loadPackets(); toast('用量已核对', 'ok'); }
  catch (e) { toast(e.message, 'err'); } finally { button.disabled = false; }
};
$('btnPacketReconcileCancel').onclick = () => { $('packetReconcile').hidden = true; };
$('btnPacketSecretCopy').onclick = async () => { try { await copyText($('packetSecret').value); toast('Key 已复制', 'ok'); } catch (e) { toast('复制失败：' + e.message, 'err'); } };
$('btnPacketSecretClose').onclick = dismissPacketSecret;
$('btnPacketRefresh').onclick = () => { loadPackets(); if (packetSelected) loadPacketDetail(packetSelected.id, true); };
$('packetsSearch').oninput = () => { packetListState.q = $('packetsSearch').value.trim().toLowerCase(); packetListState.page = 1; renderPackets(); };
$('packetsStatus').onchange = () => { packetListState.status = $('packetsStatus').value; packetListState.page = 1; renderPackets(); };
for (const [prefix, state, render] of [['packets',packetListState,renderPackets], ['packetKeys',packetKeysState,renderPacketDetail]]) {
  $(prefix+'Prev').onclick = () => { state.page--; render(); };
  $(prefix+'Next').onclick = () => { state.page++; render(); };
  $(prefix+'PageSize').onchange = () => { state.size = Number($(prefix+'PageSize').value); state.page = 1; render(); };
}
if (view === 'redpackets') loadPackets();
