const fs=require('node:fs/promises'),path=require('node:path'),os=require('node:os'),crypto=require('node:crypto');
const {buildHTML}=require('../runner/web.cjs');
const {checkBrowser}=require('../runner/browser.cjs');
const hash=data=>crypto.createHash('sha256').update(data).digest('hex');
const root=path.resolve(__dirname,'..');
const definitions=[
  {id:'board',title:'团队任务看板',description:'添加任务、推进状态、搜索和重命名；记录可恢复。',initial:{items:[{id:'sample-1',title:'整理本周目标',stage:0,done:false},{id:'sample-2',title:'准备演示材料',stage:1,done:false}]}},
  {id:'reading',title:'我的阅读清单',description:'记录书名和作者、标记已读，保存你的阅读进度。',initial:{items:[{id:'sample-1',title:'从一本书开始',author:'示例作者',done:false}]}},
  {id:'calculator',title:'复利计算器',description:'调整本金、利率和年数，保存并比较计算方案。',initial:{items:[]}}
];
(async()=>{
  const sdk=await fs.readFile(path.join(root,'internal/appruntime/runtime.js'),'utf8');
  for(const definition of definitions){
    const files={'atoms-runtime.js':sdk,'atoms.app.json':JSON.stringify({api_version:1,schema_version:1,initial_state:definition.initial})};
    for(const name of ['index.html','app.js','style.css'])files[name]=(await fs.readFile(path.join(root,'internal/catalog/source',name),'utf8')).replace('TEMPLATE_KIND',definition.id);
    const tmp=await fs.mkdtemp(path.join(os.tmpdir(),'atoms-catalog-'));
    try{
      for(const [name,content]of Object.entries(files))await fs.writeFile(path.join(tmp,name),content);
      const built=await buildHTML({files},'index.html',{root:tmp,nodePaths:[path.join(root,'runner/node_modules')]});
      const checks=[];const step=(action,selector,value)=>({action,selector,value,requirement:'新增并刷新恢复示例记录'});
      await checkBrowser(built.html,{steps:[step('fill','#title','验收新增记录'),step('click','#add'),step('expect_saved','body'),step('reload','body'),step('expect_text','#items','验收新增记录')]},true,(name,detail,status='passed')=>checks.push({name,detail,status}),{inputPaths:Object.keys(files),initialState:definition.initial,launchOptions:process.platform==='win32'?{executablePath:'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'}:{}});
      if(checks.some(check=>check.status!=='passed'))throw Error(JSON.stringify(checks));
      const output={id:definition.id,title:definition.title,description:definition.description,version:1,files,html:built.html,source_hashes:Object.fromEntries(Object.entries(files).map(([name,text])=>[name,hash(text)])),html_hash:hash(built.html),verification:{kind:'packaged_browser',at:new Date().toISOString(),checks}};
      const dir=path.join(root,'internal/catalog/bundles');await fs.mkdir(dir,{recursive:true});await fs.writeFile(path.join(dir,definition.id+'.json'),JSON.stringify(output,null,2)+'\n');
      console.log(definition.id+': verified desktop/mobile add + save + reload');
    }finally{await fs.rm(tmp,{recursive:true,force:true});}
  }
})().catch(error=>{console.error(error);process.exitCode=1;});
