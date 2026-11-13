(() => {
  const config=OFFLINE_CONFIG;
  const key='atoms-offline:'+config.key;
  let state={schema_version:config.schema,revision:0,value:config.initial},durable=true;
  const receipts=new Map();
  try { const existing=localStorage.getItem(key);if(existing){const saved=JSON.parse(existing);if(saved.schema_version===config.schema&&saved.value&&Number.isSafeInteger(saved.revision))state=saved;}localStorage.setItem(key,JSON.stringify(state)); }catch{durable=false;}
  function valid(value){if(!value||typeof value!=='object'||Array.isArray(value)||new TextEncoder().encode(JSON.stringify(value)).length>262144)throw Error('数据必须是 256 KiB 以内的对象');}
  Object.defineProperty(window,'Atoms',{value:Object.freeze({data:Object.freeze({
    load:async()=>structuredClone(state),
    save:async(value,options={})=>{
      valid(value);const hash=JSON.stringify([value,options.expectedRevision]);const prior=receipts.get(options.requestId);
      if(prior){if(prior.hash!==hash)throw Error('请求标识重复');return {revision:prior.revision,saved:true};}
      if(options.expectedRevision!==state.revision)throw Error('本地数据冲突，请重新加载');
      const next={...state,value:structuredClone(value),revision:state.revision+1};
      if(durable){try{localStorage.setItem(key,JSON.stringify(next));}catch{throw Error('浏览器缓存保存失败，请导出 JSON 备份后重试');}}
      state=next;receipts.set(options.requestId,{hash,revision:state.revision});return {revision:state.revision,saved:true};
    }
  })}),writable:false});
  function download(value,name){const url=URL.createObjectURL(new Blob([JSON.stringify(value,null,2)],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download=name;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}
  addEventListener('DOMContentLoaded',()=>{
    const bar=document.createElement('div');bar.style='padding:12px;background:#fff2cd;color:#614c18;font:13px system-ui;display:flex;gap:12px;align-items:center;flex-wrap:wrap;';
    const text=document.createElement('span');text.textContent=durable?'离线副本 · 仅当前浏览器缓存，file:// 缓存不保证保留；请导出 JSON 长期保存。不会同步服务端。':'离线副本 · 此浏览器不能持久保存，本页关闭会丢失修改；请导出 JSON 保存。不会同步服务端。';bar.append(text);
    const exp=document.createElement('button');exp.textContent='导出数据 JSON';exp.onclick=()=>download(state,'atoms-offline-data.json');bar.append(exp);
    const label=document.createElement('label');label.textContent='导入 JSON ';const input=document.createElement('input');input.type='file';input.accept='.json';label.append(input);bar.append(label);
    input.onchange=async()=>{const file=input.files[0];if(!file)return;try{if(file.size>270000)throw Error('文件过大');const incoming=JSON.parse(await file.text());valid(incoming.value);if(incoming.schema_version!==config.schema)throw Error('数据格式不兼容');if(!confirm('将替换本地数据，先下载当前数据备份。继续？'))return;download(state,'atoms-before-import.json');const next={...state,value:incoming.value,revision:state.revision+1};if(durable)localStorage.setItem(key,JSON.stringify(next));state=next;alert('导入完成，请点应用中的重新加载；刷新页面仅在本地缓存可用时保留。');}catch(error){alert(error.message);}input.value='';};
    document.body.prepend(bar);
  });
})();
