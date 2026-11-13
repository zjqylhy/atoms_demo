import { useEffect, useLayoutEffect, useRef, useState, type PointerEvent } from 'react';
import { ChevronRight, FolderOpen, GripVertical, Trash2 } from 'lucide-react';
import type { ProjectItem } from '../hooks/useProjectOrder';

type Drag = { id: string; pointer: number; startY: number; moved: boolean; items: ProjectItem[]; before: ProjectItem[] };
function move(items: ProjectItem[], id: string, index: number) {
  const item = items.find(value => value.id === id)!;
  const next = items.filter(value => value.id !== id);
  next.splice(index, 0, item);
  return next;
}

export function ProjectList({ items, selected, saving, notice, onOpen, onDelete, onBegin, onCancel, onSave }: {
  items: ProjectItem[]; selected?: string; saving: boolean; notice: string;
  onOpen: (id: string) => void; onDelete: (item: ProjectItem) => void;
  onBegin: () => boolean; onCancel: () => void; onSave: (items: ProjectItem[]) => Promise<void>;
}) {
  const nav = useRef<HTMLElement>(null), drag = useRef<Drag | undefined>(undefined);
  const keyboardFocus = useRef<HTMLButtonElement | undefined>(undefined);
  const [preview, setPreview] = useState<ProjectItem[]>();
  const [dragged, setDragged] = useState('');
  useLayoutEffect(() => {
    const handle = keyboardFocus.current;
    // Reordering can detach the focused row. Keep keyboard navigation on the
    // same handle, including the server response, unless the user moved focus.
    if (handle?.isConnected) {
      if (document.activeElement === document.body || document.activeElement === handle) handle.focus({ preventScroll: true });
      else keyboardFocus.current = undefined;
    }
    if (!saving) keyboardFocus.current = undefined;
  }, [items, saving]);
  useEffect(() => {
    const pointer = () => { keyboardFocus.current = undefined; };
    document.addEventListener('pointerdown', pointer, true);
    return () => document.removeEventListener('pointerdown', pointer, true);
  }, []);
  useEffect(() => {
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && drag.current) { event.preventDefault(); end(undefined, true); }
    };
    window.addEventListener('keydown', escape);
    return () => window.removeEventListener('keydown', escape);
  }, [onCancel]);
  function end(event?: PointerEvent<HTMLElement>, cancel = false) {
    const state = drag.current;
    if (!state || (event && event.pointerId !== state.pointer)) return;
    drag.current = undefined; setPreview(undefined); setDragged('');
    if (nav.current?.hasPointerCapture(state.pointer)) nav.current.releasePointerCapture(state.pointer);
    if (!cancel && state.moved && state.items.some((item, i) => state.before[i].id !== item.id)) void onSave(state.items);
    else onCancel();
  }
  function start(event: PointerEvent<HTMLButtonElement>, id: string) {
    if (event.button !== 0 || !event.isPrimary || items.length < 2 || !nav.current || !onBegin()) return;
    event.preventDefault(); event.currentTarget.focus();
    // Capture on the stable list, not a row React moves while previewing order.
    nav.current.setPointerCapture(event.pointerId);
    drag.current = { id, pointer: event.pointerId, startY: event.clientY, moved: false, items, before: items };
  }
  function update(event: PointerEvent<HTMLElement>) {
    const state = drag.current;
    if (!state || state.pointer !== event.pointerId || !nav.current) return;
    if (!state.moved && Math.abs(event.clientY - state.startY) < 4) return;
    state.moved = true; setDragged(state.id);
    const area = nav.current.getBoundingClientRect();
    if (event.clientY < area.top + 28) nav.current.scrollTop -= 18;
    else if (event.clientY > area.bottom - 28) nav.current.scrollTop += 18;
    const rows = [...nav.current.querySelectorAll<HTMLElement>('.project-row')];
    // Choose the nearest visible slot, so mouse and touch share the same behavior.
    let target = 0, distance = Infinity;
    rows.forEach((row, i) => { const rect = row.getBoundingClientRect(); const next = Math.abs(event.clientY - (rect.top + rect.height / 2)); if (next < distance) { target = i; distance = next; } });
    state.items = move(state.items, state.id, target); setPreview(state.items);
  }
  return <>
    <nav ref={nav} aria-label="共享项目列表" aria-busy={saving}
      onPointerMove={update} onPointerUp={event => end(event)} onPointerCancel={event => end(event, true)}
      onLostPointerCapture={event => { if (event.target === event.currentTarget) end(event, true); }}>
      {(preview || items).map((item, index) => <div className={`project-row${dragged === item.id ? ' dragging' : ''}`} key={item.id} data-project-id={item.id}>
        <button className="project-drag" aria-label={`拖动排序：${item.title}`} aria-keyshortcuts="ArrowUp ArrowDown" title="拖动排序，或按 ↑ / ↓ 移动；所有账号共用此顺序" disabled={items.length < 2} aria-disabled={saving || items.length < 2}
          onPointerDown={event => start(event, item.id)}
          onClick={event => { event.preventDefault(); event.stopPropagation(); }}
          onKeyDown={event => {
            if (!['ArrowUp', 'ArrowDown'].includes(event.key) || drag.current) return;
            event.preventDefault(); const target = index + (event.key === 'ArrowUp' ? -1 : 1);
            if (target >= 0 && target < items.length && onBegin()) { keyboardFocus.current = event.currentTarget; void onSave(move(items, item.id, target)); }
          }}><GripVertical size={15}/></button>
        <button className={item.id === selected ? 'project active' : 'project'} onClick={() => onOpen(item.id)}><FolderOpen size={16}/><span>{item.title}</span><ChevronRight size={14}/></button>
        <button className="project-delete" aria-label={`删除项目：${item.title}`} title="删除整个项目与历史" onClick={() => onDelete(item)}><Trash2 size={15}/></button>
      </div>)}
    </nav>
    <div className="project-order-status" role="status">{notice || '拖动手柄调整所有账号的共享顺序'}</div>
  </>;
}
