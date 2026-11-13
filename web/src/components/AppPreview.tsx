import { useEffect, useRef, useState } from 'react';

type AppState = {schema_version:number; revision:number; value:Record<string,unknown>};
type Access = {mode:string; grant?:string; version:string; contract?:{schema_version:number; initial_state:Record<string,unknown>}; state?:AppState};
export function AppPreview({projectId,version,csrf}:{projectId:string;version:string;csrf:string}) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [instance] = useState(() => crypto.randomUUID());
  const [access,setAccess] = useState<Access>();
  const [status,setStatus] = useState('正在连接应用数据…');
  const [problem,setProblem] = useState(false);
  const [reload,setReload] = useState(0);
  const current = useRef<Access | undefined>(undefined);
  const trial = useRef<AppState | undefined>(undefined);
  const port = useRef<MessagePort | undefined>(undefined);
  const active = useRef(true);
  const pending = useRef(false);
  const receipts = useRef(new Map<string,{hash:string;revision:number}>());
  const base = `/api/projects/${projectId}`;
  async function api(path:string,init:RequestInit={}) {
    const response = await fetch(base+path,{...init,headers:{'Content-Type':'application/json','X-CSRF-Token':csrf,...init.headers}});
    const body = await response.json();
    if (!response.ok) throw Object.assign(new Error(body.error || '数据请求失败'),{status:response.status});
    return body;
  }
  async function grant(expectedVersion?:string) {
    const value:Access = await api('/preview-access',{method:'POST',body:JSON.stringify({version})});
    if (!active.current) throw new Error('预览已关闭');
    if(expectedVersion && (value.mode!=='formal' || value.version!==expectedVersion)) throw new Error('当前交付版本已改变，请刷新预览');
    current.current = value; setAccess(value);
    if (value.mode === 'trial' && !trial.current) trial.current = {schema_version:value.contract!.schema_version,revision:0,value:structuredClone(value.contract!.initial_state)};
    return value;
  }
  useEffect(() => {
    active.current = true;
    grant().then(value => {
      setStatus(value.mode === 'formal' ? '已连接服务端 · 保存后可刷新或跨设备恢复' : value.mode === 'trial' ? '试用数据 · 草稿和历史版本不会修改正式数据' : value.mode === 'incompatible' ? '数据格式不兼容，请切回兼容版本；正式数据已保留' : '旧版应用 · 尚未接入持久化，页面内的修改可能随刷新丢失');
      setProblem(value.mode === 'incompatible');
    }).catch(error => { if(active.current){setStatus(error.message);setProblem(true);} });
    return () => {active.current=false;port.current?.close();port.current=undefined;};
  },[projectId,version,csrf]);
  async function load() {
    const info = current.current!;
    if (info.mode === 'trial') return structuredClone(trial.current!);
    if (info.mode !== 'formal') throw new Error('此版本不能读取或修改正式数据');
    try { return await api('/app-state',{headers:{'X-Preview-Grant':info.grant!}}); }
    catch (error) {
      if ((error as {status:number}).status !== 403) throw error;
      const renewed = await grant(info.version);
      if (renewed.mode !== 'formal' || renewed.version !== info.version) throw new Error('当前交付版本已改变，请刷新预览');
      return api('/app-state',{headers:{'X-Preview-Grant':renewed.grant!}});
    }
  }
  async function save(value:Record<string,unknown>,expected:number,id:string,importing=false) {
    if (!value || Array.isArray(value) || typeof value!=='object' || new TextEncoder().encode(JSON.stringify(value)).length>256*1024 || !Number.isSafeInteger(expected) || expected<0 || typeof id!=='string' || id.length<8 || id.length>100) throw new Error('应用保存内容或修订号无效');
    const info=current.current!;
    if (info.mode === 'trial') {
      const hash=JSON.stringify([value,expected]); const receipt=receipts.current.get(id);
      if(receipt){if(receipt.hash!==hash) throw new Error('请求标识重复');return {revision:receipt.revision,saved:true};}
      if(trial.current!.revision!==expected) throw new Error('试用数据发生冲突，请重新加载');
      trial.current={...trial.current!,value:structuredClone(value),revision:expected+1};
      receipts.current.set(id,{hash,revision:expected+1});if(receipts.current.size>1000)receipts.current.delete(receipts.current.keys().next().value!);
      return {revision:expected+1,saved:true};
    }
    if(info.mode!=='formal') throw new Error('此版本不能修改正式数据');
    const perform=(g:string)=>api('/app-state',{method:'PUT',body:JSON.stringify({grant:g,value,expected_revision:expected,request_id:id,schema_version:info.contract!.schema_version,import:importing})});
    try { return await perform(info.grant!); }
    catch(error) {
      if((error as {status:number}).status!==403) throw error;
      const renewed=await grant(info.version);
      if(renewed.mode!=='formal' || renewed.version!==info.version) throw new Error('当前交付版本已改变，请刷新预览');
      return perform(renewed.grant!);
    }
  }
  function connect() {
    port.current?.close();
    if(!current.current?.contract || !frame.current?.contentWindow) return;
    const channel=new MessageChannel(); port.current=channel.port1;
    channel.port1.onmessage=async({data})=>{
      if(!active.current || port.current!==channel.port1 || typeof data?.id!=='string' || data.id.length>100) return;
      const reply=(body:object)=>{if(active.current && port.current===channel.port1)channel.port1.postMessage({id:data.id,...body});};
      if(pending.current){reply({error:'上一次数据操作尚未完成，请稍后重试',status:429});return;}
      pending.current=true;
      try {
        if(data.op!=='load' && data.op!=='save') throw new Error('不支持的数据操作');
        if(data.op==='save')setStatus('正在保存…');
        const result=data.op==='load'?await load():await save(data.value,data.expectedRevision,data.requestId);
        if(active.current){setProblem(false);setStatus(current.current?.mode==='formal'?'已保存到服务端':'已保存试用数据 · 切换项目后重置');}
        reply({result});
      } catch(error){const err=error as Error & {status?:number};if(active.current){setProblem(true);setStatus(err.message);}reply({error:err.message,status:err.status});}
      finally{pending.current=false;}
    };
    // The sandbox has an opaque origin. Only this window receives the port;
    // the instance nonce and child-side parent origin check complete the handshake.
    frame.current.contentWindow.postMessage({type:'atoms-connect',instance},'*',[channel.port2]);
  }
  function download(value:unknown,name:string) {
    const url=URL.createObjectURL(new Blob([JSON.stringify(value,null,2)],{type:'application/json'}));
    const link=document.createElement('a');link.href=url;link.download=name;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
  }
  return <div className="app-preview">
    <div className={'app-data-bar '+(problem?'error':'')} role="status"><span>{status}</span>
      {access?.contract && <><button onClick={()=>load().then(data=>download(data,'atoms-data.json')).catch(error=>{setStatus(error.message);setProblem(true);})}>导出数据</button>
        <label className="data-import">导入数据<input type="file" accept=".json,application/json" hidden onChange={async event=>{
          const file=event.target.files?.[0];event.target.value='';if(!file)return;
          try {if(file.size>270000)throw new Error('文件超过大小限制');const value=JSON.parse(await file.text()) as AppState;
            if(value.schema_version!==access.contract!.schema_version)throw new Error('导入的数据格式版本不兼容');
            if(!confirm('导入将替换当前应用数据，服务端会保留最近 3 次导入前备份。确认继续？'))return;
            const before=await load();await save(value.value,before.revision,crypto.randomUUID(),true);setReload(n=>n+1);
          }catch(error){setStatus((error as Error).message);setProblem(true);}
        }}/></label></>}
      {access?.mode==='formal' && <button onClick={()=>api('/app-backups').then(data=>download(data,'atoms-data-backups.json')).catch(error=>setStatus(error.message))}>下载备份</button>}
    </div>
    {access && <iframe ref={frame} key={reload} title="生成产物预览" sandbox="allow-scripts allow-forms" onLoad={connect} src={`${base}/preview?version=${encodeURIComponent(version)}&instance=${instance}`}/>}
  </div>;
}
