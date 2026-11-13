import {useEffect,useState} from 'react';
import {api} from '../api';
type Item={id:string;title:string;description:string};
export function Catalog({csrf,onOpen}:{csrf:string;onOpen:(id:string)=>Promise<void>}){
  const[items,setItems]=useState<Item[]>([]);const[error,setError]=useState('');const[busy,setBusy]=useState('');
  useEffect(()=>{api('/catalog').then(setItems).catch(error=>setError(error.message));},[]);
  return <section className="catalog"><div className="catalog-title"><b>先体验一个成品</b><span>预置示例 · 无需模型 · 副本支持保存</span></div><div className="catalog-cards">{items.map((item,index)=><button key={item.id} disabled={!!busy||!csrf} onClick={async()=>{
    setBusy(item.id);setError('');try{const data=await api(`/catalog/${item.id}/copy`,{request_id:crypto.randomUUID()});await onOpen(data.id);}catch(error){setError((error as Error).message);}finally{setBusy('');}
  }}><div className={'catalog-art art-'+index} aria-hidden="true">{index===0?<><i/><i/><i/></>:index===1?<><i/><i/><i/><i/></>:<><strong>1.05<sup>10</sup></strong><i/></>}</div><b>{item.title}</b><p>{item.description}</p><span>{busy===item.id?'正在创建副本…':'创建共享副本 →'}</span></button>)}</div>{error&&<p role="alert" className="form-error">{error}</p>}</section>;
}
