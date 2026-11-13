import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { api, apiJSON, clearDraft, invalidateSession, readDraft, safeReturnTo, setSession, type Bootstrap } from './api';

type Auth = { boot?: Bootstrap; loading: boolean; error: string; loginRequired: boolean; refresh: () => Promise<void>; login: (mode: 'login' | 'register', username: string, password: string) => Promise<void>; logout: () => Promise<void>; requireLogin: () => void };
const AuthContext = createContext<Auth | null>(null);
export function useAuth() { const auth = useContext(AuthContext); if (!auth) throw Error('缺少账号状态'); return auth; }
function announce(reason = 'login') { try { localStorage.setItem('atoms:auth-change', reason + ':' + crypto.randomUUID()); } catch { /* Focus revalidation remains available. */ } }
function loginLocation() {
  const current = location.pathname === '/login' ? new URLSearchParams(location.search).get('returnTo') : location.pathname + location.search;
  return '/login?returnTo=' + encodeURIComponent(safeReturnTo(current));
}
export function AuthProvider({ children }: { children: ReactNode }) {
  const [boot, setBoot] = useState<Bootstrap>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [loginRequired, setLoginRequired] = useState(location.pathname === '/login');
  const revision = useRef(0);
  const mounted = useRef(true);
  const refresh = useCallback(async () => {
    const turn = ++revision.current;
    try {
      const next: Bootstrap = await apiJSON('/bootstrap');
      if (!mounted.current || turn !== revision.current) return;
      setSession(next);
      if (next.user) readDraft(next.user.id);
      setBoot(next); setError(''); setLoading(false);
    } catch (e) {
      if (!mounted.current || turn !== revision.current || (e as Error).name === 'AbortError') return;
      setBoot(undefined); setLoading(false); setError('暂时无法连接账号服务，请重试。');
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    const invalid = (event: Event) => {
      if ((event as CustomEvent).detail === 'logout') clearDraft();
      setBoot(undefined); setLoading(true); setLoginRequired(true);
      history.replaceState(null, '', loginLocation());
      void refresh();
    };
    const sync = (event: StorageEvent) => { if (event.key === 'atoms:auth-change') invalidateSession(event.newValue?.startsWith('logout:') ? 'logout' : 'sync'); };
    const focus = () => { if (document.visibilityState === 'visible') void refresh(); };
    window.addEventListener('atoms:session-invalid', invalid);
    window.addEventListener('storage', sync);
    window.addEventListener('focus', focus);
    document.addEventListener('visibilitychange', focus);
    return () => { mounted.current = false; revision.current++; window.removeEventListener('atoms:session-invalid', invalid); window.removeEventListener('storage', sync); window.removeEventListener('focus', focus); document.removeEventListener('visibilitychange', focus); };
  }, [refresh]);
  async function login(mode: 'login' | 'register', username: string, password: string) {
    await api('/auth/' + mode, { username, password });
    await refresh(); announce();
    // Restore the page only; AI and copy requests always require a new click.
    location.replace(safeReturnTo(new URLSearchParams(location.search).get('returnTo') || (location.pathname === '/login' ? '/' : location.pathname + location.search)));
  }
  async function logout() { await api('/auth/logout', {}); clearDraft(); announce('logout'); invalidateSession('logout'); }
  function requireLogin() { setLoginRequired(true); history.replaceState(null, '', loginLocation()); }
  return <AuthContext.Provider value={{ boot, loading, error, loginRequired, refresh, login, logout, requireLogin }}>{children}</AuthContext.Provider>;
}
