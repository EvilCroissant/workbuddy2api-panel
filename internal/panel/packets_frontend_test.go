package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPacketFrontendLifecycle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `const fs=require('fs'),vm=require('vm'),assert=require('assert/strict');
const elements=new Map();
function element(){return {innerHTML:'',textContent:'',hidden:false,value:'',dataset:{},style:{},get children(){return this.innerHTML?[{}]:[];},classList:{add(){},remove(){},toggle(){}},addEventListener(){},setAttribute(){},querySelector(){return null;},querySelectorAll(){return [];},appendChild(){},focus(){},select(){}};}
for(const m of fs.readFileSync('index.html','utf8').matchAll(/\bid="([^"]+)"/g))elements.set(m[1],element());
const el=id=>elements.get(id),intervals=[];
const ctx={location:{hash:'#accounts',origin:'http://test.local'},history:{replaceState(){}},localStorage:{getItem(){return 'admin';},setItem(){}},document:{hidden:false,getElementById:el,querySelectorAll:()=>[],querySelector:()=>null,documentElement:element(),addEventListener(){},createElement:element},fetch:()=>new Promise(()=>{}),addEventListener(){},matchMedia:()=>({matches:false}),setTimeout(){},clearTimeout(){},setInterval(fn,ms){intervals.push(ms);return intervals.length;},clearInterval(){},console,URLSearchParams,confirm:()=>true,navigator:{}};
ctx.window=ctx;vm.createContext(ctx);vm.runInContext(fs.readFileSync('app.js','utf8'),ctx);vm.runInContext(fs.readFileSync('packets.js','utf8'),ctx);
let packets=Array.from({length:25},(_,i)=>({id:'p'+i,title:i===0?'<img src=x onerror=alert(1)>':'Packet '+i,kind:'tokens',mode:'equal',total:100,used:0,shares:2,claimed:0,remaining:2,created_at:'2026-11-05T00:00:00Z',expires_at:'2099-11-05T00:00:00Z',allowed_models:['cn:glm-5.2']}));
let posts=0,failed=false;
const detail=p=>({...p,claim_token:'a'.repeat(64),keys:Array.from({length:25},(_,i)=>({id:'k'+i,name:'Key '+i,prefix:'sk-wb-test',enabled:true,quota:{kind:'tokens',limit_units:50,used_units:10,claimed:true,pending:false,uncertain:i===0,expires_at:p.expires_at}}))});
ctx.fetch=async(url,opts)=>{const method=opts.method||'GET';let data={};
if(failed)return {ok:false,status:500,json:async()=>({error:'write failed'})};
if(url==='/panel/api/red-packets'&&method==='GET')data={available:true,admin_configured:true,packets};
else if(url==='/panel/api/red-packets'&&method==='POST'){posts++;const input=JSON.parse(opts.body);assert.deepEqual(input.allowed_models,['cn:glm-5.2']);data=detail({...packets[0],id:'created',title:input.title});packets=[data,...packets];}
else if(url==='/panel/api/models')data={models:[{id:'cn:glm-5.2',name:'GLM'}]};
else if(url.endsWith('/revoke'))packets=packets.map(p=>p.id==='p0'?{...p,revoked_at:'2026-11-05T01:00:00Z',remaining:0}:p);
else if(url.endsWith('/reconcile')){const input=JSON.parse(opts.body);assert.equal(input.key_id,'k0');assert.equal(input.used,15);}
else if(url.includes('/red-packets/')){const p=packets.find(p=>url.endsWith('/'+p.id));data=detail(p);}
else if(url.endsWith('/secret'))data={secret:'sk-wb-revealed'};
return {ok:true,status:200,json:async()=>data};};
const run=code=>vm.runInContext(code,ctx);
(async()=>{
run("go('redpackets')");await run('loadPackets()');assert.equal(el('packetTotal').textContent,25);assert.ok(el('packetsBody').innerHTML.includes('&lt;img'));assert.ok(!el('packetsBody').innerHTML.includes('<img'));assert.equal((el('packetsBody').innerHTML.match(/data-packet-action="detail"/g)||[]).length,20);
el('packetsNext').onclick();assert.equal(el('packetsPageNumber').textContent,'2 / 2');el('packetsSearch').value='Packet 24';el('packetsSearch').oninput();assert.ok(el('packetsBody').innerHTML.includes('Packet 24'));assert.equal(el('packetsPageNumber').textContent,'1 / 1');
await run("loadPacketDetail('p0')");assert.equal(el('packetLink').value,'http://test.local/panel/claim#'+'a'.repeat(64));assert.ok(el('packetKeysBody').innerHTML.includes('待核对'));assert.equal((el('packetKeysBody').innerHTML.match(/data-packet-key-action="reveal"/g)||[]).length,20);el('packetKeysNext').onclick();assert.equal(el('packetKeysPageNumber').textContent,'2 / 2');
el('packetTitle').value='New';el('packetKind').value='tokens';el('packetMode').value='equal';el('packetAmount').value='100';el('packetShares').value='2';el('packetDays').value='7';
await el('packetForm').onsubmit({preventDefault(){}});assert.equal(posts,0);assert.ok(el('packetCreateError').textContent.includes('至少选择'));
run("setKeyModelValue('packet',['cn:glm-5.2'])");await el('packetForm').onsubmit({preventDefault(){}});assert.equal(posts,1);assert.equal(el('packetComposer').hidden,true);assert.equal(el('packetDetailTitle').textContent,'New');
await run("loadPacketDetail('p0')");await el('packetKeysBody').onclick({target:{closest:()=>({dataset:{packetKey:'k0',packetKeyAction:'reconcile'}})}});assert.equal(el('packetReconcile').hidden,false);el('packetReconcileUsed').value='15';await el('packetReconcileForm').onsubmit({preventDefault(){}});assert.equal(el('packetReconcile').hidden,true);
await run("revokePacket('p0')");assert.ok(el('packetsBody').innerHTML.includes('已收回')||run("redPackets.find(p=>p.id==='p0').revoked_at"));
el('packetSecret').value='sk-wb-private';el('packetSecretBox').hidden=false;run("go('accounts')");assert.equal(el('packetSecret').value,'');assert.equal(el('packetSecretBox').hidden,true);
run("go('redpackets')");failed=true;await run('loadPackets()');assert.ok(el('packetsNote').textContent.includes('读取失败'));assert.equal(el('btnPacketOpen').disabled,true);
console.log('packet lifecycle OK');
})().catch(err=>{console.error(err);process.exit(1)});`
	file := filepath.Join(t.TempDir(), "packets.cjs")
	if err = os.WriteFile(file, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("packet frontend: %v\n%s", err, out)
	}
}

