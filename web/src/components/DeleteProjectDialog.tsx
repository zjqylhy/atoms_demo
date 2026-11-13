import { useEffect, useRef, useState } from 'react';
import { Trash2 } from 'lucide-react';
import { api, ApiError } from '../api';

export function DeleteProjectDialog({ project, onCancel, onDeleted }: { project: {id:string;title:string}; onCancel: () => void; onDeleted: (id:string) => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => { const node = dialog.current!; node.showModal(); return () => node.close(); }, []);
  async function remove() {
    if (busy) return;
    setBusy(true); setError('');
    try { await api('/projects/' + project.id, {}, 'DELETE'); onDeleted(project.id); }
    catch (e) {
      if (e instanceof ApiError && e.status === 404) onDeleted(project.id);
      else setError((e as Error).message);
    } finally { setBusy(false); }
  }
  return <dialog className="delete-dialog" ref={dialog} aria-labelledby="delete-project-title" aria-describedby="delete-project-detail" onCancel={event => { event.preventDefault(); if (!busy) onCancel(); }}>
    <span className="delete-dialog-icon"><Trash2 size={23}/></span>
    <h2 id="delete-project-title">删除项目？</h2>
    <p className="delete-project-name">{project.title}</p>
    <p id="delete-project-detail">将删除整个项目、对话与任务历史、文件、应用数据及分享链接。此操作会影响所有登录账号，且无法撤销。</p>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="modal-actions"><button autoFocus disabled={busy} onClick={onCancel}>取消</button><button className="danger" disabled={busy} onClick={() => void remove()}>{busy ? '正在删除…' : '确认删除'}</button></div>
  </dialog>;
}
