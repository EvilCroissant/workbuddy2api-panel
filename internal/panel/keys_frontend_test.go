package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDistributedKeyFrontendLifecycle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	const script = `const fs = require('fs'), vm = require('vm'), assert = require('assert/strict');
const elements = new Map();
function element() { return { innerHTML: '', textContent: '', hidden: false, value: '', dataset: {}, style: {},
  get children() { return this.innerHTML ? [{}] : []; }, classList: {add(){},remove(){},toggle(){}},
  addEventListener(){}, setAttribute(){}, querySelector(){return null;}, querySelectorAll(){return [];},
  appendChild(){}, focus(){}, select(){} }; }
for (const m of fs.readFileSync('index.html', 'utf8').matchAll(/\bid="([^"]+)"/g)) elements.set(m[1], element());
const el = id => elements.get(id);
el('keyCreated').hidden = true;
const intervals = [];
const sandbox = { location:{hash:'#accounts'}, history:{replaceState(){}},
  localStorage:{getItem(){return 'admin';},setItem(){}},
  document:{hidden:false,getElementById:el,querySelectorAll:()=>[],querySelector:()=>null,documentElement:element(),addEventListener(){},createElement:element},
  fetch:()=>new Promise(()=>{}),addEventListener(){},matchMedia:()=>({matches:false}),
  setTimeout(){},clearTimeout(){},setInterval(fn,ms){intervals.push(ms);return intervals.length;},clearInterval(){},console,URLSearchParams,
  confirm:()=>true,navigator:{clipboard:{async writeText(v){assert.ok(v.startsWith('sk-wb-'));}}} };
sandbox.window = sandbox; vm.createContext(sandbox);
vm.runInContext(fs.readFileSync('app.js','utf8'), sandbox);
let keys = Array.from({length:45}, (_,i)=>({id:'id-'+i,name:i===0?'<img src=x onerror=alert(1)>':'Person '+i,
  prefix:'sk-wb-'+String(i).padStart(8,'0'),enabled:i%2===0,created_at:'2026-11-03T10:00:00Z'}));
let available=true,admin=true,failed=false,posts=0,usageAvailable=true;
const usage = {
  'id-0':{requests:12,errors:2,success_rate:83.3,prompt_tokens:1000,completion_tokens:400,
    total_tokens:1400,token_requests:10,credit:3.5,credit_requests:8,avg_latency_ms:420,last_used_at:'2026-11-03T10:30:00Z'}
};
sandbox.fetch = async (url, opts) => {
  if(failed) return {ok:false,status:500,json:async()=>({error:'disk failed'})};
  let data={}; const method=opts.method||'GET';
  if(url.startsWith('/panel/api/keys?hours=') && method==='GET') data={keys,available,admin_configured:admin,usage_available:usageAvailable,usage};
  else if(url==='/panel/api/models') data={models:[{id:'cn:glm-5.2',name:'GLM'},{id:'global:other',name:'Other'}]};
  else if(method==='GET' && url.endsWith('/secret')) data={secret:'sk-wb-new-secret'};
  else if(method==='POST' && url.endsWith('/rotate')) {
    const id=url.split('/').at(-2);keys=keys.map(k=>k.id===id?{...k,secret_available:true}:k);data={secret:'sk-wb-rotated-secret'};
  }
  else if(method==='POST') {posts++; const k={id:'created',name:JSON.parse(opts.body).name,allowed_models:JSON.parse(opts.body).allowed_models,secret_available:true,prefix:'sk-wb-new',enabled:true,created_at:'2026-11-03T11:00:00Z'};
    keys=[k,...keys];data={key:k,secret:'sk-wb-new-secret'};}
  else if(method==='PATCH') keys=keys.map(k=>k.id===url.split('/').pop()?{...k,...JSON.parse(opts.body)}:k);
  else if(method==='DELETE') keys=keys.filter(k=>k.id!==url.split('/').pop());
  return {ok:true,status:method==='POST'?201:200,json:async()=>JSON.parse(JSON.stringify(data))};
};
const run = code => vm.runInContext(code,sandbox);
const click = (id,action) => el('keysBody').onclick({target:{closest:()=>({dataset:{keyId:id,keyAction:action},disabled:false})}});
const keyRow = id => {
  const m=el('keysBody').innerHTML.match(new RegExp('<tr class="key-row" data-key-id="'+id+'"[\\s\\S]*?</tr>'));
  assert.ok(m,'row '+id); return m[0];
};
(async()=>{
  run("go('keys')");
  await run('loadKeys()');
  assert.ok(intervals.includes(15000),'keys view refreshes about every 15 seconds');
  assert.equal(el('btnKeyCreate').disabled,false);
  assert.equal(el('keyTotal').textContent,45);
  assert.equal(el('keyEnabled').textContent,23);
  assert.equal(el('keyDisabled').textContent,22);
  el('btnKeyOpen').onclick();assert.equal(el('keyComposer').hidden,false);
  el('btnKeyCancel').onclick();assert.equal(el('keyComposer').hidden,true);
  assert.equal((el('keysBody').innerHTML.match(/class="key-row"/g)||[]).length,20);
  assert.equal(el('keysPageNumber').textContent,'1 / 3');
  assert.ok(el('keysBody').innerHTML.includes('&lt;img'));
  assert.ok(!el('keysBody').innerHTML.includes('<img'));
  assert.ok(!el('keysBody').innerHTML.includes('NaN'));
  assert.ok(el('keysBody').innerHTML.includes('83.3%'));
  assert.ok(el('keysBody').innerHTML.includes('1.4k'));
  el('keysNext').onclick();el('keysNext').onclick();
  assert.equal((el('keysBody').innerHTML.match(/class="key-row"/g)||[]).length,5);
  el('keysSearch').value='Person 44';el('keysSearch').oninput();
  assert.ok(el('keysBody').innerHTML.includes('Person 44'));
  assert.equal(el('keysPageNumber').textContent,'1 / 1');
  el('keysSearch').value='';el('keysSearch').oninput();
  el('keysStatus').value='disabled';el('keysStatus').onchange();
  assert.equal(run('keyListState.page'),1);
  assert.ok(el('keysCount').textContent.includes('22 / 共 45'));
  el('keysStatus').value='';el('keysStatus').onchange();
  el('keyName').value='New person';
  await run('loadKeyModelCatalog()');
  el('keyCreateModelMode').onclick({target:{closest:()=>({dataset:{keyModelMode:'selected'}})}});
  await el('keyCreateForm').onsubmit({preventDefault(){}});
  assert.equal(posts,0,'specified mode requires at least one model');
  el('keyCreateModelList').onchange({target:{closest:()=>({dataset:{keyModelId:'cn:glm-5.2'},checked:true})}});
  el('keyCreateModelList').onchange({target:{closest:()=>({dataset:{keyModelId:'global:other'},checked:true})}});
  assert.equal(el('keyAllowedModels').value,'cn:glm-5.2\nglobal:other');
  assert.ok(el('keyCreateModelList').innerHTML.includes('is-cn'));
  assert.ok(el('keyCreateModelList').innerHTML.includes('is-global'));
  el('keyCreateModelSearch').value='other';el('keyCreateModelSearch').oninput();
  assert.ok(el('keyCreateModelList').innerHTML.includes('global:other'));
  assert.ok(!el('keyCreateModelList').innerHTML.includes('cn:glm-5.2'));
  el('keyCreateModelSearch').value='';el('keyCreateModelSearch').oninput();
  el('keyCreateManualModel').value='manual-model';el('keyCreateAddManual').onclick();
  assert.ok(el('keyAllowedModels').value.includes('cn:manual-model'));
  run("setKeyModelChecked('create','cn:manual-model',false)");
  await el('keyCreateForm').onsubmit({preventDefault(){}});
  assert.equal(posts,1);assert.equal(el('keySecret').value,'sk-wb-new-secret');assert.equal(el('keyCreated').hidden,false);
  assert.deepEqual(keys[0].allowed_models,['cn:glm-5.2','global:other']);
  assert.equal(el('keyAllowedModels').value,'');
  assert.match(keyRow('created'),/允许 2 个模型/);
  assert.match(keyRow('created'),/data-key-action="reveal"/);
  assert.ok(!el('keysBody').innerHTML.includes('sk-wb-new-secret'));
  await el('keyCreateForm').onsubmit({preventDefault(){}});assert.equal(posts,1,'unacknowledged secret is preserved');
  await el('btnKeyCopy').onclick();
  el('btnKeyDismiss').onclick();assert.equal(el('keySecret').value,'');assert.equal(el('keyCreated').hidden,true);
  await click('created','reveal');assert.equal(el('keySecret').value,'sk-wb-new-secret');
  assert.equal(el('keyCreated').hidden,false);el('btnKeyDismiss').onclick();
  keys[0].allowed_models=['cn:glm-5.2','global:other','global:unlisted'];await run('loadKeys()');
  await click('created','models');assert.equal(el('keyModelEditor').hidden,false);
  assert.equal(el('keyEditAllowedModels').value,'cn:glm-5.2\nglobal:other\nglobal:unlisted');
  assert.ok(el('keyEditModelList').innerHTML.includes('global:unlisted'));
  el('keyEditModelList').onchange({target:{closest:()=>({dataset:{keyModelId:'cn:glm-5.2'},checked:false})}});
  el('keyEditModelList').onchange({target:{closest:()=>({dataset:{keyModelId:'global:unlisted'},checked:false})}});
  assert.equal(el('keyEditAllowedModels').value,'global:other');
  await el('keyModelForm').onsubmit({preventDefault(){}});
  assert.deepEqual(keys[0].allowed_models,['global:other']);assert.equal(el('keyModelEditor').hidden,true);
  await click('created','models');run("setKeyModelMode('edit','all')");await el('keyModelForm').onsubmit({preventDefault(){}});
  assert.deepEqual(keys[0].allowed_models,[]);assert.match(keyRow('created'),/全部模型/);
  await click('id-0','rotate');assert.equal(keys.find(k=>k.id==='id-0').secret_available,true);
  assert.equal(el('keySecret').value,'sk-wb-rotated-secret');el('btnKeyDismiss').onclick();
  await click('created','disable');assert.equal(keys[0].enabled,false);
  await click('created','enable');assert.equal(keys[0].enabled,true);
  await click('created','delete');assert.equal(keys.length,45);
  admin=false;await run('loadKeys()');assert.equal(el('btnKeyCreate').disabled,true);
  admin=true;available=false;await run('loadKeys()');assert.equal(el('btnKeyCreate').disabled,true);
  available=true;usageAvailable=false;await run('loadKeys()');
  assert.match(keyRow('id-0'),/data-l="请求">—<\/td>/);
  assert.match(keyRow('id-0'),/data-l="成功率">—<\/td>/);
  assert.match(keyRow('id-0'),/data-l="Tokens"[^>]*>—<\/td>/);
  usageAvailable=true;
  failed=true;await run('loadKeys()');assert.equal(el('btnKeyCreate').disabled,true);assert.ok(el('keysNote').textContent.includes('disk failed'));
  failed=false;available=true;await run('loadKeys()');assert.equal(el('btnKeyCreate').disabled,false);
  el('keySecret').value='secret';el('keyCreated').hidden=false;run("go('accounts')");
  assert.equal(el('keySecret').value,'');assert.equal(el('keyCreated').hidden,true);
  console.log('KEY LIFECYCLE OK');
})().catch(e=>{console.error(e);process.exitCode=1;});`
	path := filepath.Join(t.TempDir(), "keys.cjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, path).CombinedOutput(); err != nil {
		t.Fatalf("key frontend: %v\n%s", err, out)
	}
}

