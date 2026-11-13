export type Account = { id: string; username: string };
export type Bootstrap = { user: Account | null; csrf: string; model_ready: boolean; runner_configured: boolean; notice: string };
export class ApiError extends Error {
  constructor(message: string, public status: number, public code = '') { super(message); }
}
let csrf = '';
let identity = '';
let generation = 0;
const pending = new Set<AbortController>();
function cancelRequests() {
  generation++;
  for (const controller of pending) controller.abort();
  pending.clear();
}
export function setSession(boot: Bootstrap) {
  const next = `${boot.user?.id || ''}:${boot.csrf}`;
  if (identity !== next) cancelRequests();
  identity = next; csrf = boot.csrf;
}
export function invalidateSession(reason = 'expired') {
  cancelRequests(); identity = ''; csrf = '';
  window.dispatchEvent(new CustomEvent('atoms:session-invalid', { detail: reason }));
}

// The session fence also covers downloads: delayed responses from an old
// account cannot repopulate a newly signed-in workspace.
async function request(path: string, init: RequestInit, blob: boolean) {
  const epoch = generation;
  const controller = new AbortController();
  pending.add(controller);
  const headers = new Headers(init.headers);
  if (init.body) headers.set('Content-Type', 'application/json');
  if (init.method && !['GET', 'HEAD'].includes(init.method)) headers.set('X-CSRF-Token', csrf);
  const url = path.startsWith('/api/') ? path : '/api' + path;
  try {
    const response = await fetch(url, { ...init, headers, credentials: 'same-origin', signal: init.signal ? AbortSignal.any([init.signal, controller.signal]) : controller.signal });
    const data = response.ok && blob ? await response.blob() : await response.json();
    if (epoch !== generation) throw new DOMException('账号状态已变化', 'AbortError');
    if (!response.ok) {
      const error = new ApiError(data.error || `请求失败 (${response.status})`, response.status, data.code);
      if (response.status === 401 && data.code !== 'invalid_credentials') invalidateSession();
      if (response.status === 404 && (data.code === 'project_not_found' || /^\/api\/projects\/[a-f0-9]{32}$/.test(url))) window.dispatchEvent(new CustomEvent('atoms:project-missing', { detail: url.split('/')[3].split('?')[0] }));
      throw error;
    }
    return data;
  } finally { pending.delete(controller); }
}
export function apiJSON(path: string, init: RequestInit = {}) { return request(path, init, false); }
export function api(path: string, body?: unknown, method = 'POST') {
  return apiJSON(path, body === undefined ? {} : { method, body: JSON.stringify(body) });
}
export async function downloadFile(path: string, name: string) {
  const blob = await request(path, {}, true);
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a'); link.href = url; link.download = name; link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
export function safeReturnTo(raw: string | null): string {
  if (!raw || raw.startsWith('//') || raw.includes('\\')) return '/';
  try {
    const url = new URL(raw, location.origin);
    if (url.origin !== location.origin || url.hash) return '/';
    if (/^\/share\/[a-f0-9]{64}$/.test(url.pathname) && !url.search) return url.pathname;
    if (url.pathname !== '/') return '/';
    const id = url.searchParams.get('project');
    if (id && /^[a-f0-9]{32}$/.test(id) && [...url.searchParams.keys()].length === 1) return `/?project=${id}`;
    return '/';
  } catch { return '/'; }
}
export type Draft = { user: string; text: string; mode: string; project?: string };
export function readDraft(user: string): Draft | undefined {
  try {
    const draft = JSON.parse(sessionStorage.getItem('atoms:draft') || 'null');
    if (draft?.user === user && typeof draft.text === 'string' && ['build','plan'].includes(draft.mode)) return draft;
    sessionStorage.removeItem('atoms:draft');
  } catch { /* Storage can be disabled by the browser. */ }
}
export function saveDraft(draft: Draft) { try { sessionStorage.setItem('atoms:draft', JSON.stringify(draft)); } catch { /* Optional browser storage. */ } }
export function clearDraft() { try { sessionStorage.removeItem('atoms:draft'); } catch { /* Optional browser storage. */ } }
export function recentProject(user: string) { try { return localStorage.getItem(`atoms:last:${user}`); } catch { return null; } }
export function rememberProject(user: string, id: string) { try { if (id) localStorage.setItem(`atoms:last:${user}`, id); else localStorage.removeItem(`atoms:last:${user}`); } catch { /* Optional browser storage. */ } }
