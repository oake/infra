const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
function setup(){
 const c={timelinePages:new Map(),configurationMetadata:new Map(),comparisonSelections:new Map(),values:Object.values,esc:String,date:String,enc:encodeURIComponent,commitURL:c=>`https://github.com/r/commit/${c.revision}`,badge:s=>`[${s}]`,button:()=>'<button>Load more</button>'};
 vm.createContext(c);const source=fs.readFileSync(__dirname+'/app.js','utf8');vm.runInContext(source.slice(source.indexOf('function timelineConfigurations('),source.indexOf('function hostView(')),c);return c;
}
const entry=(path,day=1,offBranch=false)=>({path,offBranch,commit:{revision:path,title:path,created:`2026-01-${String(day).padStart(2,'0')}`,branch:offBranch?'feature/test':'main'}});
const page=(entries,cursor='next')=>({entries,next_cursor:cursor,version:'v1',comparison:{before:'live',after:'latest'},staged:'',unknown:false});
test('Load more requests the server cursor and appends only the returned entries',async()=>{
 const c=setup();c.acceptTimeline('r/host',page([entry('latest',3)]));const urls=[];
 c.request=async url=>{urls.push(url);return page([entry('older',2)],'last')};
 await c.loadMoreTimeline('r/host');assert.equal(urls[0],'/api/ui/hosts/r%2Fhost/timeline?cursor=next');assert.equal(c.timelinePages.get('r/host').entries.length,2);assert.equal(c.timelinePages.get('r/host').next_cursor,'last');
 c.request=async()=>page([entry('oldest')],'');await c.loadMoreTimeline('r/host');assert.equal(c.timelineConfigurations({id:'r/host'}).more,false);
 c.request=async()=>{throw Error('should not fetch exhausted timeline')};await c.loadMoreTimeline('r/host');
});
test('polling keeps loaded pages and selections when the timeline version is unchanged',()=>{
 const c=setup();c.acceptTimeline('h',page([entry('latest',3)]));c.acceptTimeline('h',page([entry('older',2)],''),true);c.comparisonSelections.set('h',{before:'older',after:'latest'});
 c.acceptTimeline('h',page([entry('latest',3)]));assert.equal(c.timelinePages.get('h').entries.length,2);assert.equal(c.timelinePages.get('h').next_cursor,'');assert.equal(c.selectedComparison({id:'h'}).before,'older');
 c.comparisonSelections.delete('h');assert.equal(c.selectedComparison({id:'h'}).before,'live');
});
test('stale cursor reloads first page and removes obsolete history',async()=>{
 const c=setup();c.acceptTimeline('h',page([entry('old')]));let calls=0;
 c.request=async()=>{if(++calls===1){const e=Error('changed');e.status=409;throw e}return {...page([entry('new')]),version:'v2'}};
 await c.loadMoreTimeline('h');assert.equal(calls,2);assert.equal(c.timelinePages.get('h').entries.length,1);assert.equal(c.timelinePages.get('h').entries[0].path,'new');
});
test('renders server branch exceptions and undated unknown live marker',()=>{
 const c=setup();c.acceptTimeline('h',{...page([entry('main'),entry('staged',2,true)]),unknown:true,staged:'staged'});
 const html=c.configurationTimeline({id:'h',observation:{active:'unknown'}});assert(html.includes('! feature/test'));assert(html.includes('<strong>unknown configuration</strong>'));assert(html.includes('[Live]'));assert(html.includes('[Reboot to apply]'));assert.equal((html.match(/<time>/g)||[]).length,2);
});
test('failed page request preserves loaded entries and cursor for retry',async()=>{
 const c=setup();c.acceptTimeline('h',page([entry('first')]));c.request=async()=>{throw Error('offline')};await assert.rejects(()=>c.loadMoreTimeline('h'),/offline/);assert.equal(c.timelinePages.get('h').next_cursor,'next');assert.equal(c.timelinePages.get('h').entries.length,1);
});
test('deployment card uses scoped attempt and escapes error text',()=>{
 const c=setup();c.esc=s=>String(s).replaceAll('<','&lt;').replaceAll('>','&gt;');c.acceptTimeline('h',{...page([]),attempt:{outcome:'unreachable',finished:'2026-01-02T11:30:00Z',detail:'SSH: <timeout>\nRetry later'}});
 const h={id:'h',status:'Deployment queued'};const html=c.deploymentAttempt(h);assert(html.includes('Host unreachable'));assert(html.includes('2026-01-02T11:30:00Z'));assert(html.includes('&lt;timeout&gt;\nRetry later'));
 h.status='Up to date';assert.equal(c.deploymentAttempt(h),'');
});
