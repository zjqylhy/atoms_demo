import { useState } from "react";

export type Account = { id: string; username: string };

export function AccountMenu({ user, csrf, onChange }: { user?: Account | null; csrf: string; onChange: () => Promise<void> }) {
  const [mode, setMode] = useState<"login" | "register">();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [importGuest, setImportGuest] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(action: string) {
    setBusy(true); setError("");
    try {
      const response = await fetch(`/api/auth/${action}`, { method: "POST", headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf }, body: JSON.stringify(action === "logout" ? {} : { username, password, import_guest: mode === "login" && importGuest }) });
      const result = await response.json();
      if (!response.ok) throw Error(result.error || "账号操作失败");
      setPassword(""); setMode(undefined); await onChange();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <div className="account-menu">
    {user ? <><span>{user.username}</span><button disabled={busy} onClick={() => submit("logout")}>退出</button></> : <button onClick={() => { setError(""); setMode("login"); }}>登录 / 注册</button>}
    {error && !mode && <span role="alert">{error}</span>}
    {mode && <div className="modal-backdrop"><form className="modal compact account-dialog" role="dialog" aria-modal="true" aria-label={mode === "login" ? "登录账号" : "注册账号"} onSubmit={e => { e.preventDefault(); void submit(mode); }}>
      <h2>{mode === "login" ? "登录账号" : "保留你的作品"}</h2>
      <p>项目与已保存的应用数据保存在服务端，登录后可在其他设备继续使用。</p>
      <label>用户名<input autoFocus autoComplete="username" required minLength={3} maxLength={32} pattern="[A-Za-z0-9_]{3,32}" value={username} onChange={e => setUsername(e.target.value)} placeholder="3～32 位字母、数字或下划线" /></label>
      <label>密码<input type="password" autoComplete={mode === "login" ? "current-password" : "new-password"} required minLength={mode === "register" ? 12 : undefined} maxLength={128} value={password} onChange={e => setPassword(e.target.value)} placeholder="至少 12 个字符" /></label>
      {mode === "login" ? <label className="check-option"><input type="checkbox" checked={importGuest} onChange={e => setImportGuest(e.target.checked)} />接收当前浏览器的游客项目</label> : <p>注册将把当前游客项目关联到新账号，原有数据会保留。</p>}
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="actions"><button type="button" disabled={busy} onClick={() => { setMode(undefined); setPassword(""); }}>取消</button><button disabled={busy}>{busy ? "处理中…" : mode === "login" ? "登录" : "注册"}</button></div>
      <button type="button" className="text-button" disabled={busy} onClick={() => { setMode(mode === "login" ? "register" : "login"); setError(""); }}>{mode === "login" ? "还没有账号？注册" : "已有账号？登录"}</button>
    </form></div>}
  </div>;
}
