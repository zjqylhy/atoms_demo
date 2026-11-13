import {useEffect,useState} from 'react';
import {AppPreview} from './AppPreview';
import {useAuth} from '../Auth';
import {api, rememberProject} from '../api';
export function SharedView({token}:{token:string}){
  const {boot, requireLogin} = useAuth();
  const[title,setTitle]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  useEffect(()=>{api('/shares/'+encodeURIComponent(token)).then(data=>setTitle(data.title)).catch(error=>setError(error.message));},[token]);
  return <main className="shared-view"><header><a href="/">Atoms Lab</a><h1>{title||'分享应用'}</h1><button disabled={!title||busy} onClick={async()=>{
    if(!boot?.user){requireLogin();return;}
    setBusy(true);setError('');
    try{const data=await api(`/shares/${encodeURIComponent(token)}/copy`,{request_id:crypto.randomUUID()});rememberProject(boot.user.id,data.id);location.href='/?project='+data.id;}
    catch(error){setError((error as Error).message);setBusy(false);}
  }}>{boot?.user?'创建共享副本':'登录后创建副本'}</button></header><p>公开版本 · 当前修改仅为试用，不会影响原项目。登录后可创建供所有账号使用的共享副本。</p>{error&&<p role="alert">{error}</p>}{title&&<AppPreview projectId="" version="" csrf="" sharedToken={token}/>}</main>;
}
