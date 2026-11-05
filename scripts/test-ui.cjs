const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname, '../source/ui/index.html'), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];
const settle = async () => { for (let i = 0; i < 20; i++) await new Promise(resolve => setImmediate(resolve)); };
const deferred = () => { let resolve, reject; const promise = new Promise((a,b) => { resolve=a; reject=b; }); return {promise,resolve,reject}; };

class FakeElement {
  constructor(tag) { this.tag=tag; this.children=[]; this.attributes={}; this.listeners={}; this.hidden=false; this.disabled=false; this.value=''; this.className=''; this.ownText=''; this.focused=0; }
  get textContent() { return [this.ownText, ...this.children.map(child => child.textContent)].filter(Boolean).join(' '); }
  set textContent(value) { this.ownText=String(value); this.children=[]; }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.ownText=''; this.children=nodes; }
  setAttribute(k,v) { this.attributes[k]=v; }
  removeAttribute(k) { delete this.attributes[k]; if (k==='src') delete this.src; }
  addEventListener(k,fn) { this.listeners[k]=fn; }
  click() { this.listeners.click(); }
  focus() { this.focused++; }
  find(match) { if (match(this)) return this; for (const c of this.children) { const hit=c.find(match); if (hit) return hit; } return null; }
  findAll(match, out=[]) { if (match(this)) out.push(this); for (const c of this.children) c.findAll(match,out); return out; }
}

const INVENTORY = {ranges:[
  {rangeID:'MINE',name:'',vms:[{proxmoxID:101,name:'dc01',ip:'10.1.10.11',os:'windows',isRouter:false},{proxmoxID:102,name:'router',ip:'10.1.10.254',os:'linux',isRouter:true}]},
  {rangeID:'SHARED',name:'Team lab',vms:[{proxmoxID:201,name:'kali',ip:'',os:'linux',isRouter:false}]},
]};
const running = {state:'running',image:'data:image/jpeg;base64,AAAA',capturedAt:'2026-01-01T00:00:00Z'};

async function setup(options = {}) {
  const calls = [], listeners = {}, elements = {}, timers = [];
  for (const id of ['overview','content','status','refresh','auto','detail','detail-title','detail-status','detail-image','detail-refresh','back']) {
    elements[id] = new FakeElement(id);
  }
  elements.auto.value = '0';
  elements.refresh.disabled = true;
  elements.detail.hidden = true;
  const document = {getElementById:id=>elements[id], createElement:tag=>new FakeElement(tag), documentElement:{dataset:{}}};
  const window = {addEventListener:(name,fn)=>listeners[name]=fn};
  let inventoryCalls = 0, screenCalls = 0;
  if (!options.noBridge) window.ludus = {
    context: () => options.initial || Promise.resolve(options.context || {theme:'dark'}),
    request: async (route, request = {}) => {
      calls.push({route, query:request.query});
      if (route === '/screens') { inventoryCalls++; return options.screens ? options.screens(inventoryCalls) : INVENTORY; }
      screenCalls++;
      return options.screen ? options.screen(request.query, screenCalls) : running;
    },
  };
  const setTimeoutStub = (fn, ms) => { timers.push({fn, ms}); return timers.length; };
  vm.runInNewContext(script, {window,document,Error,setTimeout:setTimeoutStub,clearTimeout:()=>{}});
  await settle();
  const card = name => elements.content.find(node => node.tag==='article' && node.textContent.includes(name));
  const refreshOf = name => card(name).find(node => node.tag==='button' && node.ownText==='Refresh');
  const imageOf = name => card(name).find(node => node.tag==='img');
  return {calls,timers,elements,document,card,refreshOf,imageOf,event:detail=>listeners['ludus:context']({detail}),
    screenCalls:()=>calls.filter(call=>call.route==='/screen')};
}

