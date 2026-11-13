import {useState} from 'react';
import {apiJSON} from '../api';
type Link={id:string;version:string;created:string};
export function SharePanel({projectId,version}:{projectId:string;version:string}){
  const[open,setOpen]=useState(false),[links,setLinks]=useState<Link[]>([]),[url,setURL]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const base=`/api/projects/${projectId}/shares`;
  async function api(path='',method='GET',body?:unknown){return apiJSON(base+path,{method,body:body===undefined?undefined:JSON.stringify(body)});}
  async function refresh(){setLinks(await api());}
  return <><button className="share-button" onClick={()=>{setOpen(true);setError('');refresh().catch(error=>setError(error.message));}}>分享</button>{open&&<div className="modal-backdrop"><section className="modal compact" role="dialog" aria-modal="true" aria-label="分享应用"><button className="close" onClick={()=>setOpen(false)} aria-label="关闭分享">×</button><h2>分享已交付版本</h2><p>访客可体验页面和示例数据。项目的业务记录、对话和输入原件不会随链接公开；页面本身的内容会公开。草稿将使用当前已交付版本。</p><button disabled={busy} onClick={async()=>{setBusy(true);setError('');try{const out=await api('','POST',{version});setURL(location.origin+out.path);await refresh();}catch(error){setError((error as Error).message);}finally{setBusy(false);}}}>创建分享链接</button>{url&&<label>分享地址<input readOnly value={url} onFocus={event=>event.target.select()}/><button onClick={()=>navigator.clipboard.writeText(url).catch(()=>setError('请选中地址后手动复制'))}>复制地址</button><a href={url} target="_blank" rel="noreferrer">打开分享页</a></label>}<ul className="share-list">{links.map(link=><li key={link.id}><span>{new Date(link.created).toLocaleString()}</span><button onClick={async()=>{try{await api('/'+link.id,'DELETE');setURL('');await refresh();}catch(error){setError((error as Error).message);}}}>撤销</button></li>)}</ul>{error&&<p role="alert" className="form-error">{error}</p>}</section></div>}</>;
}
