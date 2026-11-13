import { useState } from 'react';
import { ArrowRight, Layers3, Sparkles } from 'lucide-react';
import { useAuth } from '../Auth';

export function LoginPage() {
  const { login, boot } = useAuth();
  const [mode, setMode] = useState<'login' | 'register'>('login');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  return <main className="login-page">
    <section className="login-form-panel">
      <a className="login-brand" href="/" aria-label="Atoms Lab 首页"><span><Layers3 size={21}/></span> Atoms Lab</a>
      <form className="login-form" onSubmit={async event => {
        event.preventDefault(); if (busy) return;
        setBusy(true); setError('');
        try { await login(mode, username, password); }
        catch (e) { if ((e as Error).name !== 'AbortError') setError((e as Error).message); setBusy(false); }
      }}>
        <div className="login-eyebrow">从一个想法开始</div>
        <h1>{mode === 'login' ? '欢迎回来' : '创建你的账号'}</h1>
        <p className="login-intro">登录后，与 AI 一起把想法变成作品。</p>
        <label htmlFor="login-username">用户名</label>
        <input id="login-username" autoFocus autoComplete="username" required minLength={3} maxLength={32} pattern="[A-Za-z0-9_]{3,32}" placeholder="3～32 位字母、数字或下划线" value={username} onChange={e => setUsername(e.target.value)} disabled={busy}/>
        <label htmlFor="login-password">密码</label>
        <input id="login-password" type="password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} required minLength={mode === 'register' ? 12 : undefined} maxLength={128} placeholder={mode === 'register' ? '至少 12 个字符' : '输入密码'} value={password} onChange={e => setPassword(e.target.value)} disabled={busy}/>
        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="login-submit" type="submit" disabled={busy || !boot?.csrf}>{busy ? '正在连接…' : mode === 'login' ? '登录' : '注册并进入'}<ArrowRight size={18}/></button>
        <p className="login-switch">{mode === 'login' ? '还没有账号？' : '已经有账号？'}<button type="button" disabled={busy} onClick={() => {setMode(mode === 'login' ? 'register' : 'login'); setError(''); setPassword('');}}>{mode === 'login' ? '创建账号' : '立即登录'}</button></p>
        <p className="login-sharing"><Layers3 size={16}/><span>共享工作空间<br/>所有登录账号均可查看、修改和删除项目。</span></p>
      </form>
      <footer>Atoms Lab · 让想法有迹可循</footer>
    </section>
    <aside className="login-story" aria-label="Atoms Lab 产品介绍">
      <div className="login-story-top"><Sparkles size={19}/> 从想法，到可用的作品</div>
      <h2>把灵感写下来，<br/>让下一步发生。</h2>
      <p>用自然语言描述需求，和 AI 一起完成<br/>应用、文档与数据分析。</p>
      <div className="login-product" aria-hidden="true"><div className="login-product-bar"><i/><i/><i/><span>共享工作空间</span></div><div className="login-product-body"><div className="login-sample-message">做一个简洁的阅读清单</div><div className="login-sample-reply"><Sparkles size={18}/><span>让每一个想法，成为可以继续的项目。</span></div><div className="login-sample-board"><b>我的阅读清单</b><span>设计中的设计 <em>正在阅读</em></span><span>如何阅读一本书 <em>待阅读</em></span><div>＋ 添加一本书</div></div></div></div>
      <div className="login-story-bottom"><span>创建项目</span><span>持续迭代</span><span>保留成果</span></div>
    </aside>
  </main>;
}
