package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRequestErrorDetailsInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `const fs = require('fs'), vm = require('vm'), assert = require('assert/strict');
const src = fs.readFileSync('app.js','utf8');
const elements = new Map(), handlers = new Map();
function el(id) {
  if (!elements.has(id)) {
    const classes = new Set();
    elements.set(id, {innerHTML:'',textContent:'',hidden:false,focus(){this.focused=true;},
      classList:{add(c){classes.add(c);},remove(c){classes.delete(c);},contains(c){return classes.has(c);}},
      addEventListener(type,fn){this[type]=fn;}});
  }
  return elements.get(id);
}
const failed = {request_id:'req-400',status:400,outcome:'http_error',model:'cn:deepseek-v4.1-flash',attempts:1,
  error:{code:'bad_params',upstream_code:'11101',message:'invalid tools <img src=x onerror=alert(1)>',
    upstream_request_id:'upstream-123',gateway_hint:'检查工具参数',truncated:true}};
const legacy = {request_id:'req-old',status:400,outcome:'http_error'};
const success = {request_id:'req-ok',status:200,outcome:'success'};
const ctx = {$:el,reqEntries:[failed,legacy,success],reqFilter:{},Date,Number,String,Array,
  fmtMs:()=> '554ms',fmtTok:String,trimFixed:String,reqErrorCopyText:'',reqErrorTrigger:null,
  esc:v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),
  addEventListener:(type,fn)=>handlers.set(type,fn),copyText:async()=>{},toast:()=>{}};
vm.createContext(ctx);
vm.runInContext(src.slice(src.indexOf('function reqMatch'),src.indexOf('/* 请求记录筛选控件')),ctx);
vm.runInContext(src.slice(src.indexOf('function requestLogText'),src.indexOf('function fmtBytes')),ctx);
vm.runInContext(src.slice(src.indexOf('let reqQTimer'),src.indexOf("if ($('reqQ'))")),ctx);
ctx.renderRequestTable();
assert.ok(el('reqBody').innerHTML.includes('11101 · invalid tools &lt;img'));
assert.ok(!el('reqBody').innerHTML.includes('<img'));
assert.equal((el('reqBody').innerHTML.match(/data-req-error=/g)||[]).length,2);
assert.equal(ctx.reqMatch(failed,{q:'11101 invalid',outcome:'http_error'}),true);
assert.equal(ctx.reqMatch(failed,{q:'upstream-123',outcome:''}),true);
const button={dataset:{reqError:'req-400'},isConnected:true,focus(){this.focused=true;}};
el('reqBody').click({target:{closest:()=>button}});
assert.equal(el('reqErrorMessage').textContent,failed.error.message);
assert.ok(el('reqErrorMeta').innerHTML.includes('upstream-123'));
assert.equal(el('reqErrorHint').textContent,'检查工具参数');
assert.equal(el('reqErrorHintBox').hidden,false);
assert.equal(el('reqErrorTruncated').hidden,false);
assert.ok(el('reqErrorVeil').classList.contains('on'));
assert.ok(el('btnReqErrorClose').focused);
assert.ok(ctx.reqErrorCopyText.includes('错误说明：invalid tools'));
assert.ok(ctx.requestLogText(failed).includes('bad_params · 11101'));
handlers.get('keydown')({key:'Escape'});
assert.equal(el('reqErrorVeil').classList.contains('on'),false);
assert.ok(button.focused);
ctx.openRequestError(legacy);
assert.ok(el('reqErrorMessage').textContent.includes('历史记录未保存'));
assert.equal(el('reqErrorHintBox').hidden,true);
assert.equal(el('reqErrorTruncated').hidden,true);
console.log('request error interaction OK');`
	file := filepath.Join(t.TempDir(), "request-errors.cjs")
	if err := os.WriteFile(file, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("request error details interaction: %v\n%s", err, out)
	}
}