test('every VM in every range gets a screen, without range scope in the request', async () => {
  const a=await setup();
  assert.equal(a.calls[0].route,'/screens'); assert.equal(a.calls[0].query,undefined);
  assert.deepEqual(a.screenCalls().map(call=>call.query.vmid).sort(),[101,102,201]);
  assert.equal(a.elements.content.findAll(node=>node.tag==='h2').length,2);
  assert.match(a.elements.content.textContent,/SHARED · Team lab/);
  assert.equal(a.imageOf('dc01').src,running.image); assert.equal(a.imageOf('dc01').hidden,false);
  assert.equal(a.elements.refresh.disabled,false);
  assert.match(a.elements.status.textContent,/Captured 3 of 3/);
});
test('captures never exceed the concurrency limit', async () => {
  const gates=[]; let active=0, peak=0;
  const a=await setup({screen:async()=>{ active++; peak=Math.max(peak,active); const gate=deferred(); gates.push(gate); await gate.promise; active--; return running; },
    screens:()=>({ranges:[{rangeID:'BIG',vms:Array.from({length:9},(_,i)=>({proxmoxID:i+1,name:'vm'+i}))}]})});
  for (let i=0;i<9;i++) { assert.ok(gates[i],'capture '+i+' started'); gates[i].resolve(); await settle(); }
  assert.equal(peak,4); assert.equal(a.screenCalls().length,9);
});
test('no ranges and undeployed ranges explain themselves', async () => {
  const a=await setup({screens:()=>({ranges:[]})});
  assert.match(a.elements.content.textContent,/do not have access to any ranges/);
  assert.equal(a.elements.refresh.disabled,false); assert.equal(a.screenCalls().length,0);
  const b=await setup({screens:()=>({ranges:[{rangeID:'NEW',vms:[]}]})});
  assert.match(b.elements.content.textContent,/no deployed machines/);
});
test('one failed or powered-off VM stays on its own card', async () => {
  const a=await setup({screen:query=>{
    if (query.vmid===101) throw new Error('Your Proxmox account cannot open this console');
    if (query.vmid===102) return {state:'stopped',capturedAt:'2026-01-01T00:00:00Z'};
    return running;
  }});
  assert.match(a.card('dc01').textContent,/cannot open this console/);
  assert.equal(a.card('dc01').find(node=>node.attributes.role==='alert').textContent,'Capture failed');
  assert.match(a.card('router').textContent,/Powered off/);
  assert.equal(a.card('router').find(node=>node.attributes.role==='alert'),null);
  assert.equal(a.imageOf('kali').hidden,false);
});
test('a failed card can be retried on its own', async () => {
  let fail=true;
  const a=await setup({screen:query=>{ if (query.vmid===101&&fail) throw new Error('nope'); return running; }});
  fail=false; const before=a.screenCalls().length;
  a.refreshOf('dc01').click(); await settle();
  assert.equal(a.screenCalls().length,before+1);
  assert.equal(a.imageOf('dc01').hidden,false);
  assert.equal(a.card('dc01').find(node=>node.attributes.role==='alert'),null);
});
test('one unreadable range keeps its alert while the others load', async () => {
  const a=await setup({screens:()=>({ranges:[{rangeID:'BAD',vms:[],error:'Could not load this range\'s machines.'},{rangeID:'OK',vms:[{proxmoxID:1,name:'one'}]}]})});
  assert.ok(a.elements.content.find(node=>node.attributes.role==='alert'&&node.textContent.includes('Could not load')));
  assert.equal(a.imageOf('one').hidden,false);
});
test('a failed inventory shows an alert and can be retried', async () => {
  const a=await setup({screens:n=>{ if (n===1) throw new Error('Access denied'); return INVENTORY; }});
  assert.match(a.elements.content.textContent,/Access denied/);
  assert.ok(a.elements.content.find(node=>node.attributes.role==='alert'));
  assert.equal(a.elements.refresh.disabled,false);
  a.elements.refresh.click(); await settle();
  assert.equal(a.elements.content.find(node=>node.attributes.role==='alert'),null);
  assert.ok(a.card('dc01'));
});
test('theme and range updates do not reload the screens', async () => {
  const a=await setup(); const count=a.calls.length;
  a.event({theme:'light'}); a.event({rangeID:'OTHER'}); await settle();
  assert.equal(a.calls.length,count);
  assert.equal(a.document.documentElement.dataset.theme,'light');
  assert.ok(a.card('dc01'));
});
test('an older inventory cannot replace a newer one', async () => {
  const old=deferred();
  const a=await setup({screens:n=>n===1?old.promise:{ranges:[{rangeID:'NEWER',vms:[{proxmoxID:2,name:'current'}]}]}});
  a.elements.auto.value='30'; a.elements.auto.listeners.change();
  a.timers.at(-1).fn(); await settle();
  assert.ok(a.card('current'));
  old.resolve({ranges:[{rangeID:'OLDER',vms:[{proxmoxID:1,name:'stale'}]}]}); await settle();
  assert.ok(a.card('current')); assert.equal(a.card('stale'),null);
  assert.equal(a.screenCalls().length,1);
});
test('a stale capture cannot overwrite a newer one on the same card', async () => {
  const first=deferred();
  const a=await setup({screens:()=>({ranges:[{rangeID:'R',vms:[{proxmoxID:1,name:'one'}]}]}),
    screen:(_,n)=>n===1?first.promise:{...running,image:'data:image/jpeg;base64,NEW'}});
  a.refreshOf('one').click(); await settle();
  first.resolve({...running,image:'data:image/jpeg;base64,OLD'}); await settle();
  assert.equal(a.imageOf('one').src,'data:image/jpeg;base64,NEW');
});
test('auto refresh is off by default and follows the selection', async () => {
  const a=await setup();
  assert.equal(a.timers.length,0);
  a.elements.auto.value='30'; a.elements.auto.listeners.change();
  assert.equal(a.timers.at(-1).ms,30000);
  const before=a.calls.length;
  a.timers.at(-1).fn(); await settle();
  assert.ok(a.calls.length>before);
  assert.equal(a.timers.at(-1).ms,30000);
});
test('a refresh keeps the previous image until the new one arrives', async () => {
  const gate=deferred();
  const a=await setup({screens:()=>({ranges:[{rangeID:'R',vms:[{proxmoxID:1,name:'one'}]}]}),screen:(_,n)=>n===1?running:gate.promise});
  a.elements.refresh.click(); await settle();
  assert.equal(a.imageOf('one').src,running.image); assert.equal(a.imageOf('one').hidden,false);
  gate.resolve(running); await settle();
});
test('the enlarged view asks for a larger image and restores focus', async () => {
  const a=await setup(); const frame=a.card('dc01').find(node=>node.className==='frame');
  frame.click(); await settle();
  assert.equal(a.elements.overview.hidden,true); assert.equal(a.elements.detail.hidden,false);
  const call=a.screenCalls().at(-1); assert.equal(call.query.vmid,101); assert.equal(call.query.width,1280);
  assert.equal(a.elements['detail-image'].src,running.image);
  assert.equal(a.elements.back.focused,1);
  const before=a.calls.length;
  a.elements.auto.value='30'; a.elements.auto.listeners.change(); a.timers.at(-1).fn(); await settle();
  assert.equal(a.calls.length,before,'auto refresh pauses while enlarged');
  a.elements.back.click();
  assert.equal(a.elements.detail.hidden,true); assert.equal(a.elements.overview.hidden,false);
  assert.equal(frame.focused,1);
});
test('a context event wins over a stale initial context response', async () => {
  const initial=deferred(); const a=await setup({initial:initial.promise});
  a.event({theme:'light'}); await settle();
  initial.resolve({theme:'dark'}); await settle();
  assert.equal(a.document.documentElement.dataset.theme,'light');
  assert.equal(a.calls.filter(call=>call.route==='/screens').length,1);
});
test('standalone HTML explains that the Ludus bridge is required', async () => {
  const a=await setup({noBridge:true});
  assert.match(a.elements.content.textContent,/Open this plugin in Ludus/);
  assert.equal(a.elements.refresh.disabled,true);
});
