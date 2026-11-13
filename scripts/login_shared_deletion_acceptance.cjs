// Uses a disposable SQLite directory and local model-free server only.
const fs=require('node:fs/promises'),path=require('node:path'),net=require('node:net'),{spawn}=require('node:child_process'),assert=require('node:assert/strict');
const {chromium}=require('../runner/node_modules/playwright');
const root=path.resolve(__dirname,'..'),out=path.join(root,'artifacts','login-shared-deletion-'+new Date().toISOString().replace(/[:.]/g,'-'));
const results=[],errors=[];let server,browser,base,page;
const pause=ms=>new Promise(r=>setTimeout(r,ms));
async function api(ctx,url,method='GET',body){
  let csrf='';if(!['GET','HEAD'].includes(method)){const b=await ctx.request.get(base+'/api/bootstrap');csrf=(await b.json()).csrf;}
  const r=await ctx.request.fetch(base+'/api'+url,{method,headers:{'X-CSRF-Token':csrf},data:body});let data;try{data=await r.json();}catch{data=await r.text();}return{status:r.status(),data};
}
async function waitFor(check){for(let i=0;i<100;i++){if(await check())return;await pause(100);}throw Error('condition timed out');}
function pass(name){results.push({name,status:'passed'});console.log('PASS '+name);}
async function signIn(p,name,register=false){
  await p.getByRole('heading',{name:'欢迎回来',exact:true}).waitFor();
  if(register)await p.getByRole('button',{name:'创建账号',exact:true}).click();
  await p.getByLabel('用户名',{exact:true}).fill(name);await p.getByLabel('密码',{exact:true}).fill('isolated test password 2026');
  await p.getByRole('button',{name:register?'注册并进入':'登录',exact:true}).click();
  await p.waitForFunction(()=>!document.querySelector('.login-form'));
}
async function showSidebar(p){const button=p.getByRole('button',{name:'切换侧边栏',exact:true});if(await button.getAttribute('aria-expanded')==='false')await button.click();}
async function deletion(p,title){await showSidebar(p);await p.getByRole('button',{name:'删除项目：'+title,exact:true}).click();return p.getByRole('dialog',{name:'删除项目？',exact:true});}
function watch(p){p.on('pageerror',e=>errors.push(e.message));p.setDefaultTimeout(10000);}
(async()=>{
  await fs.mkdir(out,{recursive:true});
  const listener=net.createServer();await new Promise(r=>listener.listen(0,'127.0.0.1',r));const port=listener.address().port;await new Promise(r=>listener.close(r));base=`http://127.0.0.1:${port}`;
  const env={...process.env,ATOMS_ADDR:`127.0.0.1:${port}`,ATOMS_PUBLIC_URL:'',ATOMS_DATA_DIR:path.join(out,'data'),ATOMS_WEB_DIR:path.join(root,'web/dist'),ATOMS_LLM_API_KEY:'',ATOMS_LLM_BASE_URL:'',ATOMS_LLM_MODEL:'',ATOMS_BACKUP_LLM_API_KEY:'',ATOMS_BACKUP_LLM_BASE_URL:'',ATOMS_BACKUP_LLM_MODEL:'',ATOMS_RUNNER_TOKEN:'',ATOMS_RUNNER_URL:''};
  server=spawn(path.join(root,'tmp',process.platform==='win32'?'acceptance-server.exe':'acceptance-server'),[],{cwd:root,env,windowsHide:true,stdio:['ignore','pipe','pipe']});
  server.stdout.on('data',d=>fs.appendFile(path.join(out,'server.log'),d));server.stderr.on('data',d=>fs.appendFile(path.join(out,'server.log'),d));
  await waitFor(async()=>{try{return(await fetch(base+'/healthz')).ok;}catch{return false;}});
  browser=await chromium.launch({headless:true,...(process.platform==='win32'?{executablePath:'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'}:{})});
  const a=await browser.newContext({viewport:{width:1440,height:1000},acceptDownloads:true});page=await a.newPage();watch(page);
  const requests=[];page.on('request',r=>{if(r.url().includes('/api/'))requests.push(r.url());});
  await page.goto(base);await page.getByRole('heading',{name:'欢迎回来'}).waitFor();
  assert.equal(requests.some(url=>url.includes('/api/projects')),false);assert.equal(await page.locator('.composer').count(),0);
  await page.screenshot({path:path.join(out,'desktop-login.png'),fullPage:true});
  await page.getByLabel('用户名',{exact:true}).fill('unknown_user');await page.getByLabel('密码',{exact:true}).fill('wrong password');await page.getByRole('button',{name:'登录',exact:true}).click();await page.getByRole('alert').filter({hasText:'用户名或密码错误'}).waitFor();
  await signIn(page,'account_a',true);await page.locator('.catalog-cards>button').first().waitFor();pass('login gate, credential error and UI registration');
  await page.locator('.catalog-cards>button').first().click();
  const frame=page.frameLocator('iframe[title="生成产物预览"]');await frame.locator('#title').fill('跨账号共享的记录');await frame.locator('#add').click();await page.locator('.app-data-bar').filter({hasText:'已保存到服务端'}).waitFor();
  const pID=new URL(page.url()).searchParams.get('project');assert.ok(pID);
  const original=(await api(a,'/projects/'+pID)).data;assert.ok(original.id);
  const b=await browser.newContext({viewport:{width:390,height:844}}),mobile=await b.newPage();watch(mobile);await mobile.goto(base);
  await mobile.getByRole('heading',{name:'欢迎回来'}).waitFor();assert.equal(await mobile.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false);
  await mobile.screenshot({path:path.join(out,'mobile-login.png'),fullPage:true});
  await signIn(mobile,'account_b',true);assert.ok((await api(b,'/projects')).data.some(p=>p.id===pID));
  await mobile.goto(base+'/?project='+pID);await mobile.getByRole('button',{name:/产物工作区/}).click();
  const mobileFrame=mobile.frameLocator('iframe[title="生成产物预览"]');await mobileFrame.locator('#items').filter({hasText:'跨账号共享的记录'}).waitFor();
  await mobileFrame.locator('#title').fill('B 账号新增');await mobileFrame.locator('#add').click();await mobile.locator('.app-data-bar').filter({hasText:'已保存到服务端'}).waitFor();
  await page.getByRole('button',{name:'刷新预览',exact:true}).click();await frame.locator('#items').filter({hasText:'B 账号新增'}).waitFor();
  assert.equal(await mobile.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false);await mobile.screenshot({path:path.join(out,'mobile-shared-app.png'),fullPage:true});pass('390px login and cross-account application writes');

  const q=(await api(a,'/projects','POST',{title:'可删除的历史项目'})).data;
  await page.reload();await page.getByRole('button',{name:'退出',exact:true}).waitFor();
  let dialog=await deletion(page,q.title);await dialog.getByText(/影响所有登录账号/).waitFor();await dialog.getByRole('button',{name:'取消',exact:true}).click();assert.equal((await api(b,'/projects/'+q.id)).status,200);
  dialog=await deletion(page,q.title);await page.screenshot({path:path.join(out,'delete-dialog.png'),fullPage:true});await dialog.getByRole('button',{name:'确认删除',exact:true}).click();await dialog.waitFor({state:'hidden'});
  assert.equal(new URL(page.url()).searchParams.get('project'),pID);assert.equal((await api(b,'/projects/'+q.id)).status,404);pass('cancel deletion and delete another project without changing selection');
  const lost=(await api(a,'/projects','POST',{title:'响应丢失删除验收'})).data;
  await page.reload();dialog=await deletion(page,lost.title);
  assert.equal(await dialog.getByRole('button',{name:'取消',exact:true}).evaluate(e=>e===document.activeElement),true);
  await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});dialog=await deletion(page,lost.title);
  await page.route('**/api/projects/'+lost.id,async route=>{if(route.request().method()==='DELETE'){await route.fetch();await route.abort('failed');}else await route.continue();});
  await dialog.getByRole('button',{name:'确认删除',exact:true}).click();await dialog.getByRole('alert').waitFor();assert.equal((await api(b,'/projects/'+lost.id)).status,404);
  await page.unroute('**/api/projects/'+lost.id);await dialog.getByRole('button',{name:'确认删除',exact:true}).click();await dialog.waitFor({state:'hidden'});assert.equal(await page.getByRole('button',{name:'删除项目：'+lost.title,exact:true}).count(),0);pass('keyboard cancel and lost delete response retry synchronizes 404');
  const r=(await api(b,'/projects','POST',{title:'当前项目删除验收'})).data;
  await page.goto(base+'/?project='+r.id);dialog=await deletion(page,r.title);await dialog.getByRole('button',{name:'确认删除',exact:true}).click();await dialog.waitFor({state:'hidden'});await waitFor(async()=>new URL(page.url()).search==='');assert.equal(await page.locator('.project-title').textContent(),'新想法');pass('deleting current project returns to a clean new project');
  const s=(await api(a,'/projects','POST',{title:'被另一账号删除'})).data;
  await mobile.goto(base+'/?project='+s.id);await mobile.locator('.project-title').filter({hasText:s.title}).waitFor();assert.equal((await api(a,'/projects/'+s.id,'DELETE',{})).status,200);await mobile.getByRole('alert').filter({hasText:'删除'}).waitFor();assert.equal(await mobile.locator('.project-title').textContent(),'新想法');pass('SSE disconnect detects another account deletion');

  const shared=(await api(a,'/projects/'+pID+'/shares','POST',{version:''})).data;
  const guest=await browser.newContext({viewport:{width:1100,height:850}}),publicPage=await guest.newPage();watch(publicPage);await publicPage.goto(base+shared.path);await publicPage.getByRole('button',{name:'登录后创建副本'}).waitFor();
  assert.equal((await api(guest,'/projects')).status,401);
  const beforeCopy=(await api(a,'/projects')).data.length;
  await publicPage.getByRole('button',{name:'登录后创建副本'}).click();assert.match(publicPage.url(),/\/login\?returnTo=/);await signIn(publicPage,'account_a');await publicPage.getByRole('button',{name:'创建共享副本'}).waitFor();assert.equal(new URL(publicPage.url()).pathname,shared.path);assert.equal((await api(a,'/projects')).data.length,beforeCopy);
  await publicPage.getByRole('button',{name:'创建共享副本'}).click();await publicPage.getByRole('button',{name:'退出',exact:true}).waitFor();assert.equal((await api(a,'/projects')).data.length,beforeCopy+1);pass('public share login return without automatic copying');

  await page.goto(base+'/?project='+pID);await page.getByLabel('描述需求',{exact:true}).fill('这段草稿需要同账号恢复，不能自动发送');
  await waitFor(async()=>await page.evaluate(()=>JSON.parse(sessionStorage.getItem('atoms:draft')||'null')?.text?.includes('同账号恢复')));
  // A real revoked session is discovered through the unified download path.
  await page.route('**/events',route=>route.abort());
  await api(a,'/auth/logout','POST',{});
  if(await page.getByRole('heading',{name:'欢迎回来'}).count()===0)await page.getByRole('button',{name:'下载所选产物',exact:true}).click();
  await page.getByRole('heading',{name:'欢迎回来'}).waitFor();assert.equal(await page.locator('iframe').count(),0);assert.equal(await page.locator('.composer').count(),0);
  await page.unroute('**/events');await signIn(page,'account_a');await page.getByLabel('描述需求',{exact:true}).waitFor();assert.equal(await page.getByLabel('描述需求',{exact:true}).inputValue(),'这段草稿需要同账号恢复，不能自动发送');assert.equal((await api(a,'/projects/'+pID)).data.runs.length,0);pass('session expiry unmounts workbench and restores same-account draft without running AI');
  const tab=await a.newPage();watch(tab);await tab.goto(base);await tab.getByRole('button',{name:'退出',exact:true}).waitFor();await tab.getByRole('button',{name:'退出',exact:true}).click();await page.getByRole('heading',{name:'欢迎回来'}).waitFor();assert.equal(await page.evaluate(()=>sessionStorage.getItem('atoms:draft')),null);await signIn(page,'account_b');assert.equal(await page.getByLabel('描述需求',{exact:true}).inputValue(),'');pass('cross-tab explicit logout clears draft; another account gets no stale input');

  await page.goto(base+'/?project='+pID);await page.getByRole('button',{name:'下载所选产物',exact:true}).waitFor();
  let entered,releaseOld,finished;const captured=new Promise(r=>entered=r),gate=new Promise(r=>releaseOld=r),delivered=new Promise(r=>finished=r);
  await page.route('**/export?*',async route=>{entered();await gate;try{await route.fulfill({status:401,contentType:'application/json',body:JSON.stringify({code:'login_required',error:'expired old response'})});}catch{}finally{finished();}});
  await page.getByRole('button',{name:'下载所选产物',exact:true}).click();await captured;
  await page.getByRole('button',{name:'退出',exact:true}).click();await signIn(page,'account_a');await page.getByRole('button',{name:'退出',exact:true}).waitFor();
  releaseOld();await delivered;await page.unroute('**/export?*');assert.equal(await page.locator('.account-menu>span').first().textContent(),'account_a');assert.equal(await page.locator('.login-form').count(),0);pass('delayed old-account 401 cannot sign out a newly logged-in account');

  const malicious=await browser.newContext(),redirect=await malicious.newPage();watch(redirect);await redirect.goto(base+'/login?returnTo='+encodeURIComponent('https://example.invalid/steal'));await signIn(redirect,'account_a');await redirect.getByRole('button',{name:'退出',exact:true}).waitFor();assert.equal(new URL(redirect.url()).origin,base);assert.equal(new URL(redirect.url()).pathname,'/');pass('external returnTo rejected');
  assert.deepEqual(errors,[]);
  await fs.writeFile(path.join(out,'results.json'),JSON.stringify({at:new Date().toISOString(),results,modelCalls:0,notes:['Isolated data only; no production changes.','Mobile viewport is simulated, not a physical device test.']},null,2));console.log('ARTIFACTS '+out);
})().catch(async e=>{console.error(e);if(page)await page.screenshot({path:path.join(out,'failure.png'),fullPage:true}).catch(()=>{});await fs.writeFile(path.join(out,'failure.txt'),String(e.stack));process.exitCode=1;}).finally(async()=>{if(browser)await browser.close();if(server&&server.exitCode===null){const done=new Promise(r=>server.once('exit',r));server.kill();await done;}});
