import { useEffect, useRef, useState } from 'react';

import {apiJSON} from '../api';

type AppState = {schema_version:number; revision:number; value:Record<string,unknown>};
type Access = {mode:string; grant?:string; version:string; contract?:{schema_version:number; initial_state:Record<string,unknown>}; state?:AppState};
export function AppPreview({projectId,version,csrf,sharedToken,onReady,onFailure,requireFormal=false}:{projectId:string;version:string;csrf:string;sharedToken?:string;onReady?:()=>void;onFailure?:(error:Error)=>void;requireFormal?:boolean}) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [instance] = useState(() => crypto.randomUUID());
  const [access,setAccess] = useState<Access>();
  const [problem,setProblem] = useState('');
  const [reload,setReload] = useState(0);
  const current = useRef<Access | undefined>(undefined);
  const trial = useRef<AppState | undefined>(undefined);
  const port = useRef<MessagePort | undefined>(undefined);
  const active = useRef(true);
  const lifetime = useRef(0);
  const ready = useRef(false);
  const observers = useRef({onReady,onFailure}); observers.current = {onReady,onFailure};
  const pending = useRef(false);
  const receipts = useRef(new Map<string,{hash:string;revision:number}>());
  const base = sharedToken ? `/api/shares/${encodeURIComponent(sharedToken)}` : `/api/projects/${projectId}`;
  function api(path:string,init:RequestInit={}) { return apiJSON(base+path,init); }
  async function grant(expectedVersion?:string) {
    const turn = lifetime.current;
    const value:Access = sharedToken ? await api('') : await api('/preview-access',{method:'POST',body:JSON.stringify({version})});
    if (!active.current || turn !== lifetime.current) throw new DOMException('预览已关闭','AbortError');
    if(expectedVersion && (value.mode!=='formal' || value.version!==expectedVersion)) throw new Error('当前交付版本已改变，请刷新预览');
    current.current = value; setAccess(value);
    setProblem(value.mode === 'incompatible' ? '数据格式不兼容，请切回兼容版本；正式数据已保留' : '');
    if (value.mode === 'trial' && !trial.current) trial.current = {schema_version:value.contract!.schema_version,revision:0,value:structuredClone(value.contract!.initial_state)};
    return value;
  }
  useEffect(() => {
    active.current = true; lifetime.current++; ready.current = false;
    const turn = lifetime.current;
    grant().then(value => {
      if (!active.current || turn !== lifetime.current) return;
      if(requireFormal && value.mode!=='formal') initialFailure(new Error('演示版本暂不能连接正式应用数据'));
    }).catch(error => { if(active.current && turn === lifetime.current && error.name!=='AbortError'){setProblem(error.message);initialFailure(error);} });
    return () => {active.current=false;lifetime.current++;port.current?.close();port.current=undefined;};
  },[projectId,version,csrf]);
  function initialFailure(error:Error) { if(active.current && !ready.current) observers.current.onFailure?.(error); }
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
        const result=data.op==='load'?await load():await save(data.value,data.expectedRevision,data.requestId);
        if(!active.current || port.current!==channel.port1) return;
        setProblem('');
        reply({result});
        // A loaded iframe may contain an error page. The first data reply proves
        // that this sandbox instance connected to the platform successfully.
        if(data.op==='load' && !ready.current){ready.current=true;observers.current.onReady?.();}
      } catch(error){const err=error as Error & {status?:number};if(active.current && port.current===channel.port1){setProblem(err.message);initialFailure(err);}reply({error:err.message,status:err.status});}
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
  // Keep data controls and actionable errors without a permanent status notice.
  const showDataBar = Boolean(problem || access?.contract || access?.mode==='formal' || access?.mode==='incompatible');
  return <div className="app-preview">
    {showDataBar && <div className={'app-data-bar '+(problem?'error':'')}>
      {problem && <span role="alert">{problem}</span>}
      {access?.mode==='incompatible' && <><button onClick={()=>download(access.state,'atoms-data-before-reset.json')}>导出原数据</button><button onClick={async()=>{
        if(!confirm('新代码的数据格式与原数据不兼容。确认备份原数据并使用新版初始数据？原数据保留在最近 3 次备份中。'))return;
        try{await api('/app-state/reset',{method:'POST',body:JSON.stringify({version:access.version,expected_revision:access.state!.revision,request_id:crypto.randomUUID()})});await grant();setReload(n=>n+1);}catch(error){setProblem((error as Error).message);}
      }}>备份并重置为新版</button></>}
      {access?.contract && access.mode!=='incompatible' && <><button onClick={()=>load().then(data=>download(data,'atoms-data.json')).catch(error=>setProblem(error.message))}>导出数据</button>
        <label className="data-import">导入数据<input type="file" accept=".json,application/json" hidden onChange={async event=>{
          const file=event.target.files?.[0];event.target.value='';if(!file)return;
          try {if(file.size>270000)throw new Error('文件超过大小限制');const value=JSON.parse(await file.text()) as AppState;
            if(value.schema_version!==access.contract!.schema_version)throw new Error('导入的数据格式版本不兼容');
            if(!confirm(access.mode==='formal'?'导入将替换当前应用数据，服务端会保留最近 3 次导入前备份。确认继续？':'导入将替换当前试用数据，请先导出需要保留的内容。确认继续？'))return;
            const before=await load();await save(value.value,before.revision,crypto.randomUUID(),true);setReload(n=>n+1);
          }catch(error){setProblem((error as Error).message);}
        }}/></label></>}
      {(access?.mode==='formal'||access?.mode==='incompatible') && <button onClick={()=>api('/app-backups').then(data=>download(data,'atoms-data-backups.json')).catch(error=>setProblem(error.message))}>下载备份</button>}
    </div>}
    {access && <iframe ref={frame} key={reload} title="生成产物预览" sandbox="allow-scripts allow-forms" onLoad={connect} src={`${base}/preview?version=${encodeURIComponent(version)}&instance=${instance}`}/>}
  </div>;
}