func TestPublicClaimFrontendRecovery(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `const fs=require('fs'),vm=require('vm'),assert=require('assert/strict');
const elements=new Map();for(const m of fs.readFileSync('claim.html','utf8').matchAll(/\bid="([^"]+)"/g))elements.set(m[1],{value:'',textContent:'',hidden:false,focus(){},select(){}});
const el=id=>elements.get(id),handlers={};let claims=0,pendingResolve=null;
const ctx={document:{getElementById:el,documentElement:{dataset:{}}},location:{hash:'#'+'a'.repeat(64),origin:'http://test'},matchMedia:()=>({matches:false}),addEventListener:(n,f)=>handlers[n]=f,navigator:{clipboard:{async writeText(v){assert.equal(v,'sk-wb-test');}}},isSecureContext:true,Number,Date,
fetch:async(url,opts)=>{assert.ok(!opts.headers||!('Authorization'in opts.headers));assert.equal(opts.credentials,'omit');
 if(url.endsWith('c'.repeat(64)))return new Promise(resolve=>{pendingResolve=()=>resolve({ok:true,json:async()=>({title:'STALE',total:9,shares:1,remaining:1,expires_at:'2099-01-01'})})});
 if(opts.method==='POST'){claims++;return {ok:true,json:async()=>({secret:'sk-wb-test',repeated:claims>1,key:{quota:{kind:'tokens',limit_units:500,used_units:100,expires_at:'2099-01-01'}}})};}
 return {ok:true,json:async()=>({title:'<img>',kind:'tokens',mode:'equal',total:1000,shares:2,claimed:claims?1:0,remaining:claims?1:2,expires_at:'2099-01-01',allowed_models:['cn:glm-5.2']})};}};
ctx.window=ctx;vm.createContext(ctx);vm.runInContext(fs.readFileSync('claim.js','utf8'),ctx);
const tick=()=>new Promise(resolve=>setImmediate(resolve));
(async()=>{await tick();assert.equal(el('claimTitle').textContent,'<img>');assert.equal(el('btnClaim').disabled,false);await el('btnClaim').onclick();assert.equal(el('claimSecret').value,'sk-wb-test');assert.equal(el('claimResult').hidden,false);assert.ok(el('claimQuota').textContent.includes('500'));await el('claimCopy').onclick();assert.equal(el('claimCopy').textContent,'已复制');await el('btnClaim').onclick();assert.ok(el('claimResultNote').textContent.includes('原来的'));
ctx.location.hash='#'+'c'.repeat(64);handlers.hashchange();assert.equal(el('claimSecret').value,'');await tick();ctx.location.hash='#'+'b'.repeat(64);handlers.hashchange();await tick();pendingResolve();await tick();assert.equal(el('claimTitle').textContent,'<img>');
ctx.location.hash='#bad';handlers.hashchange();assert.equal(el('btnClaim').disabled,true);assert.ok(el('claimProgress').textContent.includes('不完整'));console.log('claim recovery OK');})().catch(err=>{console.error(err);process.exit(1)});`
	file := filepath.Join(t.TempDir(), "claim.cjs")
	if err = os.WriteFile(file, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("public claim frontend: %v\n%s", err, out)
	}
}
