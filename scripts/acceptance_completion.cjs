// Isolated end-to-end checks. No production state, API key or real model is used.
const fs=require('node:fs/promises'),path=require('node:path'),net=require('node:net'),{spawn}=require('node:child_process'),assert=require('node:assert/strict');
const {chromium}=require('../runner/node_modules/playwright');
const root=path.resolve(__dirname,'..');
const stamp=new Date().toISOString().replace(/[:.]/g,'-');
const out=path.join(root,'artifacts','acceptance-completion-'+stamp);
let child,browser,base,boot,projectId,ctx,page;
const results=[];
const pause=ms=>new Promise(r=>setTimeout(r,ms));
async function freePort(){const server=net.createServer();await new Promise(r=>server.listen(0,'127.0.0.1',r));const port=server.address().port;await new Promise(r=>server.close(r));return port;}
async function start(port){
  const env={...process.env,ATOMS_ADDR:`127.0.0.1:${port}`,ATOMS_PUBLIC_URL:'',ATOMS_DATA_DIR:path.join(out,'data'),ATOMS_WEB_DIR:path.join(root,'web/dist'),ATOMS_LLM_API_KEY:'',ATOMS_LLM_BASE_URL:'',ATOMS_LLM_MODEL:'',ATOMS_BACKUP_LLM_API_KEY:'',ATOMS_BACKUP_LLM_BASE_URL:'',ATOMS_BACKUP_LLM_MODEL:'',ATOMS_RUNNER_TOKEN:'',ATOMS_RUNNER_URL:'',ATOMS_LLM_TPM:'1000000'};
  child=spawn(path.join(root,'tmp',process.platform==='win32'?'acceptance-server.exe':'acceptance-server'),[],{cwd:root,env,windowsHide:true,stdio:['ignore','pipe','pipe']});
  child.stdout.on('data',data=>fs.appendFile(path.join(out,'server.log'),data));child.stderr.on('data',data=>fs.appendFile(path.join(out,'server.log'),data));
  for(let i=0;i<100;i++){try{if((await fetch(base+'/healthz')).ok)return;}catch{}if(child.exitCode!==null)throw Error('server exited');await pause(100);}throw Error('server unavailable');
}
async function stop(){if(!child||child.exitCode!==null)return;const exited=new Promise(r=>child.once('exit',r));child.kill('SIGKILL');await exited;}
async function api(context,url,method='GET',body,csrf=boot?.csrf){const response=await context.request.fetch(base+url,{method,headers:csrf?{'X-CSRF-Token':csrf}:{},data:body});let data;try{data=await response.json();}catch{data=await response.text();}return {status:response.status(),data};}
function record(name,detail){results.push({name,detail,status:'passed'});console.log('PASS '+name);}
async function app(){return page.frameLocator('iframe[title="生成产物预览"]');}
async function savedText(text){await (await app()).locator('#items').filter({hasText:text}).waitFor();await page.locator('.app-data-bar').filter({hasText:'已保存到服务端'}).waitFor();}
(async()=>{
  await fs.mkdir(out,{recursive:true});const port=await freePort();base=`http://127.0.0.1:${port}`;await start(port);
  browser=await chromium.launch({headless:true,...(process.platform==='win32'?{executablePath:'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'}:{})});
  ctx=await browser.newContext({viewport:{width:1440,height:1000},acceptDownloads:true});page=await ctx.newPage();page.setDefaultTimeout(10000);
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  await page.goto(base);await page.getByRole('button',{name:'创建账号',exact:true}).click();
  await page.getByLabel('用户名',{exact:true}).fill('acceptance_user');await page.getByLabel('密码',{exact:true}).fill('acceptance passphrase 2026');
  await page.getByRole('button',{name:'注册并进入',exact:true}).click();await page.locator('.catalog-cards>button').first().waitFor();assert.equal(await page.locator('.catalog-cards>button').count(),3);
  await page.screenshot({path:path.join(out,'desktop-home.png'),fullPage:true});
  await page.locator('.catalog-cards>button').first().click();await (await app()).locator('#title').fill('服务重启后保留');await (await app()).locator('#add').click();await savedText('服务重启后保留');
  projectId=new URL(page.url()).searchParams.get('project');boot=(await api(ctx,'/api/bootstrap')).data;
  await (await app()).getByRole('button',{name:'更新 服务重启后保留',exact:true}).click();await (await app()).locator('.item').filter({hasText:'服务重启后保留'}).filter({hasText:'进行中'}).waitFor();
  await page.getByRole('button',{name:'刷新预览',exact:true}).click();await savedText('服务重启后保留');record('C01/C09 browser save and iframe reload','three public examples; own copy edits preserved');
  const before=(await api(ctx,`/api/projects/${projectId}/preview-access`,'POST',{version:''})).data;
  assert.equal(before.mode,'formal');const saved=(await ctx.request.get(base+`/api/projects/${projectId}/app-state`,{headers:{'X-Preview-Grant':before.grant}})).json();const beforeState=await saved;assert.equal(beforeState.revision,2);
  // An acknowledged write survives a hard process termination, with the same browser session.
  await stop();await start(port);await page.reload();await savedText('服务重启后保留');record('C09 hard backend restart','acknowledged data and progress survived SIGKILL');
  // Lost response: dispatch to real backend, wait for database commit using an independent read,
  // then kill before delivering the HTTP response back to the calling page.
  boot=(await api(ctx,'/api/bootstrap')).data;
  const grant=(await api(ctx,`/api/projects/${projectId}/preview-access`,'POST',{version:''})).data;
  const payload={grant:grant.grant,expected_revision:beforeState.revision,schema_version:1,request_id:'lost-response-operation',value:{items:[...beforeState.value.items,{id:'lost',title:'响应丢失后仍保留',stage:0,done:false}]}};
  let committed,release;const committedSignal=new Promise(resolve=>{committed=resolve});const responseGate=new Promise(resolve=>{release=resolve});let written;
  await page.route('**/app-state',async route=>{const response=await route.fetch();written={status:response.status(),data:await response.json()};committed();await responseGate;await route.abort('failed');});
  const lost=page.evaluate(async({id,payload,csrf})=>{try{await fetch(`/api/projects/${id}/app-state`,{method:'PUT',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify(payload)});return 'received';}catch{return 'response-lost';}},{id:projectId,payload,csrf:boot.csrf});
  await committedSignal;assert.equal(written.status,200);await stop();release();assert.equal(await lost,'response-lost');await page.unroute('**/app-state');
  await start(port);const repeat=await api(ctx,`/api/projects/${projectId}/app-state`,'PUT',payload);assert.equal(repeat.status,200);assert.equal(repeat.data.revision,written.data.revision);record('C10 committed write response loss and restart','proxy withheld committed response; process killed; replay returns original revision');
  // Sign in from a new device after the account-backed restart checks.
  const second=await browser.newContext({viewport:{width:390,height:844}});const secondPage=await second.newPage();await secondPage.goto(base);
  const secondBoot=(await api(second,'/api/bootstrap')).data;
  const login=await api(second,'/api/auth/login','POST',{username:'acceptance_user',password:'acceptance passphrase 2026'},secondBoot.csrf);assert.equal(login.status,200);
  const restored=await api(second,`/api/projects/${projectId}`);assert.equal(restored.status,200);
  await secondPage.goto(base+'/?project='+projectId);await secondPage.getByRole('button',{name:/产物工作区/}).click();
  await secondPage.frameLocator('iframe[title="生成产物预览"]').locator('#items').filter({hasText:'响应丢失后仍保留'}).waitFor();
  const controls=await secondPage.locator('.work-actions').evaluate(element=>({html:element.innerHTML,box:element.getBoundingClientRect().toJSON(),children:[...element.children].map(child=>({text:child.textContent,box:child.getBoundingClientRect().toJSON()}))}));
  await fs.writeFile(path.join(out,'mobile-controls.json'),JSON.stringify(controls,null,2));
  for(const control of controls.children)assert.ok(control.box.x>=0 && control.box.right<=390 && control.box.bottom<=controls.box.bottom+1,'mobile control clipped: '+control.text);
  await secondPage.screenshot({path:path.join(out,'mobile-restored.png'),fullPage:true});
  assert.equal(await secondPage.evaluate(()=>document.documentElement.scrollWidth>innerWidth+2),false);record('C05/C07/C13 cross-device account recovery','fresh 390px browser restored project and committed records without overflow');
  const outsider=await browser.newContext();await api(outsider,'/api/bootstrap');assert.equal((await api(outsider,`/api/projects/${projectId}`)).status,401);
  for(const suffix of ['preview','files?path=index.html','export','events'])assert.equal((await api(outsider,`/api/projects/${projectId}/${suffix}`)).status,401);
  record('C07 login-only route isolation','unsigned guest cannot read project, files, preview, export or events');
  boot=(await api(ctx,'/api/bootstrap')).data;
  const a=(await api(ctx,`/api/projects/${projectId}/preview-access`,'POST',{version:''})).data;
  const v=a.state.revision;const firstWrite=await api(ctx,`/api/projects/${projectId}/app-state`,'PUT',{grant:a.grant,expected_revision:v,schema_version:1,request_id:'cas-first-123',value:a.state.value});assert.equal(firstWrite.status,200);
  const conflict=await api(ctx,`/api/projects/${projectId}/app-state`,'PUT',{grant:a.grant,expected_revision:v,schema_version:1,request_id:'cas-second-123',value:{items:[]}});assert.equal(conflict.status,409);record('C11 concurrent stale save','second writer receives 409 and cannot erase records');
  // Exercise the user-facing JSON import, including its pre-import backup.
  await page.goto(base+'/?project='+projectId);await savedText('响应丢失后仍保留');
  const imported={schema_version:1,value:{items:[...a.state.value.items,{id:'imported',title:'导入私有记录',stage:0,done:false}]}};
  page.once('dialog',dialog=>dialog.accept());
  await page.locator('.data-import input').setInputFiles({name:'data.json',mimeType:'application/json',buffer:Buffer.from(JSON.stringify(imported))});
  await savedText('导入私有记录');const backups=await api(ctx,`/api/projects/${projectId}/app-backups`);assert.equal(backups.data.length,1);assert.deepEqual(backups.data[0].state.value,a.state.value);record('C15 JSON import with backup','confirmed import replaces current value; prior state remains downloadable');
  const share=(await api(ctx,`/api/projects/${projectId}/shares`,'POST',{version:''})).data;assert.ok(share.path);
  const publicPage=await outsider.newPage();await publicPage.goto(base+share.path);await publicPage.frameLocator('iframe').locator('#add').waitFor();
  assert.equal(await publicPage.frameLocator('iframe').getByText('响应丢失后仍保留').count(),0);
  await publicPage.frameLocator('iframe').locator('#title').fill('public trial');await publicPage.frameLocator('iframe').locator('#add').click();await publicPage.frameLocator('iframe').getByText('public trial',{exact:true}).waitFor();
  assert.equal((await api(ctx,`/api/projects/${projectId}/shares/${share.id}`,'DELETE')).status,200);await publicPage.reload();await publicPage.getByRole('alert').waitFor();record('C14 share seed isolation and revocation','public trial never receives owner records; revoked link fails on refresh');
  const html=await ctx.request.get(base+`/api/projects/${projectId}/export?format=html`);assert.equal(html.status(),200);const htmlText=await html.text();assert.ok(!htmlText.includes('响应丢失后仍保留'));
  const file=path.join(out,'offline.html');await fs.writeFile(file,htmlText);const offline=await browser.newContext({offline:true});const local=await offline.newPage();await local.goto(require('node:url').pathToFileURL(file).href);await local.locator('#title').fill('离线新增');await local.locator('#add').click();await local.locator('#items').filter({hasText:'离线新增'}).waitFor();await local.getByText(/离线副本 ·/).waitFor();record('C15 offline HTML','file opened with network offline; interaction works; private app data absent');
  const original=(await api(ctx,'/api/projects','POST',{title:'无模型需求'})).data;
  const submitted=await api(ctx,`/api/projects/${original.id}/actions`,'POST',{action:'submit',text:'制作任务管理工具',mode:'build',request_id:'no-key-fallback'});assert.ok(submitted.status<300);
  let failed;for(let i=0;i<100;i++){failed=(await api(ctx,`/api/projects/${original.id}`)).data;if(failed.recovery)break;await pause(100);}
  assert.ok(failed.recovery);assert.equal(failed.tasks[0].status,'failed');assert.equal(failed.runs[0].calls,0);const demo=(await api(ctx,`/api/projects/${failed.recovery.project_id}`)).data;assert.equal(demo.source.kind,'fallback');assert.ok(demo.preview_ready);record('C02 no-key fallback','distinct runnable demo, failed original preserved, zero model sends');
  const preserved=(await api(ctx,`/api/projects/${projectId}`)).data;
  assert.ok((await api(ctx,`/api/projects/${projectId}/actions`,'POST',{action:'submit',text:'增加标签筛选',mode:'build',request_id:'iterate-with-no-key'})).status<300);
  let iteration;for(let i=0;i<100;i++){iteration=(await api(ctx,`/api/projects/${projectId}`)).data;if(iteration.recovery)break;await pause(100);}
  assert.ok(iteration.recovery);assert.equal(iteration.head,preserved.head);assert.deepEqual(iteration.files,preserved.files);
  const remaining=(await api(ctx,`/api/projects/${projectId}/preview-access`,'POST',{version:''})).data;assert.deepEqual(remaining.state.value,imported.value);record('C04 failed iteration preserves delivered code and data','fallback stays in separate project; original head/files/state unchanged');
  assert.deepEqual(errors,[]);await fs.writeFile(path.join(out,'results.json'),JSON.stringify({at:new Date().toISOString(),results,notes:['Browser widths are simulated, not a physical domestic cellular test.','C10 validates committed-write response-loss replay, not an OS-level crash at every SQLite instruction.']},null,2));console.log('ARTIFACTS '+out);
})().catch(async error=>{console.error(error);if(page)await page.screenshot({path:path.join(out,'failure.png'),fullPage:true}).catch(()=>{});await fs.writeFile(path.join(out,'failure.txt'),String(error.stack));process.exitCode=1;}).finally(async()=>{if(browser)await browser.close();await stop();});
