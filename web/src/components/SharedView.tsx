import {useEffect,useState} from 'react';
import {AppPreview} from './AppPreview';
export function SharedView({token}:{token:string}){
  const[title,setTitle]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  useEffect(()=>{fetch('/api/shares/'+encodeURIComponent(token)).then(async response=>{const data=await response.json();if(!response.ok)throw Error(data.error);setTitle(data.title);}).catch(error=>setError(error.message));},[token]);
  return <main className="shared-view"><header><a href="/">Atoms Lab</a><h1>{title||'分享应用'}</h1><button disabled={!title||busy} onClick={async()=>{setBusy(true);try{const boot=await fetch('/api/bootstrap').then(r=>r.json());const response=await fetch(`/api/shares/${encodeURIComponent(token)}/copy`,{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':boot.csrf},body:JSON.stringify({request_id:crypto.randomUUID()})});const data=await response.json();if(!response.ok)throw Error(data.error);localStorage.setItem('atoms:last',data.id);location.href='/';}catch(error){setError((error as Error).message);setBusy(false);}}}>保存为我的副本</button></header><p>公开版本 · 当前修改仅为试用，不会影响分享者。创建自己的副本后可长期保存。</p>{error&&<p role="alert">{error}</p>}{title&&<AppPreview projectId="" version="" csrf="" sharedToken={token}/>}</main>;
}
