import { useState } from 'react';
import { useAuth } from '../Auth';

export function AccountMenu() {
  const { boot, logout } = useAuth();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  return <div className="account-menu"><span>{boot?.user?.username}</span><button disabled={busy} onClick={async () => {
    setBusy(true); setError('');
    try { await logout(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }}>{busy ? '退出中…' : '退出'}</button>{error && <span role="alert">{error}</span>}</div>;
}
