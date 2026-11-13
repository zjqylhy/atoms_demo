const kind = document.body.dataset.kind;
const $ = id => document.getElementById(id);
let state, revision, busy = false, retry;
const labels = {board:['团队任务看板','把想法推进到完成','新任务','添加任务'],reading:['我的阅读清单','给每一本书留一点时间','书名','加入书架'],calculator:['复利计算器','比较不同参数，保存计算方案','方案名称','计算并保存']};
const words = labels[kind];
$('heading').textContent=words[0];$('subtitle').textContent=words[1];$('title').placeholder=words[2];$('add').textContent=words[3];
$('reading-fields').hidden=kind!=='reading';$('calculator-fields').hidden=kind!=='calculator';
function status(text,error=false){$('status').textContent=text;$('status').classList.toggle('error',error);}
function button(text,handler,label){const el=document.createElement('button');el.type='button';el.textContent=text;if(label)el.setAttribute('aria-label',label);el.onclick=handler;return el;}
function render(){
  $('items').replaceChildren();
  const query=$('search').value.toLowerCase();
  const filtered=state.items.filter(item=>item.title.toLowerCase().includes(query));
  $('count').textContent=`${state.items.length} 条记录`;
  for(const item of filtered){
    const card=document.createElement('article');card.className='item';card.dataset.done=item.done?'yes':'no';
    const content=document.createElement('div');content.className='item-content';
    const title=document.createElement('strong');title.textContent=item.title;content.append(title);
    const detail=document.createElement('p');
    detail.textContent=kind==='calculator'?`本金 ${item.principal.toLocaleString()} · 年利率 ${item.rate}% · ${item.years} 年 → ${item.result.toFixed(2)}`:kind==='reading'?`${item.author || '作者未填写'} · ${item.done?'已读完':'待阅读'}`:['待处理','进行中','已完成'][item.stage||0];
    content.append(detail);card.append(content);
    const actions=document.createElement('div');actions.className='item-actions';
    if(kind!=='calculator')actions.append(button(kind==='reading'?(item.done?'标为未读':'标为已读'):['开始','完成','重新打开'][item.stage||0],()=>change(d=>{const entry=d.items.find(x=>x.id===item.id);if(kind==='reading')entry.done=!entry.done;else {entry.stage=((entry.stage||0)+1)%3;entry.done=entry.stage===2;} }),`更新 ${item.title}`));
    actions.append(button('改名',()=>{const title=prompt('输入新名称',item.title);if(title?.trim())change(d=>{d.items.find(x=>x.id===item.id).title=title.trim().slice(0,120);});},`改名 ${item.title}`));
    actions.append(button('删除',()=>change(d=>{d.items=d.items.filter(x=>x.id!==item.id);}),`删除 ${item.title}`));
    card.append(actions);$('items').append(card);
  }
  $('empty').hidden=filtered.length!==0;
}
async function commit(next,requestId){
  if(busy)return;busy=true;$('add').disabled=true;status('正在保存…');
  try{const result=await Atoms.data.save(next,{expectedRevision:revision,requestId});revision=result.revision;state=next;render();status('已保存');retry=undefined;$('retry').hidden=true;}
  catch(error){status(error.message,true);retry=()=>commit(next,requestId);$('retry').hidden=false;}
  finally{busy=false;$('add').disabled=false;}
}
function change(update){if(!state||busy)return;const next=structuredClone(state);update(next);return commit(next,crypto.randomUUID());}
$('entry-form').onsubmit=async event=>{
  event.preventDefault();const title=$('title').value.trim();if(!title)return;
  const item={id:crypto.randomUUID(),title:title.slice(0,120),stage:0,done:false};
  if(kind==='reading')item.author=$('author').value.trim().slice(0,120);
  if(kind==='calculator'){
    item.principal=Number($('principal').value);item.rate=Number($('rate').value);item.years=Number($('years').value);
    item.result=item.principal*Math.pow(1+item.rate/100,item.years);
    if(!Number.isFinite(item.result)||item.result>1e18){status('计算结果超出范围，请调整参数',true);return;}
  }
  await change(d=>d.items.unshift(item));if(!retry)$('title').value='';
};
$('retry').onclick=()=>retry?.();
$('reload').onclick=()=>load();$('search').oninput=()=>{if(state)render();};
async function load(){try{status('正在读取已保存数据…');const saved=await Atoms.data.load();if(!Array.isArray(saved.value.items))throw new Error('数据格式不兼容');state=saved.value;revision=saved.revision;retry=undefined;$('retry').hidden=true;render();status('已加载保存的数据');$('add').disabled=false;}catch(error){status(error.message,true);}}
load();
