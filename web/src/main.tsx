import React from 'react';
import {createRoot} from 'react-dom/client';
import App from './App';
import {SharedView} from './components/SharedView';
import {LoginPage} from './components/LoginPage';
import {AuthProvider, useAuth} from './Auth';
import './style.css';
function Route() {
  const {boot, loading, error, loginRequired, refresh} = useAuth();
  const share = location.pathname.match(/^\/share\/([a-f0-9]{64})$/);
  if (loading) return <main className="session-screen" role="status">正在确认登录状态…</main>;
  if (error) return <main className="session-screen"><p role="alert">{error}</p><button onClick={() => void refresh()}>重试</button></main>;
  if (share && !loginRequired) return <SharedView token={share[1]}/>;
  if (!boot?.user) return <LoginPage/>;
  return <App key={`${boot.user.id}:${boot.csrf}`}/>;
}
createRoot(document.getElementById('root')!).render(<React.StrictMode><AuthProvider><Route/></AuthProvider></React.StrictMode>);
