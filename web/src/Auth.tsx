import { useState, type FormEvent } from 'react';
import { api, ApiError, saveSession, type Session } from './api';
import { Icon } from './icons';

export const A = '/assets/ui/';

export function messageFor(error: unknown): string {
  if (!(error instanceof ApiError)) return '请求失败，请稍后重试。';
  switch (error.code) {
    case 'INVALID_CREDENTIALS': return '手机号或密码不正确，请重新输入。';
    case 'ACCOUNT_UNAVAILABLE': return '账户暂不可用，请联系管理员。';
    case 'PASSWORD_CHANGE_REQUIRED': return '请先修改临时密码。';
    case 'SERVICE_UNAVAILABLE': return '服务暂不可用，请稍后重试。';
    case 'REFRESH_TOO_SOON': return '就餐码更新过于频繁，请稍后再试。';
    case 'ENTRANCE_DISABLED': return '食堂已关闭就餐码消费，请改用其他消费模式。';
    default: return error.status === 404 ? '所需服务尚未开放，请稍后再试。' : error.message;
  }
}

export function Brand({ light = false }: { light?: boolean }) {
  return <div className={`brand ${light ? 'brand-light' : ''}`}>
    <img src={`${A}app-logo.png`} alt="食堂储值卡标识" />
    <span><strong>食堂储值卡</strong><small>消费系统</small></span>
  </div>;
}

export function Login({ onLogin }: { onLogin: (session: Session) => void }) {
  const [phone, setPhone] = useState('');
  const [password, setPassword] = useState('');
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError('');
    if (!/^1[3-9]\d{9}$/.test(phone.trim())) { setError('请输入正确的 11 位手机号。'); return; }
    if (!password) { setError('请输入密码。'); return; }
    setBusy(true);
    try {
      const session = await api.login(phone.trim(), password);
      saveSession(session);
      onLogin(session);
    } catch (reason) { setError(messageFor(reason)); }
    finally { setBusy(false); }
  }

  return <main className="login-page">
    <section className="login-scene" style={{ backgroundImage: `url(${A}login-cafeteria-bg.png)` }}>
      <div className="login-scene-content">
        <img className="login-logo" src={`${A}app-logo.png`} alt="食堂储值卡标识" />
        <h1>食堂储值卡<br/>消费系统</h1>
        <p>员工登录后出示动态就餐码，即可就餐</p>
        <div className="scene-benefits"><span><Icon name="check" size={18}/>余额可查</span><span><Icon name="check" size={18}/>安全消费</span></div>
      </div>
      <img className="login-art" src={`${A}phone-qr-illustration.png`} alt="手机出示就餐码插画" />
    </section>
    <section className="login-panel">
      <div className="login-card">
        <h2>欢迎登录</h2>
        <p className="subtle">使用管理员发放的手机号和密码</p>
        <form onSubmit={submit}>
          {error && <div className="alert alert-error" role="alert">{error}</div>}
          <label className="field"><span>手机号</span><div className="input-wrap"><Icon name="phone"/><input type="tel" inputMode="tel" autoComplete="username" value={phone} onChange={e => setPhone(e.target.value)} placeholder="请输入手机号" maxLength={11} required /></div></label>
          <label className="field"><span>密码</span><div className="input-wrap"><Icon name="lock"/><input type={show ? 'text' : 'password'} autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} placeholder="请输入密码" required/><button className="field-icon-btn" type="button" aria-label={show ? '隐藏密码' : '显示密码'} onClick={() => setShow(!show)}><Icon name={show ? 'eyeOff' : 'eye'}/></button></div></label>
          <button className="primary-button full" type="submit" disabled={busy}>{busy ? '正在登录…' : '登录'}</button>
        </form>
        <p className="forgot">忘记密码？请联系管理员重置</p>
        <div className="first-login"><Icon name="alert" size={20}/>首次登录请使用管理员分配的账号信息，并修改临时密码。</div>
      </div>
    </section>
    <div className="login-waves" style={{ backgroundImage: `url(${A}footer-waves.png)` }} aria-hidden="true"/>
  </main>;
}

export function PasswordChange({ session, forced, onChanged, onCancel, onExpired }: {
  session: Session; forced: boolean; onChanged: (session: Session) => void; onCancel: () => void; onExpired: () => void;
}) {
  const [oldPassword, setOldPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError('');
    if (new TextEncoder().encode(newPassword).length < 12) { setError('新密码至少需要 12 个字节。'); return; }
    if (newPassword === oldPassword) { setError('新密码不能与当前密码相同。'); return; }
    if (newPassword !== confirm) { setError('两次输入的新密码不一致。'); return; }
    setBusy(true);
    try {
      const next = await api.changePassword(session, oldPassword, newPassword);
      saveSession(next);
      onChanged(next);
    } catch (reason) {
      if (reason instanceof ApiError && reason.code === 'UNAUTHORIZED') onExpired();
      else setError(messageFor(reason));
    } finally { setBusy(false); }
  }

  return <main className="simple-page"><header className="simple-header"><Brand/></header><section className="form-card">
    <div className="form-icon"><Icon name="lock" size={30}/></div>
    <h1>{forced ? '请先修改临时密码' : '修改登录密码'}</h1>
    <p className="subtle">{forced ? '设置新密码后即可查看余额和使用就餐码。' : '修改后需要使用新密码登录。'}</p>
    <form onSubmit={submit}>
      {error && <div className="alert alert-error" role="alert">{error}</div>}
      <label className="field"><span>当前密码</span><div className="input-wrap"><Icon name="lock"/><input type="password" autoComplete="current-password" value={oldPassword} onChange={e => setOldPassword(e.target.value)} required/></div></label>
      <label className="field"><span>新密码</span><div className="input-wrap"><Icon name="lock"/><input type="password" autoComplete="new-password" value={newPassword} onChange={e => setNewPassword(e.target.value)} required/></div><small>至少 12 个字节</small></label>
      <label className="field"><span>确认新密码</span><div className="input-wrap"><Icon name="lock"/><input type="password" autoComplete="new-password" value={confirm} onChange={e => setConfirm(e.target.value)} required/></div></label>
      <button className="primary-button full" type="submit" disabled={busy}>{busy ? '正在保存…' : '保存新密码'}</button>
    </form>
    <button className="text-button center" type="button" onClick={onCancel}>{forced ? '退出登录' : '返回个人信息'}</button>
  </section></main>;
}
