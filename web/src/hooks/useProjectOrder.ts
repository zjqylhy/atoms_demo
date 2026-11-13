import { useEffect, useRef, useState } from 'react';
import { apiJSON, ApiError } from '../api';

export type ProjectItem = { id: string; title: string };
type Order = { revision: number; projects: ProjectItem[] };

export function useProjectOrder() {
  const [projects, setProjects] = useState<ProjectItem[]>([]);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState('');
  const current = useRef<Order>({ revision: 0, projects: [] });
  const alive = useRef(true), sequence = useRef(0);
  const editing = useRef(false), writing = useRef(false), pending = useRef(false);

  function apply(order: Order) { current.current = order; setProjects(order.projects); }
  async function refresh() {
    if (editing.current || writing.current) { pending.current = true; return current.current.projects; }
    const request = ++sequence.current;
    const order: Order = await apiJSON('/project-order');
    if (alive.current && request === sequence.current && !editing.current && !writing.current) apply(order);
    return order.projects;
  }
  function begin() {
    if (editing.current || writing.current) return false;
    editing.current = true; sequence.current++; setNotice('');
    return true;
  }
  function finish() {
    editing.current = false;
    if (pending.current) {
      pending.current = false;
      void refresh().catch(error => { if (alive.current && error.name !== 'AbortError') setNotice('暂时无法刷新共享顺序，请稍后重试。'); });
    }
  }
  async function save(items: ProjectItem[]) {
    if (writing.current) return;
    editing.current = false; writing.current = true; sequence.current++;
    const before = current.current;
    setSaving(true); setNotice('正在保存共享顺序…'); setProjects(items);
    try {
      const order: Order = await apiJSON('/project-order', { method: 'PUT', body: JSON.stringify({ revision: before.revision, ids: items.map(item => item.id) }) });
      if (alive.current) { apply(order); setNotice('共享顺序已保存'); }
    } catch (error) {
      if (!alive.current || (error as Error).name === 'AbortError') return;
      const message = error instanceof ApiError && error.status === 409 ? '顺序或项目列表已被其他人修改，已加载最新顺序。' : `排序未保存：${(error as Error).message}。已恢复服务端顺序。`;
      try {
        const latest: Order = await apiJSON('/project-order');
        if (alive.current) { apply(latest); setNotice(message); }
      } catch {
        if (alive.current) { apply(before); setNotice('排序保存结果暂时无法确认，已恢复拖动前的列表；请联网后刷新。'); }
      }
    } finally {
      writing.current = false;
      if (alive.current) { setSaving(false); finish(); }
    }
  }
  function remove(id: string) {
    sequence.current++;
    apply({ ...current.current, projects: current.current.projects.filter(item => item.id !== id) });
  }
  useEffect(() => {
    alive.current = true;
    const focus = () => {
      if (document.visibilityState === 'visible') void refresh().catch(error => {
        if (alive.current && error.name !== 'AbortError') setNotice('暂时无法刷新共享顺序，请稍后重试。');
      });
    };
    window.addEventListener('focus', focus); document.addEventListener('visibilitychange', focus);
    return () => { alive.current = false; sequence.current++; window.removeEventListener('focus', focus); document.removeEventListener('visibilitychange', focus); };
  }, []);
  return { projects, saving, notice, refresh, begin, finish, save, remove };
}
