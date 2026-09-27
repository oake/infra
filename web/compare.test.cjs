const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const route='/compare/a/b/base/head', key='a/b/base/head';
function setup(pathname=route){
 const app={innerHTML:''},requests=[],timers=[],listeners={};
 const commit=(revision,title)=>({repository:'a/b',revision,title,created:'2026-01-01'});
 const data={repository:'a/b',head:commit('head','Head title'),base:commit('base','Base title'),github_url:'https://github.com/a/b/compare/base..head',checks:'success',inputs:[],all:{name:'All hosts',pair:'all'},hosts:Array.from({length:18},(_,i)=>({id:`a/b/h${i}`,name:`h${i}`,pair:'same'})),comparisons:{same:{status:'different',packages:[{name:'test',before:'1',after:'2'}],size_old:1,size_new:2},all:{status:'different',packages:[{name:'test',before:'1',after:'2'}],size_old:1,size_new:2}}};
 const p={id:'pr',repository:'a/b',title:'PR title',number:1,head:'head',base:'base',state:'open',inputs:[],hosts:[],checks:'success'};
 const ctx={location:{pathname},document:{querySelector:s=>s==='#app'?app:null,querySelectorAll:()=>[],addEventListener:(event,fn)=>listeners[event]=fn,activeElement:null},window:{addEventListener:(event,fn)=>listeners[event]=fn,scrollTo:()=>{}},setInterval:fn=>timers.push(fn),setTimeout,clearTimeout,
 fetch:async url=>{requests.push(url);return {ok:true,json:async()=>url.includes('/compare/')?data:{hosts:{},repositories:{'a/b':{id:'a/b',main:'main'}},pull_requests:{pr:p}}}}};
 ctx.history={pushState:(_,__,path)=>ctx.location.pathname=path};
 vm.createContext(ctx);vm.runInContext(fs.readFileSync(__dirname+'/app.js','utf8'),ctx);
 return {ctx,app,requests,timers,listeners,data};
}
const settle=()=>new Promise(resolve=>setImmediate(resolve));
test('direct comparison loads everything once and never polls',async()=>{
 const c=setup();await settle();assert.deepEqual(c.requests,['/api/ui'+route]);assert(c.app.innerHTML.includes('test'));assert(!c.app.innerHTML.includes('data-old='));
 for(let i=0;i<5;i++)c.timers[0]();await settle();assert.equal(c.requests.length,1);
 await vm.runInContext('refresh(false)',c.ctx);assert.equal(c.requests.length,1);
});
test('PR cards pin commit URLs; navigation and back restore fleet polling',async()=>{
 const c=setup('/');await settle();assert(c.app.innerHTML.includes(`href="${route}"`));c.requests.length=0;
 const a={getAttribute:()=>route};
 await c.listeners.click({target:{closest:s=>s==='a'?a:null},button:0,preventDefault:()=>{}});
 await settle();assert.equal(c.ctx.location.pathname,route);assert.deepEqual(c.requests,['/api/ui'+route]);
 c.ctx.location.pathname='/';c.listeners.popstate();await settle();assert.equal(c.requests.at(-1),'/api/ui/state');
});
test('host selection and ordering use loaded comparisons',async()=>{
 const c=setup();await settle();assert.match(c.app.innerHTML,/data-selection="" aria-pressed="true"/);
 vm.runInContext(`selectComparisonHost('${key}','a/b/h3')`,c.ctx);
 let html=vm.runInContext(`comparisonReview(commitComparisons.get('${key}'),'${key}')`,c.ctx);
 assert.match(html,/data-selection="a\/b\/h3" aria-pressed="true"/);assert.equal(c.requests.length,1);
 c.data.comparisons.large={status:'different',packages:Array(5).fill({})};c.data.hosts[7].pair='large';
 html=vm.runInContext(`comparisonReview(commitComparisons.get('${key}'),'${key}')`,c.ctx);
 assert(html.indexOf('data-selection="a/b/h7"')<html.indexOf('data-selection="a/b/h0"'));
});
test('generic summary has commits and failures; PR detection only changes GitHub URL',async()=>{
 const c=setup();await settle();const before=c.app.innerHTML;
 assert(before.includes('Head title'));assert(before.includes('Base title'));assert(!before.includes('PR title'));
 assert(before.includes('<h2>Summary</h2>'));assert(!before.includes('Merge'));
 c.data.github_url='https://github.com/a/b/pull/1';
 let html=vm.runInContext(`commitComparisonView('${key}')`,c.ctx);
 assert.equal(html,before.replace('https://github.com/a/b/compare/base..head','https://github.com/a/b/pull/1'));
 c.data.checks='failure';c.data.failed_checks=[{name:'slate',summary:'<summary>',detail:'raw trace',url:'https://builder/#/builders/217/builds/399/steps/1/logs/nix_error'}];
 html=vm.runInContext(`commitComparisonView('${key}')`,c.ctx);
 assert(html.includes('Failed checks'));assert(html.includes('&lt;summary&gt;'));assert(!html.includes('raw trace'));assert(html.includes('/steps/1/logs/nix_error'));
 c.data.failed_checks[0].summary='';html=vm.runInContext(`commitComparisonView('${key}')`,c.ctx);assert(!html.includes('raw trace'));assert(!html.includes('&lt;summary&gt;'));assert(html.includes('/steps/1/logs/nix_error'));
});
