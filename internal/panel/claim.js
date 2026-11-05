'use strict';
(() => {
  const el = id => document.getElementById(id);
  let token = '', generation = 0;
  // The claim page never reads or sends the administrator's locally stored key.
  let theme = matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  document.documentElement.dataset.theme = theme;
  el('claimTheme').onclick = () => { theme = theme === 'dark' ? 'light' : 'dark'; document.documentElement.dataset.theme = theme; };
  const amount = (units, kind) => Number(units || 0) / (kind === 'credit' ? 1000000 : 1);
  const fmt = n => Number(n).toLocaleString('zh-CN', { maximumFractionDigits: 6 });
  async function call(method, forToken = token) {
    const options = { method, credentials: 'omit', cache: 'no-store' };
    if (method === 'POST') { options.headers = { 'Content-Type': 'application/json' }; options.body = '{}'; }
    const response = await fetch('/panel/api/claim/' + encodeURIComponent(forToken), options);
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || '查询失败：HTTP ' + response.status);
    return data;
  }
  async function load(epoch = generation) {
    if (!/^[a-f0-9]{64}$/.test(token)) { el('claimProgress').textContent = '红包链接不完整，请使用发放者提供的完整链接。'; return; }
    try {
      const p = await call('GET');
      if (epoch !== generation) return;
      el('claimTitle').textContent = p.title;
      el('claimMode').textContent = p.mode === 'random' ? '拼手气红包' : '均分红包';
      el('claimAmount').textContent = fmt(p.total);
      el('claimUnit').textContent = (p.kind === 'credit' ? '积分' : 'Tokens') + ' · 总额度';
      el('claimProgress').textContent = '已领取 ' + p.claimed + ' / ' + p.shares + ' 份 · 剩余 ' + p.remaining + ' 份';
      el('claimExpiry').textContent = '有效期至 ' + new Date(p.expires_at).toLocaleString('zh-CN');
      el('claimModels').textContent = '可用模型：' + ((p.allowed_models || []).join('、') || '全部模型');
      el('btnClaim').disabled = false;
      el('btnClaim').textContent = p.remaining ? '领取一份' : '已领完 · 取回我的 Key';
    } catch (e) { if (epoch === generation) { el('claimProgress').textContent = e.message; el('btnClaim').disabled = true; } }
  }
  el('btnClaim').onclick = async () => {
    const epoch = generation;
    el('btnClaim').disabled = true; el('claimError').hidden = true;
    try {
      const c = await call('POST'), q = c.key.quota, unit = q.kind === 'credit' ? '积分' : 'Tokens';
      if (epoch !== generation) return;
      el('claimSecret').value = c.secret;
      el('claimBase').value = location.origin + '/v1';
      el('claimQuota').textContent = '本份额度 ' + fmt(amount(q.limit_units, q.kind)) + ' ' + unit + ' · 已用 ' + fmt(amount(q.used_units, q.kind)) + ' · ' + new Date(q.expires_at).toLocaleString('zh-CN') + ' 到期';
      el('claimResultNote').textContent = c.repeated ? '此 IP 已领取，以下是原来的 Key。' : '领取成功，请保存下方 Key。';
      el('claimResult').hidden = false;
      await load(epoch); if (epoch === generation) el('btnClaim').textContent = '已领取 · 取回 Key';
    } catch (e) { if (epoch === generation) { el('claimError').textContent = e.message; el('claimError').hidden = false; } }
    finally { if (epoch === generation) el('btnClaim').disabled = false; }
  };
  el('claimCopy').onclick = async () => {
    try {
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(el('claimSecret').value);
      else { el('claimSecret').focus(); el('claimSecret').select(); if (!document.execCommand('copy')) throw new Error('请手动复制'); }
      el('claimCopy').textContent = '已复制';
    } catch (e) { el('claimError').textContent = '复制失败，请选中 Key 手动复制。'; el('claimError').hidden = false; }
  };
  function changePacket() {
    generation++; token = location.hash.slice(1);
    el('claimSecret').value = ''; el('claimResult').hidden = true; el('claimError').hidden = true;
    el('claimTitle').textContent = '领取红包'; el('claimAmount').textContent = '—'; el('claimUnit').textContent = '额度';
    el('claimMode').textContent = '—'; el('claimModels').textContent = ''; el('claimExpiry').textContent = '';
    el('claimProgress').textContent = '正在查询红包…'; el('btnClaim').disabled = true; el('btnClaim').textContent = '领取一份';
    el('claimCopy').textContent = '复制 Key';
    load();
  }
  addEventListener('hashchange', changePacket);
  changePacket();
})();