func TestDistributedKeyUsageFrontendRenderingAndStaleResponses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	const script = `const fs = require('fs'), vm = require('vm'), assert = require('assert/strict');
const elements = new Map();
function element() { return { innerHTML: '', textContent: '', hidden: false, value: '', dataset: {}, style: {},
  get children() { return this.innerHTML ? [{}] : []; }, classList: {add(){},remove(){},toggle(){}},
  addEventListener(){}, setAttribute(){}, querySelector(){return null;}, querySelectorAll(){return [];},
  appendChild(){}, focus(){}, select(){} }; }
for (const m of fs.readFileSync('index.html', 'utf8').matchAll(/\bid="([^"]+)"/g)) elements.set(m[1], element());
const el = id => elements.get(id);
el('keyCreated').hidden = true;
const intervals = [];
const keys = [
  {id:'alice',name:'Alice',prefix:'sk-wb-alice',enabled:true,created_at:'2026-11-03T08:00:00Z'},
  {id:'bob',name:'Bob',prefix:'sk-wb-bob',enabled:true,created_at:'2026-11-03T09:00:00Z'},
  {id:'unused',name:'Unused',prefix:'sk-wb-unused',enabled:false,created_at:'2026-11-03T10:00:00Z'}
];
const stats = requests => ({requests,errors:requests>1?1:0,success_rate:requests>1?66.7:100,
  prompt_tokens:requests*80,completion_tokens:requests*60,total_tokens:requests*140,
  token_requests:requests,credit:requests*.5,credit_requests:requests,avg_latency_ms:320,
  last_used_at:'2026-11-03T10:30:00Z'});
const listFor = hours => ({
  keys,available:true,admin_configured:true,usage_available:true,
  usage:{alice:stats(Number(hours)===0?3:Number(hours)===24?4:Number(hours)===168?7:9),bob:stats(1)}
});
const detailFor = (id, hours) => {
  const requests = id==='alice' ? Number(hours)||3 : 1;
  const model = id==='alice' ? 'glm-5.2-'+hours : 'deepseek-'+hours;
  return {key:keys.find(k=>k.id===id),available:true,usage:{
    totals:stats(requests),
    by_model:[{model,...stats(requests)}],
    series:[{time:'2026-11-03T09:00:00Z',...stats(requests)}],
    recent:[{time:'2026-11-03T10:30:00Z',model,path:'/v1/chat/completions',status:200,ok:true,
      outcome:'success',duration_ms:280,attempts:1,prompt_tokens:80,completion_tokens:60,total_tokens:140,
      token_known:true,credit:.5,credit_known:true,request_id:'req-'+id+'-'+hours,key_id:id}],
    hours:Number(hours),from:'2026-11-03T00:00:00Z',to:'2026-11-03T11:00:00Z',granularity:'hour'
  }};
};
const response = data => ({ok:true,status:200,json:async()=>JSON.parse(JSON.stringify(data))});
let holdList=false,holdDetail=false;
const heldLists=[],heldDetails=[];
const sandbox = { location:{hash:'#accounts'}, history:{replaceState(){}},
  localStorage:{getItem(){return 'admin';},setItem(){}},
  document:{hidden:false,getElementById:el,querySelectorAll:()=>[],querySelector:()=>null,documentElement:element(),addEventListener(){},createElement:element},
  addEventListener(){},matchMedia:()=>({matches:false}),setTimeout(){},clearTimeout(){},
  setInterval(fn,ms){intervals.push(ms);return intervals.length;},clearInterval(){},console,URLSearchParams,
  confirm:()=>true,navigator:{clipboard:{async writeText(){}}} };
sandbox.fetch = (url,opts={}) => {
  const method=opts.method||'GET';
  if(method==='GET' && url.startsWith('/panel/api/keys?hours=')) {
    const hours=url.split('hours=')[1];
    if(holdList) return new Promise(resolve=>heldLists.push({hours,resolve}));
    return Promise.resolve(response(listFor(hours)));
  }
  if(method==='GET' && /^\/panel\/api\/keys\/[^/]+\/usage\?hours=/.test(url)) {
    const m=url.match(/^\/panel\/api\/keys\/([^/]+)\/usage\?hours=(.+)$/);
    if(holdDetail) return new Promise(resolve=>heldDetails.push({id:decodeURIComponent(m[1]),hours:m[2],resolve}));
    return Promise.resolve(response(detailFor(decodeURIComponent(m[1]),m[2])));
  }
  return new Promise(()=>{});
};
sandbox.window=sandbox;vm.createContext(sandbox);
vm.runInContext(fs.readFileSync('app.js','utf8'),sandbox);
const run = code => vm.runInContext(code,sandbox);
const keyRow = id => {
  const m=el('keysBody').innerHTML.match(new RegExp('<tr class="key-row" data-key-id="'+id+'"[\\s\\S]*?</tr>'));
  assert.ok(m,'row '+id); return m[0];
};
(async()=>{
  run("go('keys')");
  await run('loadKeys()');
  assert.ok(intervals.includes(15000));
  assert.match(keyRow('alice'),/data-l="请求">3<\/td>/);
  assert.match(keyRow('alice'),/data-l="成功率">66\.7%<\/td>/);
  assert.match(keyRow('alice'),/data-l="Tokens"[^>]*>420<\/td>/);
  assert.match(keyRow('unused'),/data-l="请求">0<\/td>/);
  assert.match(keyRow('unused'),/data-l="成功率">—<\/td>/);
  assert.match(keyRow('unused'),/data-l="Tokens"[^>]*>0<\/td>/);

  await run("toggleKeyUsage('alice')");
  assert.ok(el('keysBody').innerHTML.includes('模型分布'));
  assert.ok(el('keysBody').innerHTML.includes('glm-5.2-0'));
  assert.ok(el('keysBody').innerHTML.includes('req-alice-0'));

  el('keySecret').value='sk-wb-keep-me';el('keyCreated').hidden=false;
  await run("setKeyRange('24')");
  assert.equal(el('keySecret').value,'sk-wb-keep-me','range refresh preserves the one-time secret');
  assert.equal(el('keyCreated').hidden,false);
  assert.match(keyRow('alice'),/data-l="请求">4<\/td>/);
  assert.ok(el('keysBody').innerHTML.includes('glm-5.2-24'));
  assert.ok(el('keysBody').innerHTML.includes('<span>24h</span>'));

  await run("toggleKeyUsage('alice')");
  holdList=true;
  const staleRange=run('loadKeys()');
  const freshRange=run("setKeyRange('168')");
  assert.equal(heldLists.length,2);
  heldLists[1].resolve(response(listFor('168')));
  await freshRange;
  heldLists[0].resolve(response({...listFor('24'),usage:{alice:stats(999)}}));
  await staleRange;
  assert.match(keyRow('alice'),/data-l="请求">7<\/td>/);
  assert.ok(!keyRow('alice').includes('999'));
  holdList=false;

  holdDetail=true;
  const staleKey=run("toggleKeyUsage('alice')");
  const freshKey=run("toggleKeyUsage('bob')");
  assert.equal(heldDetails.length,2);
  heldDetails[1].resolve(response(detailFor('bob','168')));
  await freshKey;
  assert.ok(el('keysBody').innerHTML.includes('deepseek-168'));
  assert.ok(!el('keysBody').innerHTML.includes('glm-5.2-168'));
  heldDetails[0].resolve(response(detailFor('alice','168')));
  await staleKey;
  assert.ok(el('keysBody').innerHTML.includes('deepseek-168'));
  assert.ok(!el('keysBody').innerHTML.includes('glm-5.2-168'));
  holdDetail=false;

  await run("toggleKeyUsage('bob')");
  holdList=true;
  const staleLeave=run('loadKeys()');
  run("go('accounts')");
  assert.equal(heldLists.length,3);
  heldLists[2].resolve(response({...listFor('168'),keys:[{id:'stale',name:'STALE',prefix:'sk-wb-stale',enabled:true,created_at:'2026-11-03T10:00:00Z'}]}));
  await staleLeave;
  assert.equal(run('distributedKeys.length'),3);
  assert.ok(!el('keysBody').innerHTML.includes('STALE'));
  const sparse=JSON.parse(run("JSON.stringify(compressKeySeries([\n    {time:'2026-10-01T00:00:00Z',requests:1,total_tokens:20,token_requests:1,avg_latency_ms:100},\n    {time:'2026-10-04T00:00:00Z',requests:2,total_tokens:60,token_requests:2,avg_latency_ms:400}\n  ],30,{granularity:'day'}))"));
  assert.equal(sparse.length,4,'missing days keep their position on the time axis');
  assert.deepEqual(sparse.map(p=>p.requests),[1,0,0,2]);
  assert.equal(sparse.reduce((n,p)=>n+p.total_tokens,0),80);
  const hourly=JSON.parse(run("JSON.stringify(compressKeySeries([\n    {time:'2026-11-03T12:00:00Z',requests:2,total_tokens:40,token_requests:2}\n  ],30,{granularity:'hour',from:'2026-11-03T00:00:00Z',to:'2026-11-03T23:30:00Z'}))"));
  assert.equal(hourly.length,24,'full selected range includes silent hours');
  assert.equal(hourly[12].requests,2);assert.equal(hourly[0].requests,0);
  const decades=JSON.parse(run("JSON.stringify(compressKeySeries([\n    {time:'2000-01-01T00:00:00Z',requests:1},\n    {time:'2099-01-01T00:00:00Z',requests:1}\n  ],30,{granularity:'day'}))"));
  assert.ok(decades.length<=30,'long histories allocate bounded chart bins');
  assert.equal(decades.reduce((n,p)=>n+p.requests,0),2);
  assert.ok(run("keyTrendHtml({series:[{time:'2026-10-01T00:00:00Z',requests:1},{time:'2026-10-04T00:00:00Z',requests:1}],granularity:'day'}).includes('无请求')"));
  assert.equal(run("compressKeySeries([{time:'invalid'}],30).length"),0);
  console.log('KEY USAGE FRONTEND OK');
})().catch(e=>{console.error(e);process.exitCode=1;});`
	path := filepath.Join(t.TempDir(), "keys-usage.cjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, path).CombinedOutput(); err != nil {
		t.Fatalf("key usage frontend: %v\n%s", err, out)
	}
}
