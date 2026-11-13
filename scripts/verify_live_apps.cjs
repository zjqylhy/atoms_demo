const fs=require('node:fs/promises'),path=require('node:path'),assert=require('node:assert/strict');
const{buildHTML}=require('../runner/web.cjs');const{checkBrowser}=require('../runner/browser.cjs');
const directory=path.resolve(process.argv[2]||'artifacts/live-app-completion-20261113');
const step=(action,selector,value)=>({action,selector,value,requirement:'真实模型应用持久化交互与刷新恢复'});
(async()=>{
  const sdk=await fs.readFile(path.resolve(__dirname,'../internal/appruntime/runtime.js'),'utf8');
  const before=JSON.parse(await fs.readFile(path.join(directory,'legacy-before.json'),'utf8'));
  const receipts=JSON.parse(await fs.readFile(path.join(directory,'receipts.json'),'utf8'));
  assert.deepEqual(receipts.find(r=>r.name==='iterated').changed_files,['index.html']);
  const results=[];
  for(const name of ['generated','iterated','legacy-adapted']){
    const root=path.join(directory,name),files={};
    for(const file of ['index.html','app.js','style.css','atoms.app.json'])files[file]=await fs.readFile(path.join(root,file),'utf8');
    if(name==='legacy-adapted'){assert.equal(files['index.html'],before['index.html']);assert.equal(files['style.css'],before['style.css']);}
    const contract=JSON.parse(files['atoms.app.json']);files['atoms-runtime.js']=sdk;await fs.writeFile(path.join(root,'atoms-runtime.js'),sdk);
    const built=await buildHTML({files},'index.html',{root,nodePaths:[path.resolve(__dirname,'../runner/node_modules')]});
    const steps=name==='legacy-adapted'?[step('click','#nextBtn'),step('expect_saved','body'),step('reload','body'),step('expect_text','#pageIndicator','2')]:[step('fill','#new-item','真实模型持久化验收'),step('click','#add-item'),step('expect_saved','body'),step('reload','body'),step('expect_text','#items','真实模型持久化验收')];
    if(name==='iterated')steps.push(step('expect_text','#add-item','新增待办'));
    if(name!=='legacy-adapted')steps.push(step('check','#items .toggle'),step('expect_saved','body'),step('reload','body'),step('expect_visible','#items .item.done'));
    const checks=[];
    await checkBrowser(built.html,{steps},true,(name,detail,status='passed')=>checks.push({name,detail,status}),{inputPaths:Object.keys(files),initialState:contract.initial_state,launchOptions:process.platform==='win32'?{executablePath:'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'}:{}});
    await fs.writeFile(path.join(root,'browser-check.json'),JSON.stringify(checks,null,2));await fs.writeFile(path.join(root,'preview.html'),built.html);
    assert.ok(checks.every(c=>c.status==='passed'),JSON.stringify({name,checks}));results.push({name,checks:checks.length,status:'passed'});console.log('PASS live '+name+' desktop/mobile save + reload');
  }
  await fs.writeFile(path.join(directory,'verification.json'),JSON.stringify({results,scope:'Real model output checked with production builder/SDK and isolated browser test host; not a full production Agent scheduling run.'},null,2));
})().catch(error=>{console.error(error);process.exitCode=1;});
