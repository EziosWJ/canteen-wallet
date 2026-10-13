import { useCallback, useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent, type ReactNode } from 'react';
import QRCode from 'qrcode';
import { adminApi, AdminApiError, readAdminSession, saveAdminSession, type AdminAccount, type AdminEmployee, type AdminMeal, type AdminSession, type AdminTransaction, type AdminTerminal, type ConsumptionModes, type SecondFactorEnrollment, type SecurityState } from './adminApi';
import './admin.css';

type Section = 'employees' | 'withdrawals' | 'imports' | 'ledger' | 'refunds' | 'reversals' | 'meals' | 'daily' | 'receipts' | 'manual-supply' | 'backups' | 'exports' | 'modes' | 'self-service-qr' | 'terminals' | 'security' | 'audit' | 'not-found';
type LoadState = { busy: boolean; error: string; data: unknown };
const emptyLoad: LoadState = { busy: false, error: '', data: null };
type MealDraft = AdminMeal & { price_text: string };
const mealToDraft = (meal: AdminMeal): MealDraft => ({ ...meal, price_text: (meal.price_cents / 100).toFixed(2) });
const draftToMeal = (draft: MealDraft): AdminMeal => ({ code: draft.code, name: draft.name, start_time: draft.start_time, end_time: draft.end_time, price_cents: draft.price_cents, enabled: draft.enabled });
type Page = Exclude<Section, 'not-found'>;
const sectionLabels: Record<Page, string> = {
  employees: '员工管理', withdrawals: '余额退还与关户', imports: '人员导入',
  ledger: '资金流水', refunds: '消费退款', reversals: '充值冲正', meals: '餐次配置',
  daily: '每日余额日结', receipts: '线下收款复核', 'manual-supply': '故障供餐补录', backups: '备份管理',
  exports: '数据导出', modes: '消费模式', 'self-service-qr': '自助消费二维码', terminals: '终端与扫码事件', security: '管理员与个人安全', audit: '审计记录',
};
const sectionPaths: Record<Page, string> = {
  employees: '/admin/employees', withdrawals: '/admin/withdrawals', imports: '/admin/imports',
  ledger: '/admin/ledger', refunds: '/admin/refunds', reversals: '/admin/reversals', meals: '/admin/meals',
  daily: '/admin/daily', receipts: '/admin/receipts', 'manual-supply': '/admin/manual-supply', backups: '/admin/backups',
  exports: '/admin/exports', modes: '/admin/consumption-modes', 'self-service-qr': '/admin/self-service-qr', terminals: '/admin/terminals', security: '/admin/security', audit: '/admin/audit',
};
const navigationGroups: { title: string; pages: Page[] }[] = [
  // The withdrawal page keeps its address but leaves the navigation: it is only
  // ever reached with an employee already chosen, from the operations panel or
  // from the refund flow's deep link.
  { title: '人员与账户', pages: ['employees', 'imports'] },
  { title: '资金管理', pages: ['ledger', 'refunds', 'reversals'] },
  { title: '财务核对', pages: ['daily', 'receipts'] },
  { title: '供餐管理', pages: ['meals', 'manual-supply'] },
  { title: '系统管理', pages: ['backups', 'exports', 'modes', 'self-service-qr', 'terminals', 'security', 'audit'] },
];

function sectionFromPath(pathname: string): Section {
  const path = pathname.replace(/\/$/, '') || '/';
  // `/admin` is the console root and has no entry of its own; canonicalPath has
  // already sent the removed addresses to the employee list before this runs.
  if (path === '/admin') return 'employees';
  return (Object.keys(sectionPaths) as Page[]).find(page => sectionPaths[page] === path) || 'not-found';
}

/** Old bookmarks point at pages that no longer exist — the balance adjustment is
 * a modal inside employee management now, and the withdrawal page needs its
 * employee to have been chosen. Send those addresses to the employee list rather
 * than showing a dead page or an empty picker. */
function canonicalPath(pathname: string, search: string): string | null {
  const path = pathname.replace(/\/$/, '') || '/';
  if (path === '/admin' || path === '/admin/adjustments') return sectionPaths.employees;
  if (path === '/admin/withdrawals' && !new URLSearchParams(search).get('employee_id')) return sectionPaths.employees;
  return null;
}

/** The message shown when the server refuses something the page could have
 * predicted. Every endpoint answers with a stable code, and the code is the
 * contract this page reads; the server's own message is English and is only
 * ever a last resort. The sign-in form and the signed-in console share this one
 * function so the same refusal cannot read two different ways. */
function message(error: unknown): string {
  if (!(error instanceof AdminApiError)) return '操作失败，请稍后重试。';
  switch (error.code) {
    // Sign-in answers INVALID_CREDENTIALS with 401 for every refusal — unknown
    // account, wrong password, wrong or missing code — so this case is read
    // before the session-expiry one below, or a refused sign-in is reported as
    // an expired session. A session that really expired answers UNAUTHORIZED.
    case 'INVALID_CREDENTIALS': return '账号或密码不正确；已绑定动态验证码的账号还需同时输入验证器上当前的验证码。';
    case 'INSUFFICIENT_FUNDS': return '账户余额不足；记录已标记为异常，补足余额后可从此记录重试。';
    case 'ACCOUNT_UNAVAILABLE': return '员工或账户状态不可用；记录已标记为异常，请处理状态后从此记录重试。';
    case 'ALREADY_INITIALIZED': return '系统已经有管理员，无法再次初始化。';
    case 'ENTRANCE_DISABLED': return '该消费入口已关闭。';
    case 'SERVICE_UNAVAILABLE': return '服务暂时不可用，请稍后重试。';
    case 'INVALID_STATE': return secondFactorStateMessage(error.message);
    case 'CONFLICT': return conflictMessage(error.message);
    case 'INVALID_REQUEST':
      // The import endpoints answer their refusals in Chinese already (the
      // template header, for one), so an unrecognised detail is passed through
      // rather than replaced by a generic line.
      if (error.message.includes('administrator username')) return '管理员账号名不合法：需为 3 至 64 位的字母、数字、点、下划线或连字符。';
      if (error.message.includes('initial password')) return '初始密码至少需要 12 个字符。';
      return error.message;
  }
  if (error.status === 401) return '管理员会话已失效，请重新登录。';
  if (error.status === 404) return '此功能的服务接口尚未开放。';
  return error.message;
}

/** The second-factor endpoints report every refusal as INVALID_STATE, so the
 * server's own message is the only thing that tells them apart. */
function secondFactorStateMessage(detail: string): string {
  if (detail.includes('no second factor is bound')) return '该账号尚未绑定动态验证码，无需解绑。';
  if (detail.includes('no pending second factor enrollment')) return '当前没有待确认的动态验证码绑定；请重新开始绑定并扫描新的二维码。';
  if (detail.includes('has expired')) return '这次绑定已超过十分钟，已经失效；请重新开始绑定。';
  return '当前状态不允许此操作，请刷新页面后重试。';
}

/** Creating an administrator and confirming an import preview both refuse with
 * CONFLICT. */
function conflictMessage(detail: string): string {
  if (detail.includes('username is already taken')) return '该管理员账号名已被占用，请换一个。';
  if (detail.includes('preview already confirmed')) return '此预览已提交过。为保护临时密码，不能再次读取导入结果；请检查员工列表后重新上传需要补充的记录。';
  if (detail.includes('preview expired')) return '导入预览已过期，请重新上传文件并核对行数。';
  return '操作与当前状态冲突，请刷新页面后重试。';
}

function yuan(cents: number): string { return `¥${(cents / 100).toFixed(2)}`; }
function cents(value: string): number { return Math.round(Number(value) * 100); }
function nonnegativeMoneyInputCents(value: string): number | null {
  if (!/^(?:0|[1-9]\d*)(?:\.\d{1,2})?$/.test(value.trim())) return null;
  const amount = Number(value);
  if (!Number.isFinite(amount) || amount < 0 || amount > Number.MAX_SAFE_INTEGER / 100) return null;
  return Math.round(amount * 100);
}
function moneyInputCents(value: string): number | null {
  const amount = nonnegativeMoneyInputCents(value);
  return amount != null && amount > 0 ? amount : null;
}
function signedYuan(amount: number): string { return `${amount < 0 ? '−' : '+'}${yuan(Math.abs(amount))}`; }
function businessToday(): string { return new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai' }).format(new Date()); }
function idempotencyKey(): string { return globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(36).slice(2)}`; }

function Login({ onLogin }: { onLogin: (session: AdminSession) => void }) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [secondFactor, setSecondFactor] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('');
    try { const session = await adminApi.login(username.trim(), password, secondFactor.trim()); saveAdminSession(session); onLogin(session); }
    catch (reason) { setError(message(reason)); }
    finally { setBusy(false); }
  }
  return <main className="admin-login-wrap"><section className="admin-login-card">
    <div className="admin-brand"><img src="/assets/ui/app-logo.png" alt="食堂储值卡"/><span><b>食堂储值卡</b><small>管理后台</small></span></div>
    <h1>管理员登录</h1><p className="admin-muted">使用独立管理员账号；已绑定动态验证码的账号需要一并输入</p>
    <form onSubmit={submit} className="admin-form">
      {error && <div className="admin-alert error" role="alert">{error}</div>}
      <label>管理员账号<input autoComplete="username" value={username} onChange={e => setUsername(e.target.value)} required/></label>
      <label>密码<input type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} required/></label>
      <label>动态验证码<input inputMode="numeric" autoComplete="one-time-code" value={secondFactor} onChange={e => setSecondFactor(e.target.value)} placeholder="未绑定可留空"/><small className="admin-muted">绑定是可选的：未绑定时只输入密码即可登录。</small></label>
      <button className="admin-primary" disabled={busy}>{busy ? '正在验证…' : '安全登录'}</button>
    </form>
  </section></main>;
}

function ModeChoice({ modes, onChange, legend }: { modes: ConsumptionModes; onChange: (next: ConsumptionModes) => void; legend: string }) {
  const options: { key: keyof ConsumptionModes; label: string; detail: string }[] = [
    { key: 'payment_code', label: '就餐码消费', detail: '员工在食堂终端前出示动态码' },
    { key: 'self_service', label: '自助消费', detail: '员工在自助入口自行确认扣款' },
  ];
  const enabledCount = options.filter(option => modes[option.key]).length;
  return <fieldset className="mode-choice"><legend>{legend}</legend>
    {options.map(option => <label key={option.key} className={`mode-option ${modes[option.key] ? 'on' : ''}`}>
      <input type="checkbox" checked={modes[option.key]} disabled={modes[option.key] && enabledCount === 1}
        onChange={event => onChange({ ...modes, [option.key]: event.target.checked })}/>
      <span><b>{option.label}</b><small>{option.detail}</small></span>
    </label>)}
    <p className="admin-muted">至少保留一种消费模式；最后一种开启的模式不能关闭。</p>
  </fieldset>;
}

function ConsumptionModeForm({ modes, busy, onSubmit }: { modes: ConsumptionModes; busy: boolean; onSubmit: (next: ConsumptionModes) => void }) {
  const [draft, setDraft] = useState(modes);
  // The page reloaded the modes, so the form must follow the server again.
  useEffect(() => setDraft(modes), [modes]);
  const changed = draft.payment_code !== modes.payment_code || draft.self_service !== modes.self_service;
  return <form className="admin-form consumption-mode-form" onSubmit={event => { event.preventDefault(); onSubmit(draft); }}>
    <ModeChoice modes={draft} onChange={setDraft} legend="当前启用的消费模式"/>
    <div className="admin-form-actions">
      {changed && <button className="admin-secondary" type="button" onClick={() => setDraft(modes)}>放弃修改</button>}
      <button className="admin-primary" disabled={busy || !changed}>{busy ? '正在保存…' : '保存消费模式'}</button>
    </div>
  </form>;
}

/** The one-time initialization entry. It is only reachable while the
 * installation has no administrator at all, which the status endpoint reports. */
export function Initialization({ onReady }: { onReady: (session: AdminSession, nextStep: string) => void }) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  // Nothing is pre-selected: the initializer must state which entrances this
  // canteen offers rather than accept a guess.
  const [modes, setModes] = useState<ConsumptionModes>({ payment_code: false, self_service: false });
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError('');
    if (password.length < 12) { setError('密码至少需要 12 个字符。'); return; }
    if (password !== confirm) { setError('两次输入的密码不一致。'); return; }
    if (!modes.payment_code && !modes.self_service) { setError('请选择至少一种消费模式。'); return; }
    setBusy(true);
    try {
      const session = await adminApi.initialize({ username: username.trim(), password, ...modes });
      saveAdminSession(session);
      onReady(session, session.next_step);
    } catch (reason) { setError(message(reason)); }
    finally { setBusy(false); }
  }

  return <main className="admin-login-wrap"><section className="admin-login-card">
    <div className="admin-brand"><img src="/assets/ui/app-logo.png" alt="食堂储值卡"/><span><b>食堂储值卡</b><small>管理后台</small></span></div>
    <h1>初始化系统</h1><p className="admin-muted">系统还没有管理员。请创建首位管理员并选择食堂提供的消费模式，随后继续配置餐次。</p>
    <form onSubmit={submit} className="admin-form">
      {error && <div className="admin-alert error" role="alert">{error}</div>}
      <label>管理员账号<input autoComplete="username" value={username} onChange={e => setUsername(e.target.value)} required minLength={3} maxLength={64} pattern="[a-z0-9._\-]+"/><small className="admin-muted">3–64 位小写字母、数字、点、下划线或短横线。</small></label>
      <label>登录密码<input type="password" autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} required minLength={12}/><small className="admin-muted">至少 12 个字符。</small></label>
      <label>确认密码<input type="password" autoComplete="new-password" value={confirm} onChange={e => setConfirm(e.target.value)} required/></label>
      <ModeChoice modes={modes} onChange={setModes} legend="员工可用的消费模式"/>
      <button className="admin-primary" disabled={busy}>{busy ? '正在初始化…' : '创建首位管理员并继续'}</button>
    </form>
    <p className="admin-muted">初始化成功后会立即登录，并提示你继续配置餐次时间与价格。</p>
  </section></main>;
}

function Panel({ title, description, children, action }: { title: string; description?: string; children: ReactNode; action?: ReactNode }) {
  return <section className="admin-panel"><header className="admin-panel-head"><div><h2>{title}</h2>{description && <p>{description}</p>}</div>{action}</header>{children}</section>;
}

function ErrorBox({ children, retry }: { children: string; retry?: () => void }) {
  return <div className="admin-alert error" role="alert">{children}{retry && <button type="button" className="admin-link" onClick={retry}>重试</button>}</div>;
}

function Loading() { return <div className="admin-empty" role="status">正在读取服务数据…</div>; }

function isPrivateIPv4(hostname: string): boolean {
  const octets = hostname.split('.').map(Number);
  if (octets.length !== 4 || octets.some(value => !Number.isInteger(value) || value < 0 || value > 255)) return false;
  return octets[0] === 10 || octets[0] === 127 || octets[0] === 0 ||
    (octets[0] === 172 && octets[1] >= 16 && octets[1] <= 31) ||
    (octets[0] === 192 && octets[1] === 168) ||
    (octets[0] === 169 && octets[1] === 254);
}

function selfServiceAddressWarning(address: string): string {
  let parsed: URL;
  try { parsed = new URL(address); } catch { return ''; }
  const hostname = parsed.hostname.toLowerCase();
  const local = hostname === 'localhost' || hostname === '::1' || hostname === '[::1]' || isPrivateIPv4(hostname) || hostname.endsWith('.local');
  if (local) return '当前地址属于本机或局域网地址，请确认员工手机能访问此地址；系统仍允许生成二维码。';
  if (parsed.protocol !== 'https:') return '当前地址未使用 HTTPS，请确认员工手机能访问此地址并按部署环境评估传输安全；系统仍允许生成二维码。';
  return '';
}

function SelfServiceQRCode() {
  const defaultLink = `${window.location.origin}/self-service`;
  const [link, setLink] = useState(defaultLink);
  const [draftLink, setDraftLink] = useState(defaultLink);
  const [qrData, setQrData] = useState('');
  const [error, setError] = useState('');
  const [linkError, setLinkError] = useState('');
  const [notice, setNotice] = useState('');
  const warning = selfServiceAddressWarning(link);

  useEffect(() => {
    let cancelled = false;
    setQrData(''); setError('');
    void QRCode.toDataURL(link, {
      errorCorrectionLevel: 'M', margin: 2, width: 360,
      color: { dark: '#17324D', light: '#FFFFFF' },
    }).then(data => { if (!cancelled) setQrData(data); })
      .catch(() => { if (!cancelled) setError('二维码生成失败，请刷新页面后重试。'); });
    return () => { cancelled = true; };
  }, [link]);

  function regenerate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const value = draftLink.trim();
    try {
      const parsed = new URL(value);
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') throw new Error('unsupported protocol');
      setLinkError('');
      setLink(value);
      setNotice('二维码已按新地址重新生成。');
    } catch {
      setLinkError('请输入完整的 http:// 或 https:// 地址。');
    }
  }

  async function copyLink() {
    try {
      await navigator.clipboard.writeText(link);
      setNotice('自助消费链接已复制。');
    } catch {
      setNotice('浏览器未允许自动复制，请选中下方链接手动复制。');
    }
  }

  function downloadQR() {
    if (!qrData) return;
    const anchor = document.createElement('a');
    anchor.href = qrData; anchor.download = 'self-service-qr.png'; anchor.click();
    setNotice('二维码已下载。');
  }

  return <div className="self-service-qr-page">
    {warning && <div className="admin-alert warn self-service-qr-warning" role="alert">{warning}</div>}
    <div className="self-service-qr-layout">
      <section className="self-service-qr-print-card">
        <p className="self-service-qr-brand">食堂储值卡</p>
        <h3>员工自助消费</h3>
        {qrData ? <img className="self-service-qr-image" src={qrData} alt="固定自助消费链接二维码"/> : <div className="self-service-qr-placeholder" role="status">{error || '正在生成二维码…'}</div>}
        <p className="self-service-qr-url">{link}</p>
        <p className="self-service-qr-instruction">扫码进入自助消费，登录后查看当前餐次和金额，确认无误后点击确认扣款。</p>
      </section>
      <section className="self-service-qr-details">
        <h3>固定链接</h3>
        <p className="admin-muted">所有员工共用同一个入口。二维码只编码当前站点地址，不携带任何员工身份或消费数据。</p>
        <form className="self-service-qr-link-form" onSubmit={regenerate}>
          <label className="self-service-qr-link-field">完整地址<input type="url" required value={draftLink} onChange={event => { setDraftLink(event.target.value); setLinkError(''); }} onFocus={event => event.currentTarget.select()} placeholder="https://canteen.example.com/self-service"/></label>
          <button className="admin-primary" type="submit">重新生成二维码</button>
        </form>
        {linkError && <div className="admin-alert error" role="alert">{linkError}</div>}
        <div className="admin-inline-actions self-service-qr-actions">
          <button className="admin-primary" type="button" onClick={() => void copyLink()}>复制链接</button>
          <button className="admin-secondary" type="button" onClick={downloadQR} disabled={!qrData}>下载二维码</button>
          <button className="admin-secondary" type="button" onClick={() => window.print()}>打印二维码</button>
        </div>
        {notice && <p className="admin-muted" role="status">{notice}</p>}
        <div className="admin-note self-service-qr-note"><h3>使用提醒</h3><ul><li>请先确认上方地址能被员工手机访问，再打印或张贴。</li><li>员工扫码后如未登录，会在同一页面完成登录；首次使用临时密码还需先修改密码。</li><li>打开页面和查看预览不会扣款，只有员工明确点击确认扣款才会产生消费。</li></ul></div>
      </section>
    </div>
  </div>;
}

function Table({ rows }: { rows: Record<string, unknown>[] }) {
  if (!rows.length) return <div className="admin-empty">暂无记录</div>;
  const columns = Object.keys(rows[0]).filter(key => !['photo_url', 'payload_summary'].includes(key));
  const labels: Record<string, string> = { id: '编号', transaction_id: '交易编号', account_id: '账户编号', employee_id: '员工编号', employee_no: '员工工号', name: '姓名', type: '类型', amount_cents: '金额', before_balance_cents: '变动前余额', after_balance_cents: '变动后余额', opening_cents: '期初余额', movement_cents: '期间变动', expected_cents: '理论期末余额', actual_cents: '实际期末余额', difference_cents: '差额', breakdown_cents: '分类金额', business_id: '业务编号', related_transaction_id: '关联交易', receipt_ref: '凭据编号', meal_code: '餐次', business_date: '营业日期', status: '状态', note: '说明', created_at: '发生时间', collected_at: '收款时间', checked_at: '核对时间', entered_by: '录入人', reviewer_id: '复核人', terminal_id: '终端', result_code: '结果', filename: '文件名', external: '外部副本', last_seen_at: '最近心跳', scanner_status: '扫码枪', voice_status: '语音模块', payment_method: '收款方式', enabled: '启用', administrator_id: '管理员', action: '操作', subject_type: '对象类型', subject_id: '对象编号', details_json: '操作详情', generated_by: '生成管理员', generated_at: '生成时间' };
  const values: Record<string, string> = { RECHARGE: '线下充值', RECHARGE_REVERSAL: '充值冲正', CONSUME: '餐费消费', REFUND: '消费退款', BALANCE_ADJUSTMENT: '余额调整', BALANCE_WITHDRAWAL: '余额退还', MATCHED: '凭据匹配', DIFFERENCE: '存在差异', RESOLVED: '差异已处理', PENDING: '待复核', POSTED: '已补录', EXCEPTION: '待人工处理', ONLINE: '在线', OFFLINE: '离线', READY: '就绪', UNAVAILABLE: '不可用' };
  const valueText = (key: string, value: unknown) => {
    if (value == null) return '—';
    if (typeof value === 'boolean') return value ? '是' : '否';
    if (key.endsWith('_cents') && typeof value === 'number') return yuan(value);
    if (key.endsWith('_at') && typeof value === 'string') { const date = new Date(value); return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', hour12: false }).format(date); }
    if (value && typeof value === 'object') return JSON.stringify(value);
    return values[String(value)] || String(value);
  };
  return <div className="admin-table-wrap"><table className="admin-table"><thead><tr>{columns.map(key => <th key={key}>{labels[key] || key.replaceAll('_', ' ')}</th>)}</tr></thead><tbody>{rows.map((row, index) => <tr key={String(row.id ?? row.transaction_id ?? row.code ?? index)}>{columns.map(key => <td key={key}>{valueText(key, row[key])}</td>)}</tr>)}</tbody></table></div>;
}

function rowsFrom(value: unknown): Record<string, unknown>[] {
  if (Array.isArray(value)) return value.filter(item => item && typeof item === 'object') as Record<string, unknown>[];
  if (value && typeof value === 'object') {
    for (const item of Object.values(value as Record<string, unknown>)) if (Array.isArray(item)) return item.filter(row => row && typeof row === 'object') as Record<string, unknown>[];
  }
  return [];
}

export default function Admin() {
  // The session lives here because initialization and login both produce one.
  const [session, setSession] = useState<AdminSession | null>(() => readAdminSession());
  // Whether the one-time initialization entry is open is decided by the server,
  // so the first paint waits for it rather than guessing from a missing session.
  const [bootstrap, setBootstrap] = useState<'checking' | 'required' | 'ready'>('checking');
  const [nextStep, setNextStep] = useState('');

  useEffect(() => {
    let cancelled = false;
    void adminApi.bootstrapStatus()
      .then(status => { if (!cancelled) setBootstrap(status.required ? 'required' : 'ready'); })
      .catch(() => { if (!cancelled) setBootstrap('ready'); });
    return () => { cancelled = true; };
  }, []);

  if (bootstrap === 'checking') return <main className="admin-login-wrap"><section className="admin-login-card"><Loading/></section></main>;
  if (bootstrap === 'required') return <Initialization onReady={(next, step) => { setBootstrap('ready'); setNextStep(step); setSession(next); }}/>;

  return <AdminConsole session={session} onSession={setSession} initialState={nextStep} onInitialStateConsumed={() => setNextStep('')}/>;
}

function AdminConsole({ session, onSession, initialState, onInitialStateConsumed }: {
  session: AdminSession | null; onSession: (session: AdminSession | null) => void;
  initialState: string; onInitialStateConsumed: () => void;
}) {
  const [section, setSection] = useState<Section>(() => sectionFromPath(canonicalPath(window.location.pathname, window.location.search) ?? window.location.pathname));
  const [mobileNavigationGroup, setMobileNavigationGroup] = useState('');
  const [desktopNavigation, setDesktopNavigation] = useState(() => window.matchMedia('(min-width: 851px)').matches);
  const [identity, setIdentity] = useState('');
  const [state, setState] = useState<LoadState>(emptyLoad);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [employees, setEmployees] = useState<AdminEmployee[]>([]);
  const [employeeFilters, setEmployeeFilters] = useState({ q: '', department: '', account_status: '' });
  const [employeeDraft, setEmployeeDraft] = useState({ q: '', department: '', account_status: '' });
  const [employeeNextCursor, setEmployeeNextCursor] = useState('');
  const [meals, setMeals] = useState<AdminMeal[]>([]);
  const [modes, setModes] = useState<ConsumptionModes | null>(null);
  const [accounts, setAccounts] = useState<AdminAccount[]>([]);
  const [security, setSecurity] = useState<SecurityState | null>(null);
  const [me, setMe] = useState<{ id: number; username: string } | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [showEdit, setShowEdit] = useState<AdminEmployee | null>(null);
  const [showRecharge, setShowRecharge] = useState<AdminEmployee | null>(null);
  // The panel and the two modal flows it opens are mutually exclusive: opening
  // a flow closes the panel, so the console never stacks two overlays.
  const [showOperations, setShowOperations] = useState<AdminEmployee | null>(null);
  // The balance adjustment opened from the panel. It keeps the employee it was
  // opened for, so the mount key is per account and the adjustment cannot be
  // redirected to someone else mid-flight.
  const [showAdjustment, setShowAdjustment] = useState<AdminEmployee | null>(null);
  const [showPasswordReset, setShowPasswordReset] = useState<AdminEmployee | null>(null);
  const [businessDate, setBusinessDate] = useState(businessToday());
  const [mealDraft, setMealDraft] = useState<MealDraft[] | null>(null);
  const loadRequest = useRef(0);
  const currentSection = useRef(section);
  currentSection.current = section;
  const mealsDirty = mealDraft != null && JSON.stringify(mealDraft) !== JSON.stringify(meals.map(mealToDraft));
  const mealsDirtyRef = useRef(mealsDirty);
  mealsDirtyRef.current = mealsDirty;

  function navigate(page: Page, replace = false, params?: Record<string, string | number>) {
    if (currentSection.current === 'meals' && page !== 'meals' && mealsDirtyRef.current && !window.confirm('餐次配置有未保存的修改。确定放弃并离开吗？')) return;
    const query = params ? `?${new URLSearchParams(Object.entries(params).map(([key, value]) => [key, String(value)]))}` : '';
    const path = `${sectionPaths[page]}${query}`;
    if (window.location.pathname === sectionPaths[page] && window.location.search === query && !replace) { setNotice(''); return; }
    if (window.location.pathname + window.location.search !== path) {
      if (replace) window.history.replaceState({ page }, '', path);
      else window.history.pushState({ page }, '', path);
    }
    loadRequest.current += 1;
    currentSection.current = page;
    setSection(page);
    setState(emptyLoad);
    setMobileNavigationGroup(navigationGroups.find(group => group.pages.includes(page))?.title || '');
    setNotice('');
  }

  function followAdminLink(event: ReactMouseEvent<HTMLAnchorElement>, page: Page) {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    navigate(page);
  }

  function goToEmployeeOperation(page: 'withdrawals', employeeId: number) {
    setShowOperations(null);
    navigate(page, false, { employee_id: employeeId });
  }

  useEffect(() => {
    const handlePopState = () => {
      // Old addresses are normalised first, so history never rests on a page
      // that no longer exists.
      const redirect = canonicalPath(window.location.pathname, window.location.search);
      if (redirect) { window.history.replaceState({ page: 'employees' }, '', redirect); }
      const page = redirect ? 'employees' : sectionFromPath(window.location.pathname);
      if (currentSection.current === 'meals' && page !== 'meals' && mealsDirtyRef.current && !window.confirm('餐次配置有未保存的修改。确定放弃并离开吗？')) {
        window.history.pushState({ page: 'meals' }, '', sectionPaths.meals);
        return;
      }
      loadRequest.current += 1;
      currentSection.current = page;
      setSection(page);
      setState(emptyLoad);
      setMobileNavigationGroup(page === 'not-found' ? '' : navigationGroups.find(group => group.pages.includes(page))?.title || '');
      setNotice('');
    };
    window.addEventListener('popstate', handlePopState);
    // A direct visit needs the same treatment: the first render takes its section
    // from the path, so normalize it once at mount.
    handlePopState();
    return () => window.removeEventListener('popstate', handlePopState);
  }, []);

  useEffect(() => {
    const media = window.matchMedia('(min-width: 851px)');
    const updateLayout = () => setDesktopNavigation(media.matches);
    updateLayout();
    media.addEventListener('change', updateLayout);
    return () => media.removeEventListener('change', updateLayout);
  }, []);

  const expire = useCallback(() => { saveAdminSession(null); onSession(null); setIdentity(''); setState(emptyLoad); }, [onSession]);
  const routeQuery = new URLSearchParams(window.location.search);
  const initialEmployeeId = Number(routeQuery.get('employee_id')) || undefined;
  const initialTransactionId = routeQuery.get('transaction_id') || undefined;
  const load = useCallback(async () => {
    if (!session) return;
    const requestID = ++loadRequest.current;
    setState(current => ({ ...current, busy: true, error: '' }));
    try {
      let data: unknown;
      if (section === 'employees') {
        const response = await adminApi.searchEmployees(session, { ...employeeFilters, limit: 50 });
        if (requestID === loadRequest.current) {
          setEmployees(response.employees || []);
          setEmployeeNextCursor(response.next_cursor || '');
        }
        data = response;
      }
      else if (section === 'meals') {
        const response = await adminApi.mealPeriods(session);
        if (requestID === loadRequest.current) {
          setMeals(response.meal_periods || []);
          if (!mealsDirtyRef.current) setMealDraft((response.meal_periods || []).map(mealToDraft));
        }
        data = response;
      }
      else if (section === 'security') {
        const [state, people] = await Promise.all([adminApi.security(session), adminApi.administrators(session)]);
        setSecurity(state); setAccounts(people.administrators || []); data = state;
      }
      else if (section === 'terminals') { data = await adminApi.terminals(session); }
      else if (section === 'modes') { const response = await adminApi.adminConsumptionModes(session); setModes(response); data = response; }
      else {
        const paths: Partial<Record<Section, string>> = {
          daily: `/api/admin/reconciliation/daily?business_date=${encodeURIComponent(businessDate)}`,
          audit: '/api/admin/audit-events',
        };
        if (section === 'imports' || section === 'ledger' || section === 'refunds' || section === 'reversals' || section === 'exports' || section === 'receipts' || section === 'manual-supply' || section === 'backups' || section === 'self-service-qr' || section === 'not-found') {
          data = null;
        } else if (paths[section]) data = await adminApi.generic<unknown>(session, paths[section]!);
      }
      if (requestID === loadRequest.current) setState({ busy: false, error: '', data });
    } catch (reason) {
      if (requestID !== loadRequest.current) return;
      if (reason instanceof AdminApiError && reason.status === 401) { expire(); return; }
      setState(current => ({ busy: false, error: message(reason), data: current.data }));
    }
  }, [session, section, businessDate, employeeFilters, expire]);

  useEffect(() => {
    if (!session) return;
    adminApi.me(session).then(value => {
      setIdentity(value.username);
      setMe({ id: value.id, username: value.username });
      // /api/admin/me already carries the binding state, so the header can show
      // it without a second request on every page.
      setSecurity(current => current ?? { second_factor_bound: value.second_factor_bound, enrollment_pending: value.enrollment_pending });
    }).catch(reason => { if (reason instanceof AdminApiError && reason.status === 401) expire(); else setIdentity(session.administrator.username); });
  }, [session, expire]);
  useEffect(() => { void load(); }, [load]);

  // Initialization deliberately stops after creating the account so the operator
  // continues with the meal configuration the page prompts for.
  useEffect(() => {
    if (!initialState || !session) return;
    navigate('meals', true);
    setNotice('系统已初始化。请继续配置餐次供应时间与价格。');
    onInitialStateConsumed();
  }, [initialState, session, onInitialStateConsumed]);

  async function runAction(action: () => Promise<unknown>, success: string) {
    const actionSection = currentSection.current;
    setBusy(true); setNotice('');
    try { await action(); if (currentSection.current === actionSection) { setNotice(success); await load(); } }
    catch (reason) { if (reason instanceof AdminApiError && reason.status === 401) expire(); else setNotice(message(reason)); }
    finally { setBusy(false); }
  }
  async function logout() { if (session) { try { await adminApi.logout(session); } catch { /* local credentials must still be cleared */ } } expire(); }

  if (!session) return <Login onLogin={onSession}/>;

  return <div className="admin-app">
    <header className="admin-topbar"><a className="admin-brand" href={sectionPaths.employees} onClick={event => followAdminLink(event, 'employees')} aria-label="食堂储值卡管理后台"><img src="/assets/ui/app-logo.png" alt=""/><span><b>食堂储值卡</b><small>管理后台</small></span></a><div className="admin-user"><span>{identity || session.administrator.username}</span><button type="button" onClick={() => void logout()}>退出</button></div></header>
    <div className="admin-layout">
      <nav className="admin-sidebar" aria-label="管理菜单">{navigationGroups.map(group => <details className="admin-nav-group" key={group.title} open={desktopNavigation || mobileNavigationGroup === group.title} onToggle={event => { if (!desktopNavigation) { if (event.currentTarget.open) setMobileNavigationGroup(group.title); else setMobileNavigationGroup(current => current === group.title ? '' : current); } }}><summary className="admin-nav-group-title">{group.title}</summary>{group.pages.map(page => <a className={`admin-nav-link ${section === page ? 'selected' : ''}`} href={sectionPaths[page]} key={page} aria-current={section === page ? 'page' : undefined} onClick={event => followAdminLink(event, page)}>{sectionLabels[page]}</a>)}</details>)}</nav>
      <main className="admin-main"><div className="admin-title-row"><div><p className="admin-eyebrow">食堂运营控制台</p><h1>{section === 'not-found' ? '页面不存在' : sectionLabels[section]}</h1></div><button className="admin-secondary" type="button" onClick={() => void load()} disabled={state.busy}>刷新</button></div>
        {notice && <div className={`admin-alert ${notice.includes('失败') || notice.includes('尚未') || notice.includes('不正确') ? 'error' : 'success'}`} role="status">{notice}</div>}
        {section === 'not-found' && <Panel title="找不到管理页面" description="请从管理菜单选择一个功能页面。"><a className="admin-link" href={sectionPaths.employees} onClick={event => followAdminLink(event, 'employees')}>返回员工管理</a></Panel>}
        {section === 'employees' && <>
          <Panel title="员工账户" description="员工档案和余额来自服务端，账户变更均通过审计接口提交。" action={<div className="admin-inline-actions"><ExportButton session={session} kind="employees" filters={employeeFilters} label="导出当前筛选员工"/><ExportButton session={session} kind="balances" filters={employeeFilters} label="导出当前筛选余额"/><button className="admin-secondary compact" type="button" onClick={() => navigate('imports')}>人员导入</button><button className="admin-primary compact" type="button" onClick={() => setShowCreate(true)}>新建员工</button></div>}>
            <form className="admin-inline-form transaction-filters" onSubmit={event => { event.preventDefault(); setEmployees([]); setEmployeeNextCursor(''); setEmployeeFilters(employeeDraft); }}>
              <label>搜索姓名、工号或手机号<input type="search" value={employeeDraft.q} onChange={event => setEmployeeDraft({ ...employeeDraft, q: event.target.value })} placeholder="输入姓名、工号或手机号"/></label>
              <label>部门<input value={employeeDraft.department} onChange={event => setEmployeeDraft({ ...employeeDraft, department: event.target.value })} placeholder="全部部门"/></label>
              <label>账户状态<select value={employeeDraft.account_status} onChange={event => setEmployeeDraft({ ...employeeDraft, account_status: event.target.value })}><option value="">全部状态</option><option value="ACTIVE">正常</option><option value="FROZEN">冻结</option><option value="CLOSED">已关闭</option></select></label>
              <button className="admin-secondary">搜索员工</button>
              <button className="admin-link" type="button" onClick={() => { const empty = { q: '', department: '', account_status: '' }; setEmployeeDraft(empty); setEmployeeFilters(empty); setEmployees([]); setEmployeeNextCursor(''); }}>清除筛选</button>
            </form>
            {state.busy && !state.data ? <Loading/> : <>{state.error && <ErrorBox retry={() => void load()}>{state.error}</ErrorBox>}<div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>员工编号</th><th>姓名</th><th>部门</th><th>手机号</th><th>账户余额</th><th>状态</th><th>操作</th></tr></thead><tbody>{employees.map(person => <tr key={person.id}><td>{person.employee_no}</td><td>{person.name}</td><td>{person.department || '—'}</td><td>{person.phone}</td><td className="money">{yuan(person.balance)}</td><td><span className={`admin-status ${person.status === 'ACTIVE' && person.account_status === 'ACTIVE' ? 'ok' : 'warn'}`}>{person.status}/{person.account_status}</span></td><td><div className="admin-actions"><button type="button" className="admin-op-trigger" onClick={() => setShowOperations(person)}>账户操作</button></div></td></tr>)}</tbody></table>{employees.length === 0 && <div className="admin-empty">暂无符合条件的员工</div>}</div>{employeeNextCursor && <button className="admin-secondary load-more" type="button" disabled={state.busy} onClick={async () => { const cursor = employeeNextCursor; setState(current => ({ ...current, busy: true, error: '' })); try { const page = await adminApi.searchEmployees(session, { ...employeeFilters, limit: 50, cursor }); setEmployees(current => [...current, ...page.employees]); setEmployeeNextCursor(page.next_cursor || ''); setState(current => ({ ...current, busy: false })); } catch (error) { setState(current => ({ ...current, busy: false, error: message(error) })); } }}>加载更多员工</button>}</>}
          </Panel>
        </>}
        {section === 'withdrawals' && <Panel title="余额退还与关户" description="正常或冻结账户可退还全部余额后关户；零余额可直接关户；已关户账户只可线下退还尚未支付的历史消费退款。"><WithdrawalPage key={`${initialEmployeeId ?? 'none'}-${routeQuery.get('refund_id') || ''}`} session={session} initialEmployeeId={initialEmployeeId} initialRefundId={routeQuery.get('refund_id') || undefined} busy={busy} onNotice={setNotice} onNavigate={page => { void load(); navigate(page); }}/></Panel>}
        {section === 'imports' && <Panel title="人员 Excel 导入" description="先由服务端校验并预览行级结果，再确认创建有效行；错误行会保留在报告中。"><ImportPanel session={session} onNotice={setNotice}/></Panel>}
        {section === 'ledger' && <TransactionLedger session={session} onNavigate={(page, id) => navigate(page, false, { transaction_id: id })}/>}
        {section === 'refunds' && <MoneyRecordFlow key={`refund-${initialTransactionId || ''}`} kind="refund" session={session} initialTransactionId={initialTransactionId} onNotice={setNotice} onNavigateToWithdrawal={(employeeId, refundId) => navigate('withdrawals', false, { employee_id: employeeId, refund_id: refundId })}/>}
        {section === 'reversals' && <MoneyRecordFlow key={`reverse-${initialTransactionId || ''}`} kind="reversal" session={session} initialTransactionId={initialTransactionId} onNotice={setNotice}/>}
        {section === 'meals' && <Panel title="餐次配置" description="早餐、午餐和晚餐共用一份草稿；检查全部时段后一次保存，避免部分时段先后生效。">
          {state.error && <ErrorBox retry={() => void load()}>{state.error}</ErrorBox>}{state.busy && !state.data ? <Loading/> : <MealConfiguration meals={mealDraft ?? meals.map(mealToDraft)} savedMeals={meals} busy={busy} loading={state.busy} error={state.error} onChange={setMealDraft} onDiscard={() => setMealDraft(meals.map(mealToDraft))} onSave={async draft => {
            const saved = await adminApi.updateMeals(session, draft.map(draftToMeal));
            setMeals(saved.meal_periods || []); setMealDraft((saved.meal_periods || []).map(mealToDraft));
            setNotice('早餐、午餐和晚餐配置已一次保存。');
          }}/>}</Panel>}
        {section === 'modes' && <Panel title="消费模式" description="决定员工可以使用哪些消费入口；修改立即生效并写入审计记录。">
          {state.error && <ErrorBox retry={() => void load()}>{state.error}</ErrorBox>}{state.busy && !state.data ? <Loading/> : modes ? <ConsumptionModeForm modes={modes} busy={busy} onSubmit={next => runAction(async () => { const saved = await adminApi.updateConsumptionModes(session, next); setModes(saved); }, '消费模式已更新，立即生效。')}/> : null}
          <div className="admin-note"><h3>关闭入口时会发生什么</h3><ul><li>该入口已展示但尚未确认的扫码请求、自助消费意图会被终止，不会扣款。</li><li>重新开启后需要员工重新发起消费。</li><li>已完成的消费、历史流水查询和退款不受影响。</li></ul></div>
        </Panel>}
        {section === 'self-service-qr' && <Panel title="固定自助消费二维码" description="把当前访问地址生成二维码，员工扫码后进入固定的自助消费入口。二维码不包含员工身份、餐次、金额或消费凭据。"><SelfServiceQRCode/></Panel>}
        {section === 'security' && <>
          <SelfSecurityPanel session={session} security={security} busy={busy} onNotice={setNotice} onExpired={expire}/>
          <AdministratorPanel session={session} me={me} accounts={accounts} busy={busy} onNotice={setNotice} onChanged={() => void load()}/>
        </>}
        {section === 'terminals' && <><TerminalManager session={session} items={(state.data as { items: AdminTerminal[] } | null)?.items || []} loading={state.busy} loadError={state.error} reload={() => void load()} onUnauthorized={expire}/><Panel title="扫码事件" description="仅展示脱敏事件摘要，不显示原始 Token。"><RemoteTable session={session} path="/api/admin/scan-events"/></Panel></>}
        {section === 'daily' && <DailyReconciliation date={businessDate} records={rowsFrom(state.data)} loading={state.busy} error={state.error} busy={busy} onDate={setBusinessDate} onRetry={() => void load()} exportButton={<ExportButton session={session} kind="reconciliation" filters={{ business_date: businessDate }} label={`导出 ${businessDate} 日结`}/>} onGenerate={() => {
          const existing = rowsFrom(state.data).find(item => item.business_date === businessDate);
          if (existing && !window.confirm(`${businessDate} 已有日结结果。重新生成会更新这一天的核对结果，是否继续？`)) return;
          void runAction(() => adminApi.generic(session, '/api/admin/reconciliation/daily', { method: 'POST', body: JSON.stringify({ business_date: businessDate }) }), existing ? `已更新 ${businessDate} 日结。` : `已生成 ${businessDate} 日结。`);
        }}/>}
        {section === 'receipts' && <ReceiptReviews session={session} onNotice={setNotice}/>}
        {section === 'manual-supply' && <Panel title="故障供餐登记与补录" description="先登记供餐事实；核对并补录后才扣款。记录按实际入账时间进入日结。"><ManualSupply session={session}/></Panel>}
        {section === 'backups' && <BackupManager session={session}/>}
        {section === 'exports' && <Panel title="全量数据导出" description="按需导出全部匹配数据，不受当前页面筛选或已加载行数影响。若只需当前筛选范围，请在员工、资金流水或日结页面使用对应导出入口。"><div className="export-grid">{[
          ['employees', '全部员工档案'], ['balances', '全部账户余额'], ['transactions', '全部资金流水'], ['reconciliation', '全部日结记录'],
        ].map(([kind, label]) => <ExportButton key={kind} session={session} kind={kind} all label={label}/>)}</div></Panel>}
        {section === 'audit' && <Panel title="管理员审计记录" description="重要操作由后端持续记录；此页面不提供删除或修改入口。">{state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void load()}>{state.error}</ErrorBox> : <Table rows={rowsFrom(state.data)}/>}</Panel>}
      </main>
    </div>
    {showCreate && <CreateEmployee onClose={() => setShowCreate(false)} onSave={input => runAction(async () => { const result = await adminApi.createEmployee(session, input); window.prompt('请安全交付员工临时密码', result.temporary_password); setShowCreate(false); }, '员工档案已创建。')}/>}
    {showEdit && <EditEmployee employee={showEdit} onClose={() => setShowEdit(null)} onSave={input => runAction(async () => { await adminApi.updateEmployee(session, showEdit.id, input); setShowEdit(null); }, '员工档案已更新。')}/>}
    {showRecharge && <RechargeModal employee={showRecharge} onClose={() => setShowRecharge(null)} onSave={input => runAction(async () => { await adminApi.recharge(session, input); setShowRecharge(null); }, '充值及收款凭据已登记。')}/>}
    {showOperations && <AccountOperations employee={showOperations} onClose={() => setShowOperations(null)}
      onEdit={() => { setShowEdit(showOperations); setShowOperations(null); }}
      onRecharge={() => { setShowRecharge(showOperations); setShowOperations(null); }}
      onAdjust={() => { setShowAdjustment(showOperations); setShowOperations(null); }}
      onWithdrawal={() => goToEmployeeOperation('withdrawals', showOperations.id)}
      onFreeze={() => { setShowOperations(null); void runAction(() => adminApi.setEmployeeStatus(session, showOperations.id, showOperations.status === 'ACTIVE' ? 'FROZEN' : 'ACTIVE'), '员工状态已更新。'); }}
      onResetPassword={() => { setShowPasswordReset(showOperations); setShowOperations(null); }}/>}
    {showAdjustment && <AdjustmentModal session={session} employee={showAdjustment} busy={busy} onNotice={setNotice} onClose={() => setShowAdjustment(null)} onDone={() => { setShowAdjustment(null); void load(); }}/>}
    {showPasswordReset && <ResetEmployeePasswordModal key={showPasswordReset.id} session={session} employee={showPasswordReset} onClose={() => { setShowPasswordReset(null); void load(); }}/>}
  </div>;
}

function TerminalManager({ session, items, loading, loadError, reload, onUnauthorized }: {
  session: AdminSession; items: AdminTerminal[]; loading: boolean; loadError: string; reload: () => void; onUnauthorized: () => void;
}) {
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [selected, setSelected] = useState<AdminTerminal | null>(null);
  async function create(event: FormEvent) {
    event.preventDefault(); setError(''); setNotice('');
    if (new TextEncoder().encode(name.trim()).length > 100) { setError('终端名称不能超过 100 字节。'); return; }
    setBusy(true);
    try {
      const item = await adminApi.createTerminal(session, name.trim());
      setName(''); setNotice('终端已创建。请查看 key 或下载配置。'); setSelected(item); reload();
    } catch (reason) {
      if (reason instanceof AdminApiError && reason.status === 401) onUnauthorized();
      else setError(message(reason));
    } finally { setBusy(false); }
  }
  const deviceStatus = (value: string) => ({ READY: '就绪', OFFLINE: '离线', UNKNOWN: '未报告', UNAVAILABLE: '未配置', FAILED: '异常' }[value] || value);
  return <Panel title="扫码终端" description="创建扫码点位并管理其 key。更换扫码枪时可继续使用原终端。">
    <form className="admin-form terminal-create" onSubmit={event => void create(event)}>
      <label>终端名称<input required maxLength={100} value={name} onChange={event => setName(event.target.value)} placeholder="例如：食堂一号终端"/></label>
      <button className="admin-primary" disabled={busy || loading}>{busy ? '正在创建…' : '创建终端'}</button>
    </form>
    {error && <ErrorBox>{error}</ErrorBox>}{notice && <p role="status">{notice}</p>}
    {loading ? <Loading/> : loadError ? <ErrorBox retry={reload}>{loadError}</ErrorBox> : items.length ? <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>终端</th><th>状态</th><th>最近心跳</th><th>扫码枪</th><th>语音</th><th>服务</th><th>操作</th></tr></thead><tbody>{items.map(item => <tr key={item.id}>
      <td><b>{item.name}</b><small className="terminal-id">{item.id}</small></td>
      <td><span className={`admin-status ${item.enabled ? 'ok' : 'warn'}`}>{item.enabled ? '启用' : '停用'}</span></td>
      <td>{item.last_seen_at ? new Date(item.last_seen_at).toLocaleString('zh-CN', { hour12: false }) : '未报告心跳'}</td>
      <td>{deviceStatus(item.scanner_status)}</td><td>{deviceStatus(item.voice_status)}</td><td>{deviceStatus(item.database_status)}</td>
      <td><button className="admin-link" type="button" onClick={() => { setNotice(''); setSelected(item); }}>管理</button></td>
    </tr>)}</tbody></table></div> : <div className="admin-empty">还没有扫码终端。填写名称即可创建。</div>}
    {selected && <TerminalSettings key={selected.id} session={session} terminal={selected} onClose={() => setSelected(null)} onUnauthorized={onUnauthorized} onChanged={item => { setSelected(item); reload(); }}/>}
  </Panel>;
}

function TerminalSettings({ session, terminal, onClose, onChanged, onUnauthorized }: {
  session: AdminSession; terminal: AdminTerminal; onClose: () => void; onChanged: (item: AdminTerminal) => void; onUnauthorized: () => void;
}) {
  const [name, setName] = useState(terminal.name);
  const [serverURL, setServerURL] = useState(window.location.origin);
  const [scannerMode, setScannerMode] = useState('serial');
  const [scannerDevice, setScannerDevice] = useState('/dev/ttyACM0');
  const [scannerBaud, setScannerBaud] = useState('9600');
  const [key, setKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  async function perform(action: () => Promise<void>) {
    setBusy(true); setError(''); setNotice('');
    try { await action(); }
    catch (reason) {
      if (reason instanceof AdminApiError && reason.status === 401) onUnauthorized();
      else setError(message(reason));
    } finally { setBusy(false); }
  }
  async function reveal(copy: boolean) {
    const value = (await adminApi.terminalKey(session, terminal.id)).key;
    setKey(value);
    if (copy) {
      if (navigator.clipboard) { try { await navigator.clipboard.writeText(value); setNotice('key 已复制。'); return; } catch { /* HTTP pages may not permit clipboard access. */ } }
      setNotice('浏览器未允许自动复制，请选中下方 key 手动复制。');
    }
  }
  async function download() {
    const file = await adminApi.terminalConfig(session, terminal.id, serverURL, { mode: scannerMode, device: scannerDevice, baud: scannerBaud });
    const url = URL.createObjectURL(file);
    const anchor = document.createElement('a'); anchor.href = url; anchor.download = 'terminal.env'; anchor.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    setNotice('配置已下载，包含所选扫码设备设置。请在扫码机器加载配置并重启 Agent。');
  }
  return <Modal title={`管理扫码终端 · ${terminal.name}`} onClose={() => { if (!busy) onClose(); }}>
    <div className="admin-form">
      <p className="admin-muted">编号：{terminal.id} · {terminal.enabled ? '启用' : '停用'}</p>
      {error && <ErrorBox>{error}</ErrorBox>}{notice && <p role="status">{notice}</p>}
      <form className="admin-form" onSubmit={event => { event.preventDefault(); void perform(async () => {
        await adminApi.updateTerminal(session, terminal.id, { name: name.trim() }); onChanged({ ...terminal, name: name.trim() }); setNotice('名称已保存。');
      }); }}>
        <label>终端名称<input required maxLength={100} disabled={busy} value={name} onChange={event => setName(event.target.value)}/></label>
        <button className="admin-secondary" disabled={busy || name.trim() === terminal.name}>保存名称</button>
      </form>
      <label>终端 key<input readOnly type={key ? 'text' : 'password'} value={key || '••••••••••••••••'} onFocus={event => { if (key) event.target.select(); }}/></label>
      {!terminal.credential_available && <p className="admin-muted">此终端的原 key 无法取回。可继续使用原凭据文件，或重置 key 后重新配置。</p>}
      <div className="admin-form-actions">
        <button className="admin-secondary" type="button" disabled={busy || !terminal.credential_available} onClick={() => void perform(() => reveal(false))}>查看 key</button>
        <button className="admin-secondary" type="button" disabled={busy || !terminal.credential_available} onClick={() => void perform(() => reveal(true))}>复制 key</button>
        {key && <button className="admin-secondary" type="button" disabled={busy} onClick={() => setKey('')}>隐藏 key</button>}
      </div>
      <label>服务端地址<input type="url" disabled={busy} value={serverURL} onChange={event => setServerURL(event.target.value)} placeholder="http://服务器:端口"/></label>
      <label>扫码设备模式<select disabled={busy} value={scannerMode} onChange={event => {
        const mode = event.target.value; setScannerMode(mode);
        setScannerDevice({ serial: '/dev/ttyACM0', evdev: '/dev/input/eventX', hidraw: '/dev/hidrawX' }[mode] || '');
      }}><option value="serial">串口（serial）</option><option value="evdev">键盘事件（evdev）</option><option value="hidraw">HID 报告（hidraw）</option></select></label>
      <label>扫码设备路径<input disabled={busy} value={scannerDevice} onChange={event => setScannerDevice(event.target.value)} placeholder="例如 /dev/ttyACM0"/></label>
      {scannerMode === 'serial' && <label>串口波特率<select disabled={busy} value={scannerBaud} onChange={event => setScannerBaud(event.target.value)}>{[1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200, 230400].map(baud => <option key={baud} value={baud}>{baud}</option>)}</select></label>}
      <p className="admin-muted">设备路径填写扫码机器上的路径。{scannerMode === 'serial' ? '串口使用 8N1，波特率须与扫码枪一致；扫码结束须发送回车或换行。' : '请确认扫码枪支持所选模式。'}这些设置用于本次下载配置。</p>
      <button className="admin-secondary" type="button" disabled={busy || !terminal.credential_available || !serverURL || !scannerDevice} onClick={() => void perform(download)}>下载配置</button>
      <div className="admin-form-actions">
        <button className="admin-secondary" type="button" disabled={busy} onClick={() => {
          if (!window.confirm('重置后旧 key 立即失效。需要更新 Agent 配置并重启，确定重置吗？')) return;
          setKey(''); void perform(async () => { await adminApi.resetTerminalKey(session, terminal.id); onChanged({ ...terminal, credential_available: true }); setNotice('key 已重置。请复制新 key 或下载配置，更新 Agent 后重启。'); });
        }}>重置 key</button>
        <button className="admin-secondary" type="button" disabled={busy} onClick={() => {
          if (terminal.enabled && !window.confirm('停用后该终端无法扫码或查询结果，历史记录会保留。确定停用吗？')) return;
          void perform(async () => { await adminApi.updateTerminal(session, terminal.id, { enabled: !terminal.enabled }); onChanged({ ...terminal, enabled: !terminal.enabled }); setNotice(terminal.enabled ? '终端已停用。' : '终端已启用。'); });
        }}>{terminal.enabled ? '停用终端' : '启用终端'}</button>
        <button className="admin-secondary" type="button" disabled={busy} onClick={onClose}>关闭</button>
      </div>
    </div>
  </Modal>;
}

function Modal({ title, children, onClose, className = '' }: { title: string; children: ReactNode; onClose: () => void; className?: string }) {
  return <div className="admin-modal-backdrop" onMouseDown={event => { if (event.target === event.currentTarget) onClose(); }}><section className={`admin-modal ${className}`} role="dialog" aria-modal="true" aria-label={title}><header><h2>{title}</h2><button type="button" aria-label="关闭" onClick={onClose}>×</button></header>{children}</section></div>;
}

function CreateEmployee({ onClose, onSave }: { onClose: () => void; onSave: (input: Pick<AdminEmployee, 'employee_no' | 'name' | 'phone' | 'department'>) => void }) {
  const [form, setForm] = useState({ employee_no: '', name: '', phone: '', department: '' });
  return <Modal title="新建员工档案" onClose={onClose}><form className="admin-form" onSubmit={e => { e.preventDefault(); onSave(form); }}>
    {([['employee_no', '员工编号'], ['name', '姓名'], ['phone', '手机号'], ['department', '部门']] as const).map(([key, label]) => <label key={key}>{label}<input required value={form[key]} onChange={e => setForm({ ...form, [key]: e.target.value })}/></label>)}
    <div className="admin-form-actions"><button className="admin-secondary" type="button" onClick={onClose}>取消</button><button className="admin-primary">创建账户</button></div>
  </form></Modal>;
}

function EditEmployee({ employee, onClose, onSave }: { employee: AdminEmployee; onClose: () => void; onSave: (input: Pick<AdminEmployee, 'employee_no' | 'name' | 'phone' | 'department' | 'photo_url'>) => void }) {
  const [form, setForm] = useState({ employee_no: employee.employee_no, name: employee.name, phone: employee.phone, department: employee.department, photo_url: employee.photo_url || '' });
  return <Modal title={`编辑员工 · ${employee.name}`} onClose={onClose}><form className="admin-form" onSubmit={e => { e.preventDefault(); onSave({ ...form, photo_url: form.photo_url.trim() || null }); }}>
    {([['employee_no', '员工编号'], ['name', '姓名'], ['phone', '手机号'], ['department', '部门'], ['photo_url', '头像地址（可留空）']] as const).map(([key, label]) => <label key={key}>{label}<input required={key !== 'photo_url'} value={form[key]} onChange={e => setForm({ ...form, [key]: e.target.value })}/></label>)}
    <div className="admin-form-actions"><button className="admin-secondary" type="button" onClick={onClose}>取消</button><button className="admin-primary">保存档案</button></div>
  </form></Modal>;
}

function RechargeModal({ employee, onClose, onSave }: { employee: AdminEmployee; onClose: () => void; onSave: (input: { employee_id: number; amount_cents: number; receipt_ref: string; collected_at: string; payment_method: string; idempotency_key: string }) => void }) {
  const [amount, setAmount] = useState(''); const [receipt, setReceipt] = useState(''); const [method, setMethod] = useState('CASH');
  return <Modal title={`为 ${employee.name} 登记线下充值`} onClose={onClose}><form className="admin-form" onSubmit={e => { e.preventDefault(); onSave({ employee_id: employee.id, amount_cents: cents(amount), receipt_ref: receipt.trim(), collected_at: new Date().toISOString(), payment_method: method, idempotency_key: idempotencyKey() }); }}>
    <p className="admin-muted">当前余额 {yuan(employee.balance)}。请先确认已实际收款，并填写唯一凭据。</p>
    <label>充值金额（元）<input type="number" min="0.01" step="0.01" required value={amount} onChange={e => setAmount(e.target.value)}/></label>
    <label>收款凭据编号<input required value={receipt} onChange={e => setReceipt(e.target.value)} maxLength={120}/></label>
    <label>收款方式<select value={method} onChange={e => setMethod(e.target.value)}><option value="CASH">现金</option><option value="BANK_TRANSFER">银行转账</option><option value="WECHAT">微信</option><option value="ALIPAY">支付宝</option><option value="OTHER">其他线下方式</option></select></label>
    <div className="admin-form-actions"><button className="admin-secondary" type="button" onClick={onClose}>取消</button><button className="admin-primary">确认已收款并登记</button></div>
  </form></Modal>;
}

/** The single operations entry on an employee row. It carries the whole set of
 * actions for that person so the row stops being a row of buttons, and it takes
 * the employee from the moment it opened — it holds no employee picker and
 * issues no request of its own.
 *
 * The account state decides what is offered: the server accepts only the two
 * refund bookings on a closed account, so the other four actions are shown
 * disabled with the reason rather than letting the administrator fill in a form
 * that can only fail. */
function AccountOperations({ employee, onClose, onEdit, onRecharge, onAdjust, onWithdrawal, onFreeze, onResetPassword }: {
  employee: AdminEmployee; onClose: () => void; onEdit: () => void; onRecharge: () => void;
  onAdjust: () => void; onWithdrawal: () => void; onFreeze: () => void; onResetPassword: () => void;
}) {
  const closed = employee.account_status === 'CLOSED';
  const reasonID = useId();
  const reason = closed ? '账户已关户，此操作不可用。' : '';
  /** A disabled action has to explain itself, so each one carries the reason
   * inline and points at the shared explanation above the list. Withdrawal is
   * the one action a closed account still accepts, so it opts out. */
  const action = (label: string, detail: string, onClick: () => void, blockedOnClosed = true) => {
    const blocked = closed && blockedOnClosed;
    return <button type="button" className="admin-op-action" disabled={blocked} aria-describedby={blocked ? reasonID : undefined} onClick={onClick}><b>{label}</b><small>{blocked ? reason : detail}</small></button>;
  };
  return <Modal title={`账户操作 · ${employee.name}`} onClose={onClose}>
    <div className="admin-op-summary">
      <span>目标员工</span><strong>{employee.name}（{employee.employee_no}）</strong>
      <small>{employee.department || '未填写部门'} · 员工状态 {accountStatusLabel(employee.status)} · 账户状态 {accountStatusLabel(employee.account_status)}</small>
      <span>当前余额</span><em>{yuan(employee.balance)}</em>
    </div>
    {closed && <p className="admin-op-reason" id={reasonID} role="note">账户已关户，服务端只接受「消费退款」与「余额退还」两类记账，因此「编辑档案」「登记充值」「余额调整」「重置密码」不可用。</p>}
    <div className="admin-op-actions">
      {action('编辑档案', '修改姓名、工号、手机号与部门', onEdit)}
      {action('登记充值', '登记线下收款并增加余额', onRecharge)}
      {action('余额调整', '按审批原因增减余额', onAdjust)}
      {action(closed ? '退还余额' : '退还并关户', closed ? '从未发放的退款记录中单选一笔发放' : '线下退还全部余额并关闭账户（不可逆）', onWithdrawal, false)}
      {employee.status === 'ACTIVE' || employee.status === 'FROZEN' ? action(employee.status === 'ACTIVE' ? '冻结账户' : '解冻账户', employee.status === 'ACTIVE' ? '暂停该员工的消费与登录' : '恢复正常消费与登录', onFreeze) : null}
      {action('重置密码', '手动指定密码或生成 6 位数字密码', onResetPassword)}
    </div>
    <div className="admin-form-actions"><button className="admin-secondary" type="button" onClick={onClose}>关闭面板</button></div>
  </Modal>;
}

function ResetEmployeePasswordModal({ session, employee, onClose }: { session: AdminSession; employee: AdminEmployee; onClose: () => void }) {
  const [method, setMethod] = useState<'generated' | 'manual'>('generated');
  const [password, setPassword] = useState(''); const [confirm, setConfirm] = useState('');
  const [issuedPassword, setIssuedPassword] = useState(''); const [busy, setBusy] = useState(false);
  const [error, setError] = useState(''); const [copied, setCopied] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault(); setError('');
    if (method === 'manual') {
      if ([...password].length < 6) { setError('密码至少需要 6 位，不限制字符类型。'); return; }
      if (new TextEncoder().encode(password).length > 1024) { setError('密码不能超过 1024 字节。'); return; }
      if (password !== confirm) { setError('两次输入的密码不一致。'); return; }
    }
    setBusy(true);
    try {
      const result = await adminApi.resetEmployeePassword(session, employee.id, method === 'manual' ? password : undefined);
      setIssuedPassword(result.temporary_password);
    } catch (reason) { setError(message(reason)); }
    finally { setBusy(false); }
  }
  async function copyPassword() {
    try {
      await navigator.clipboard.writeText(issuedPassword);
      setCopied(true);
    } catch { setCopied(false); }
  }
  return <Modal title={issuedPassword ? '密码已重置' : `重置密码 · ${employee.name}`} onClose={() => { if (!busy) onClose(); }} className="employee-password-modal">
    {issuedPassword ? <>
      <div className="admin-alert success" role="status">员工下次登录时需要修改密码。请通过安全渠道交付此密码。</div>
      <label className="issued-password-label">临时密码<input type="text" readOnly value={issuedPassword} onFocus={event => event.currentTarget.select()}/></label>
      <div className="admin-form-actions"><button className="admin-secondary" type="button" onClick={() => void copyPassword()}>{copied ? '已复制' : '复制密码'}</button><button className="admin-primary" type="button" onClick={onClose}>完成</button></div>
      {!copied && <p className="admin-muted">如果无法复制，可选中密码后手动复制。</p>}
    </> : <>
      <p className="admin-muted password-reset-hint">手动密码至少 6 位，可使用数字、字母或其他字符。系统生成的密码为 6 位数字。</p>
      <form className="admin-form" onSubmit={submit}>
        <fieldset className="password-reset-method"><legend>选择密码方式</legend>
          <label><input type="radio" name="password-method" checked={method === 'generated'} onChange={() => setMethod('generated')}/><span><strong>系统生成</strong><small>自动生成 6 位数字密码</small></span></label>
          <label><input type="radio" name="password-method" checked={method === 'manual'} onChange={() => setMethod('manual')}/><span><strong>手动指定</strong><small>设置员工下次登录使用的密码</small></span></label>
        </fieldset>
        {method === 'manual' && <>
          <label>新密码<input type="password" autoComplete="new-password" minLength={6} maxLength={1024} value={password} onChange={event => setPassword(event.target.value)}/></label>
          <label>确认新密码<input type="password" autoComplete="new-password" minLength={6} maxLength={1024} value={confirm} onChange={event => setConfirm(event.target.value)}/></label>
        </>}
        {error && <div className="admin-alert error" role="alert">{error}</div>}
        <div className="admin-form-actions"><button className="admin-secondary" type="button" disabled={busy} onClick={onClose}>取消</button><button className="admin-primary" disabled={busy}>{busy ? '正在重置…' : '重置密码'}</button></div>
      </form>
    </>}
  </Modal>;
}

function EmployeeSelector({ session, value, onChange, initialEmployeeId, label = '员工' }: {  session: AdminSession; value: AdminEmployee | null; onChange: (employee: AdminEmployee | null) => void;
  initialEmployeeId?: number; label?: string;
}) {
  const inputID = useId();
  const [query, setQuery] = useState('');
  const [page, setPage] = useState<AdminEmployee[]>([]);
  const [nextCursor, setNextCursor] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const requestID = useRef(0);
  const loadedInitialID = useRef<number | undefined>(undefined);

  useEffect(() => {
    if (!initialEmployeeId || loadedInitialID.current === initialEmployeeId) return;
    loadedInitialID.current = initialEmployeeId;
    let cancelled = false;
    void adminApi.generic<AdminEmployee>(session, `/api/admin/employees/${initialEmployeeId}`)
      .then(employee => { if (!cancelled) onChange(employee); })
      .catch(reason => { if (!cancelled) setError(message(reason)); });
    return () => { cancelled = true; };
  }, [session, initialEmployeeId, onChange]);

  useEffect(() => {
    if (!open || value) return;
    const currentRequest = ++requestID.current;
    const timer = window.setTimeout(() => {
      setBusy(true); setError('');
      void adminApi.searchEmployees(session, { q: query.trim(), limit: 50 })
        .then(result => {
          if (currentRequest !== requestID.current) return;
          setPage(result.employees || []); setNextCursor(result.next_cursor || ''); setActive(0);
        })
        .catch(reason => { if (currentRequest === requestID.current) setError(message(reason)); })
        .finally(() => { if (currentRequest === requestID.current) setBusy(false); });
    }, 180);
    return () => { window.clearTimeout(timer); requestID.current += 1; };
  }, [session, query, open, value]);

  async function more() {
    if (!nextCursor || busy) return;
    const currentRequest = ++requestID.current; setBusy(true); setError('');
    try {
      const result = await adminApi.searchEmployees(session, { q: query.trim(), limit: 50, cursor: nextCursor });
      if (currentRequest === requestID.current) { setPage(current => [...current, ...result.employees]); setNextCursor(result.next_cursor || ''); }
    } catch (reason) { if (currentRequest === requestID.current) setError(message(reason)); }
    finally { if (currentRequest === requestID.current) setBusy(false); }
  }

  function choose(employee: AdminEmployee) { onChange(employee); setOpen(false); setQuery(''); setError(''); }
  function onKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown') { event.preventDefault(); setOpen(true); setActive(index => Math.min(index + 1, Math.max(page.length - 1, 0))); }
    else if (event.key === 'ArrowUp') { event.preventDefault(); setActive(index => Math.max(index - 1, 0)); }
    else if (event.key === 'Enter' && open && page[active]) { event.preventDefault(); choose(page[active]); }
    else if (event.key === 'Escape') { event.preventDefault(); if (value) onChange(null); else { setQuery(''); setOpen(false); } }
  }

  if (value) return <div className="admin-employee-picker">
    <span className="admin-muted">{label}</span>
    <div className="admin-note admin-employee-selected"><strong>{value.name}</strong> · 工号 {value.employee_no} · {value.department || '未填写部门'}
      <div>余额 {yuan(value.balance)} · 账户状态 {accountStatusLabel(value.account_status)}</div>
      <button className="admin-link" type="button" onClick={() => { onChange(null); setOpen(true); setQuery(''); }}>更换员工</button>
    </div>
  </div>;

  return <div className="admin-employee-picker">
    <label htmlFor={inputID}>{label}<input id={inputID} type="search" role="combobox" aria-autocomplete="list" aria-expanded={open} aria-controls={`${inputID}-results`} autoComplete="off" placeholder="姓名、工号或手机号" value={query} onFocus={() => setOpen(true)} onChange={event => { setQuery(event.target.value); setOpen(true); }} onKeyDown={onKeyDown}/></label>
    {open && <div id={`${inputID}-results`} className="admin-table-wrap" role="listbox" aria-label="员工搜索结果">
      {busy && <p className="admin-muted" role="status">正在搜索员工…</p>}
      {error && <div className="admin-alert error" role="alert">{error} <button className="admin-link" type="button" onClick={() => { setOpen(false); window.setTimeout(() => setOpen(true), 0); }}>重试</button></div>}
      {!busy && !error && page.length === 0 && <p className="admin-empty">没有找到员工，请检查姓名、工号或手机号。</p>}
      {page.map((employee, index) => <button className="admin-link admin-employee-option" style={{ display: 'block', width: '100%', textAlign: 'left', whiteSpace: 'normal' }} type="button" role="option" aria-selected={active === index} key={employee.id} onMouseEnter={() => setActive(index)} onClick={() => choose(employee)}>
        <strong>{employee.name}</strong> · {employee.employee_no}<span> · {employee.department || '未填写部门'}</span>
      </button>)}
      {nextCursor && <button className="admin-secondary compact" type="button" disabled={busy} onClick={() => void more()}>加载更多匹配员工</button>}
    </div>}
  </div>;
}

function accountStatusLabel(status: string): string {
  return status === 'ACTIVE' ? '正常' : status === 'FROZEN' ? '冻结' : status === 'CLOSED' ? '已关闭' : status;
}

/** The balance adjustment from the account operations panel. The employee is
 * fixed by the caller, so the form carries no picker and the person cannot
 * change mid-flight. The mount key is per account, so each opening gets a fresh
 * idempotency intent: a retry inside one opening reuses the pending key, while
 * reopening and submitting the same numbers is a new adjustment. */
function AdjustmentModal({ session, employee, busy, onNotice, onClose, onDone }: {
  session: AdminSession; employee: AdminEmployee; busy: boolean; onNotice: (value: string) => void; onClose: () => void; onDone: () => void;
}) {
  return <Modal title={`余额调整 · ${employee.name}`} onClose={onClose}>
    <AdjustmentForm key={employee.account_id} session={session} employee={employee} busy={busy} onNotice={onNotice} onDone={onDone} onCancel={onClose}/>
  </Modal>;
}

function AdjustmentForm({ session, employee, busy, onNotice, onDone, onCancel }: {
  session: AdminSession; employee: AdminEmployee; busy: boolean; onNotice: (value: string) => void; onDone: () => void; onCancel?: () => void;
}) {
  const [direction, setDirection] = useState<'increase' | 'decrease'>('increase');
  const [amount, setAmount] = useState(''); const [reason, setReason] = useState('');
  const [saving, setSaving] = useState(false); const [error, setError] = useState('');
  const retryIntent = useRef<{ signature: string; key: string } | null>(null);
  const amountCents = moneyInputCents(amount);
  const signedAmount = amountCents == null ? 0 : direction === 'increase' ? amountCents : -amountCents;
  const projectedBalance = amountCents == null ? null : employee.balance + signedAmount;
  const valid = Boolean(amountCents != null && reason.trim() && projectedBalance != null && projectedBalance >= 0);

  async function submit(event: FormEvent) {
    event.preventDefault(); setError('');
    if (amountCents == null || !reason.trim() || projectedBalance == null || projectedBalance < 0) {
      setError('请核对正数金额和原因；减少金额不能超过当前余额。'); return;
    }
    const centsToApply = direction === 'increase' ? amountCents : -amountCents;
    const signature = `${employee.account_id}|${centsToApply}|${reason.trim()}`;
    if (retryIntent.current?.signature !== signature) retryIntent.current = { signature, key: idempotencyKey() };
    setSaving(true);
    try {
      await adminApi.adjust(session, employee.account_id, centsToApply, reason.trim(), retryIntent.current.key);
      retryIntent.current = null;
      onNotice('余额调整已登记，资金流水和审计记录已更新。');
      onDone();
    } catch (reasonValue) { setError(message(reasonValue)); }
    finally { setSaving(false); }
  }

  return <form className="admin-form adjustment-flow" onSubmit={submit}>
    <div className="balance-current"><span>当前余额</span><strong>{yuan(employee.balance)}</strong><small>{employee.name} · {employee.employee_no} · 账户状态：{accountStatusLabel(employee.account_status)}</small></div>
    <fieldset className="direction-choice"><legend>调整方向</legend><label className={direction === 'increase' ? 'active' : ''}><input type="radio" name="adjustment-direction" checked={direction === 'increase'} onChange={() => setDirection('increase')}/>增加余额</label><label className={direction === 'decrease' ? 'active' : ''}><input type="radio" name="adjustment-direction" checked={direction === 'decrease'} onChange={() => setDirection('decrease')}/>减少余额</label></fieldset>
    <label className="money-entry">调整金额（元）<input inputMode="decimal" type="number" min="0.01" step="0.01" required value={amount} onChange={event => setAmount(event.target.value)} placeholder="例如 20.00"/><small>输入正数；系统会按所选方向增加或减少。</small></label>
    <label>账务原因<textarea required maxLength={512} rows={3} value={reason} onChange={event => setReason(event.target.value)} placeholder="例如：按审批单补记餐费补贴"/></label>
    <div className="impact-preview" aria-live="polite"><h3>余额变化预览</h3><div><span>{yuan(employee.balance)}</span><b>{amountCents == null ? '填写金额后显示' : `${direction === 'increase' ? '+' : '−'} ${yuan(amountCents)}`}</b><strong>{projectedBalance == null ? '—' : yuan(projectedBalance)}</strong></div><small>{direction === 'increase' ? '调整后余额 = 当前余额 + 调整金额' : '调整后余额 = 当前余额 − 调整金额'}</small></div>
    {error && <div className="admin-alert error" role="alert">{error}</div>}
    <div className="admin-form-actions">{onCancel && <button type="button" className="admin-secondary" onClick={onCancel}>取消</button>}<button className="admin-primary" disabled={busy || saving || !valid}>{saving ? '正在登记…' : !valid && projectedBalance != null && projectedBalance < 0 ? '余额不足，无法确认' : amountCents == null ? '填写有效金额后确认' : `确认${direction === 'increase' ? '增加' : '减少'} ${yuan(amountCents)}`}</button></div>
  </form>;
}

function localDateTimeNow(): string {
  const date = new Date(); date.setMinutes(date.getMinutes() - date.getTimezoneOffset());
  return date.toISOString().slice(0, 16);
}

/** What the page shows after a payout succeeds. It is taken from the response
 * and the submitted fields, so no extra query is needed and the receipt survives
 * the inputs being cleared for the next person. */
type WithdrawalReceipt = {
  amount_cents: number;
  payout_ref: string;
  paid_at: string;
  transaction_no: string;
  closed_account: boolean;
  account_status: string;
};

/** The withdrawal secondary page. It is reachable only with a known employee —
 * from the account operations panel, or from the refund flow's deep link — so it
 * carries no employee picker and cannot be redirected to someone else. That is
 * deliberate: the three branches below are decided entirely by the account's
 * state, so nobody should be able to change the subject halfway through.
 *
 * A closed account is a terminal state, so a successful payout stays on the page
 * with its receipt instead of navigating away. */
function WithdrawalPage({ session, initialEmployeeId, initialRefundId, busy, onNotice, onNavigate }: {
  session: AdminSession; initialEmployeeId?: number; initialRefundId?: string; busy: boolean;
  onNotice: (value: string) => void; onNavigate: (page: 'employees' | 'refunds') => void;
}) {
  const [employee, setEmployee] = useState<AdminEmployee | null>(null);
  const [loadingEmployee, setLoadingEmployee] = useState(Boolean(initialEmployeeId));
  const [reloadToken, setReloadToken] = useState(0);
  const [refunds, setRefunds] = useState<AdminTransaction[]>([]); const [refundCursor, setRefundCursor] = useState('');
  const [selectedRefund, setSelectedRefund] = useState<AdminTransaction | null>(null);
  const [receipt, setReceipt] = useState(''); const [paidAt, setPaidAt] = useState(localDateTimeNow()); const [method, setMethod] = useState('BANK_TRANSFER');
  const [loadingRefunds, setLoadingRefunds] = useState(false); const [saving, setSaving] = useState(false); const [error, setError] = useState('');
  const [payout, setPayout] = useState<WithdrawalReceipt | null>(null);
  const retryIntent = useRef<{ signature: string; key: string } | null>(null);
  const refundCursorRef = useRef('');
  refundCursorRef.current = refundCursor;

  // Arriving with a refund identifier means the refund flow sent us here; going
  // back must return there rather than to the employee list.
  const backPage: 'employees' | 'refunds' = initialRefundId ? 'refunds' : 'employees';

  // The employee is read once from the identifier in the URL. The dependency is
  // just the identifier, so a parent re-render can never discard this read.
  useEffect(() => {
    if (!initialEmployeeId) { setEmployee(null); return; }
    let cancelled = false;
    setLoadingEmployee(true); setError('');
    void adminApi.generic<AdminEmployee>(session, `/api/admin/employees/${initialEmployeeId}`)
      .then(current => { if (!cancelled) setEmployee(current); })
      .catch(reason => { if (!cancelled) setError(message(reason)); })
      .finally(() => { if (!cancelled) setLoadingEmployee(false); });
    return () => { cancelled = true; };
  }, [session, initialEmployeeId, reloadToken]);

  const refreshRefunds = useCallback(async (append = false) => {
    if (!employee || employee.account_status !== 'CLOSED') { setRefunds([]); setRefundCursor(''); return; }
    setLoadingRefunds(true); setError('');
    try {
      const page = await adminApi.transactions(session, { employee_id: String(employee.id), type: 'REFUND', limit: 50, ...(append && refundCursorRef.current ? { cursor: refundCursorRef.current } : {}) });
      const available = page.items.filter(item => item.payout_status === 'AVAILABLE');
      setRefunds(current => append ? [...current, ...available] : available);
      setRefundCursor(page.next_cursor || '');
    } catch (reason) { setError(message(reason)); }
    finally { setLoadingRefunds(false); }
  }, [session, employee?.id, employee?.account_status]);

  useEffect(() => {
    if (employee?.account_status === 'CLOSED') void refreshRefunds();
    else { setRefunds([]); setSelectedRefund(null); setRefundCursor(''); }
  }, [employee?.id, employee?.account_status, refreshRefunds]);

  useEffect(() => {
    if (!initialRefundId || !employee || employee.account_status !== 'CLOSED') return;
    let cancelled = false;
    void adminApi.transaction(session, initialRefundId).then(({ transaction }) => {
      if (cancelled) return;
      if (transaction.employee_id !== employee.id || transaction.type !== 'REFUND' || transaction.payout_status !== 'AVAILABLE') {
        setError('该退款不属于此员工，或已经线下退还。'); return;
      }
      setSelectedRefund(transaction);
    }).catch(reason => { if (!cancelled) setError(message(reason)); });
    return () => { cancelled = true; };
  }, [session, employee?.id, employee?.account_status, initialRefundId]);

  /** Closing the account moves it to the terminal state, so the page keeps it on
   * screen: the zero-balance card disappears and the payout receipt stays. */
  function markClosed(updated: AdminEmployee) {
    setEmployee(updated);
    setRefunds([]); setSelectedRefund(null); setRefundCursor('');
  }

  async function closeZeroBalance() {
    if (!employee || employee.balance !== 0 || (employee.status !== 'ACTIVE' && employee.status !== 'FROZEN')) return;
    if (!window.confirm(`确认关闭 ${employee.name}（${employee.employee_no}）的零余额账户？此操作会永久关闭账户并撤销员工会话。`)) return;
    setSaving(true); setError(''); setPayout(null);
    try {
      await adminApi.setEmployeeStatus(session, employee.id, 'CLOSED');
      onNotice('零余额账户已关闭，没有生成虚构的付款流水。');
      markClosed({ ...employee, status: 'CLOSED', account_status: 'CLOSED' });
    } catch (reason) { setError(message(reason)); }
    finally { setSaving(false); }
  }

  async function payOut(event: FormEvent) {
    event.preventDefault(); setError(''); setPayout(null);
    if (!employee || !receipt.trim() || !paidAt || (employee.account_status === 'CLOSED' && !selectedRefund)) {
      setError('请填写线下付款时间和唯一凭据；已关户账户还需选定一笔可退退款记录。'); return;
    }
    const refundedClosedAccount = employee.account_status === 'CLOSED';
    const relatedRefundID = selectedRefund?.id || 0;
    const amountCents = selectedRefund ? Math.abs(selectedRefund.amount_cents) : employee.balance;
    const paidAtISO = new Date(paidAt).toISOString();
    const signature = `${employee.account_id}|${relatedRefundID}|${receipt.trim()}|${paidAtISO}|${method}|${amountCents}`;
    if (retryIntent.current?.signature !== signature) retryIntent.current = { signature, key: idempotencyKey() };
    setSaving(true);
    try {
      const response = await adminApi.generic<{ transaction?: AdminTransaction }>(session, `/api/admin/accounts/${employee.account_id}/withdraw`, { method: 'POST', body: JSON.stringify({ payout_ref: receipt.trim(), paid_at: paidAtISO, payment_method: method, idempotency_key: retryIntent.current.key, related_refund_transaction_id: relatedRefundID }) });
      retryIntent.current = null;
      const transaction = response.transaction;
      setPayout({
        amount_cents: transaction ? Math.abs(transaction.amount_cents) : amountCents,
        payout_ref: receipt.trim(),
        paid_at: paidAt,
        transaction_no: transaction?.transaction_no || '—',
        closed_account: !refundedClosedAccount,
        // A payout either keeps the account closed (refund of a closed account)
        // or closes it (returning the whole balance), so it is always closed.
        account_status: 'CLOSED',
      });
      onNotice(refundedClosedAccount ? '该笔历史消费退款已线下退还；员工账户仍保持关闭。' : '余额已按记录的线下付款全额退还，员工账户已关闭。');
      setReceipt('');
      if (refundedClosedAccount) {
        // The account was already closed: the employee stays closed and the
        // payout history loses the refund that was just paid out.
        setSelectedRefund(null);
        if (transaction) setEmployee(current => current ? { ...current, balance: transaction.after_balance_cents } : current);
        await refreshRefunds();
      } else if (transaction) {
        markClosed({ ...employee, balance: transaction.after_balance_cents, account_status: 'CLOSED', status: 'CLOSED' });
      }
    } catch (reason) { setError(message(reason)); }
    finally { setSaving(false); }
  }

  async function loadMoreRefunds() {
    if (!refundCursor || loadingRefunds) return;
    await refreshRefunds(true);
  }

  return <div className="admin-form withdrawal-flow">
    <div className="withdrawal-head">
      <button className="admin-secondary" type="button" onClick={() => onNavigate(backPage)}>{backPage === 'refunds' ? '返回消费退款' : '返回员工管理'}</button>
      <p className="admin-muted">本页只对进入时指定的员工操作，不能在此更换对象；三条分支由账户状态决定。</p>
    </div>
    {!initialEmployeeId ? <div className="admin-empty">此页面需要指定员工。请从员工管理的「账户操作」进入。</div>
      : loadingEmployee && !employee ? <Loading/>
      : !employee ? <ErrorBox retry={() => setReloadToken(token => token + 1)}>{error || '无法读取该员工。'}</ErrorBox>
      : <>
        <div className="withdrawal-target">
          <div><span>退还对象</span><strong>{employee.name}</strong><small>工号 {employee.employee_no} · {employee.department || '未填写部门'}</small></div>
          <div><span>账户状态</span><strong>{accountStatusLabel(employee.account_status)}</strong><small>员工状态 {accountStatusLabel(employee.status)}</small></div>
          <div><span>当前余额</span><strong className="money">{yuan(employee.balance)}</strong><small>金额由服务端按状态推导</small></div>
        </div>
        {payout && <section className="withdrawal-receipt" role="status">
          <h3>已登记线下退还</h3>
          <dl>
            <div><dt>退还金额</dt><dd>{yuan(payout.amount_cents)}</dd></div>
            <div><dt>收款凭据</dt><dd>{payout.payout_ref}</dd></div>
            <div><dt>付款日期</dt><dd>{formatAdminDate(payout.paid_at)}</dd></div>
            <div><dt>流水标识</dt><dd>{payout.transaction_no}</dd></div>
            <div><dt>是否关户</dt><dd>{payout.closed_account ? '本次已退还并关户' : '账户原已关户，未再次关户'}</dd></div>
            <div><dt>账户最新状态</dt><dd>{accountStatusLabel(payout.account_status)}</dd></div>
          </dl>
          <p className="admin-muted">关户是终态操作，本页停留在回执上以便核对；需要时可截图或抄录后自行返回。</p>
        </section>}
        {employee.account_status === 'CLOSED' && <section className="payout-history"><h3>未线下退还的历史消费退款</h3><p className="admin-muted">仅显示属于该员工且尚未登记线下付款的退款流水。请选择一笔全额退还，账户仍会保持关闭。</p>
          {loadingRefunds && !refunds.length ? <Loading/> : error && !refunds.length ? <ErrorBox retry={() => void refreshRefunds()}>{error}</ErrorBox> : refunds.length ? <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>選</th><th>原消费退款流水</th><th>退款金额</th><th>退款时间</th><th>操作</th></tr></thead><tbody>{refunds.map(refund => <tr key={refund.id}><td><input type="radio" name="closed-refund" aria-label={`选择退款 ${refund.transaction_no}`} checked={selectedRefund?.id === refund.id} onChange={() => { setSelectedRefund(refund); setError(''); }}/></td><td>{refund.transaction_no}<small className="admin-muted">关联消费 {refund.related_transaction?.transaction_no || refund.related_transaction_id}</small></td><td>{yuan(Math.abs(refund.amount_cents))}</td><td>{formatAdminDate(refund.created_at)}</td><td><button className="admin-link" type="button" onClick={() => setSelectedRefund(refund)}>选择此退款</button></td></tr>)}</tbody></table></div> : <div className="admin-empty">没有待线下退还的历史消费退款。</div>}
          {refundCursor && <button className="admin-secondary" type="button" disabled={loadingRefunds} onClick={() => void loadMoreRefunds()}>加载更多退款</button>}
        </section>}
        {(employee.account_status === 'ACTIVE' || employee.account_status === 'FROZEN') && employee.balance === 0 && <section className="zero-close-card"><h3>余额为零，可以直接关户</h3><p>系统不会登记付款凭据，也不会生成零金额交易。</p><button className="admin-secondary" disabled={busy || saving} onClick={() => void closeZeroBalance()}>确认关闭此账户</button></section>}
        {(employee.account_status === 'ACTIVE' || employee.account_status === 'FROZEN') && employee.balance > 0 && <form className="withdrawal-payment" onSubmit={payOut}>
          <h3>退还全部余额并关户</h3><p>当前需要实际线下退还 <strong>{yuan(employee.balance)}</strong>。登记付款前请先完成现金或转账。</p>
          <PayoutFields receipt={receipt} onReceipt={setReceipt} paidAt={paidAt} onPaidAt={setPaidAt} method={method} onMethod={setMethod}/>
          <div className="confirm-summary"><h3>付款确认</h3><p>{employee.name}（{employee.employee_no}）· {accountStatusLabel(employee.account_status)}账户 · 线下退还 {yuan(employee.balance)} · 退还后关户并撤销登录会话。</p></div>
          <button className="admin-primary" disabled={busy || saving || !receipt.trim() || !paidAt}>{saving ? '正在记录付款并关户…' : `确认已线下退还 ${yuan(employee.balance)} 并关户`}</button>
        </form>}
        {employee.account_status === 'CLOSED' && <form className="withdrawal-payment" onSubmit={payOut}>
          <h3>登记所选退款的线下付款</h3>
          {selectedRefund ? <div className="confirm-summary"><p><strong>{employee.name}（{employee.employee_no}）</strong></p><p>原消费退款 {selectedRefund.transaction_no} · {yuan(Math.abs(selectedRefund.amount_cents))} · 账户当前余额 {yuan(employee.balance)}</p><p>此后账户仍保持关闭。</p></div> : <p className="admin-muted">先从上方选择一笔未退的退款记录。</p>}
          <PayoutFields receipt={receipt} onReceipt={setReceipt} paidAt={paidAt} onPaidAt={setPaidAt} method={method} onMethod={setMethod}/>
          <button className="admin-primary" disabled={busy || saving || !selectedRefund || !receipt.trim() || !paidAt}>{saving ? '正在记录线下付款…' : selectedRefund ? `确认已线下退还 ${yuan(Math.abs(selectedRefund.amount_cents))}` : '选择退款后确认付款'}</button>
        </form>}
        {error && <div className="admin-alert error" role="alert">{error}</div>}
      </>}
  </div>;
}

function PayoutFields({ receipt, onReceipt, paidAt, onPaidAt, method, onMethod }: { receipt: string; onReceipt: (value: string) => void; paidAt: string; onPaidAt: (value: string) => void; method: string; onMethod: (value: string) => void }) {
  return <div className="admin-form payout-fields"><label>实际付款时间<input type="datetime-local" required value={paidAt} onChange={event => onPaidAt(event.target.value)}/></label><label>实际付款方式<select value={method} onChange={event => onMethod(event.target.value)}><option value="BANK_TRANSFER">银行转账</option><option value="CASH">现金</option><option value="OTHER">其他线下方式</option></select></label><label>唯一付款凭据<input required maxLength={128} value={receipt} onChange={event => onReceipt(event.target.value)} placeholder="转账单号或现金收据编号"/></label></div>;
}

function formatAdminDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short', hour12: false }).format(date);
}

function transactionTypeLabel(type: string): string {
  const labels: Record<string, string> = { RECHARGE: '线下充值', RECHARGE_REVERSAL: '充值冲正', CONSUME: '餐费消费', REFUND: '消费退款', BALANCE_ADJUSTMENT: '余额调整', BALANCE_WITHDRAWAL: '余额退还' };
  return labels[type] || type;
}
function mealCodeLabel(code: string): string {
  return code === 'BREAKFAST' ? '早餐' : code === 'LUNCH' ? '午餐' : code === 'DINNER' ? '晚餐' : code || '餐次';
}
function blockReasonLabel(reason: string): string {
  return reason === 'already_refunded' ? '该消费已退款' : reason === 'not_consumption' ? '原流水不是消费' : reason === 'already_reversed' ? '该充值已冲正' : reason === 'not_recharge' ? '原流水不是充值' : reason || '状态已变化';
}

function TransactionTable({ rows, onView, onNavigate }: { rows: AdminTransaction[]; onView: (row: AdminTransaction) => void; onNavigate: (page: 'refunds' | 'reversals', transactionId: number) => void }) {
  if (!rows.length) return <div className="admin-empty">没有符合筛选条件的资金流水。</div>;
  return <div className="admin-table-wrap"><table className="admin-table transaction-table"><thead><tr><th>发生时间</th><th>员工</th><th>交易类型</th><th>金额</th><th>余额变化</th><th>处理状态</th><th>操作</th></tr></thead><tbody>{rows.map(row => <tr key={row.id}>
    <td>{formatAdminDate(row.created_at)}</td><td><strong>{row.employee_name}</strong><small>{row.employee_no} · {row.department || '未填写部门'}</small></td><td>{transactionTypeLabel(row.type)}<small>{row.transaction_no}</small></td><td className={`money ${row.amount_cents < 0 ? 'amount-negative' : 'amount-positive'}`}>{signedYuan(row.amount_cents)}</td><td>{yuan(row.before_balance_cents)} → {yuan(row.after_balance_cents)}</td>
    <td>{row.type === 'CONSUME' ? row.can_refund ? '可退款' : row.refund_status === 'REFUNDED' ? '已退款' : '不可退款' : row.type === 'RECHARGE' ? row.can_reverse ? '可冲正' : row.reversal_status === 'REVERSED' ? '已冲正' : '不可冲正' : row.type === 'REFUND' && row.payout_status === 'AVAILABLE' ? '待线下退还' : row.type === 'REFUND' && row.payout_status === 'PAID' ? '已线下退还' : '—'}</td>
    <td><div className="admin-actions transaction-actions"><button type="button" onClick={() => onView(row)}>查看</button>{row.type === 'CONSUME' && <button type="button" disabled={!row.can_refund} title={row.can_refund ? '打开独立退款页' : row.refund_block_reason} onClick={() => onNavigate('refunds', row.id)}>{row.can_refund ? '退款' : row.refund_status === 'REFUNDED' ? '已退款' : '不可退款'}</button>}{row.type === 'RECHARGE' && <button type="button" disabled={!row.can_reverse} title={row.can_reverse ? '打开独立冲正页' : row.reversal_block_reason} onClick={() => onNavigate('reversals', row.id)}>{row.can_reverse ? '冲正' : row.reversal_status === 'REVERSED' ? '已冲正' : '不可冲正'}</button>}</div></td>
  </tr>)}</tbody></table></div>;
}

function TransactionDetail({ transaction, busy, error, onNavigate }: { transaction: AdminTransaction; busy: boolean; error: string; onNavigate: (page: 'refunds' | 'reversals', id: number) => void }) {
  const meal = transaction.meal_snapshot;
  const receipt = transaction.recharge_receipt;
  const entered = transaction.entered_by;
  return <section className="transaction-detail" aria-label="资金流水详情">
    <header><div><p className="admin-eyebrow">流水详情</p><h3>{transaction.transaction_no} · {transactionTypeLabel(transaction.type)}</h3></div>{busy && <span className="admin-muted">正在刷新详情…</span>}</header>
    {error && <ErrorBox>{error}</ErrorBox>}
    <div className="transaction-facts"><div><span>员工</span><strong>{transaction.employee_name}（{transaction.employee_no}）</strong><small>{transaction.department || '未填写部门'} · 手机 {transaction.employee_phone || '—'}</small></div><div><span>账户当前状态</span><strong>{accountStatusLabel(transaction.account_status || '')} · 当前余额 {transaction.current_balance_cents == null ? '—' : yuan(transaction.current_balance_cents)}</strong><small>员工状态：{accountStatusLabel(transaction.employee_status || '')}</small></div><div><span>发生时间</span><strong>{formatAdminDate(transaction.created_at)}</strong><small>录入人：{entered?.username || (transaction.administrator_id ? `管理员 #${transaction.administrator_id}` : '系统')}</small></div><div><span>金额与余额</span><strong>{signedYuan(transaction.amount_cents)}</strong><small>{yuan(transaction.before_balance_cents)} → {yuan(transaction.after_balance_cents)}</small></div></div>
    {transaction.reason && <div className="transaction-source"><strong>业务说明</strong><span>{transaction.reason}</span></div>}
    {meal && <div className="transaction-source"><strong>消费快照</strong><span>{meal.meal_name || mealCodeLabel(meal.meal_code || '')} · {mealCodeLabel(meal.meal_code || '')} · 营业日期 {meal.business_date} · {meal.amount_cents == null ? '—' : yuan(meal.amount_cents)}</span></div>}
    {receipt && <div className="transaction-source"><strong>原收款凭据</strong><span>{receipt.receipt_ref} · {receipt.payment_method} · {formatAdminDate(receipt.collected_at)} · {yuan(receipt.amount_cents)}</span></div>}
    {transaction.related_transaction && <div className="transaction-source"><strong>关联原流水</strong><span>{transaction.related_transaction.transaction_no} · {transactionTypeLabel(transaction.related_transaction.type)} · {yuan(transaction.related_transaction.amount_cents)}</span></div>}
    {transaction.type === 'CONSUME' && <div className="transaction-detail-actions">{transaction.can_refund ? <button className="admin-primary" type="button" onClick={() => onNavigate('refunds', transaction.id)}>前往消费退款</button> : <p className="admin-muted">{transaction.refund_status === 'REFUNDED' ? '此消费已退款。' : blockReasonLabel(transaction.refund_block_reason)}</p>}</div>}
    {transaction.type === 'RECHARGE' && <div className="transaction-detail-actions">{transaction.can_reverse ? <button className="admin-primary" type="button" onClick={() => onNavigate('reversals', transaction.id)}>前往充值冲正</button> : <p className="admin-muted">{transaction.reversal_status === 'REVERSED' ? '此充值已冲正。' : blockReasonLabel(transaction.reversal_block_reason)}</p>}</div>}
  </section>;
}

function TransactionLedger({ session, onNavigate }: { session: AdminSession; onNavigate: (page: 'refunds' | 'reversals', id: number) => void }) {
  const [draft, setDraft] = useState({ type: '', from: '', to: '' });
  const [employee, setEmployee] = useState<AdminEmployee | null>(null);
  const [applied, setApplied] = useState({ type: '', from: '', to: '', employee_id: '' });
  const [rows, setRows] = useState<AdminTransaction[]>([]); const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(false); const [error, setError] = useState('');
  const [selected, setSelected] = useState<AdminTransaction | null>(null); const [detailLoading, setDetailLoading] = useState(false); const [detailError, setDetailError] = useState('');
  const requestID = useRef(0); const detailRequestID = useRef(0);

  const load = useCallback(async (filters: typeof applied, append = false, pageCursor = '') => {
    const request = ++requestID.current; setLoading(true); setError('');
    if (!append) { setRows([]); setCursor(''); setSelected(null); }
    try {
      const page = await adminApi.transactions(session, { ...filters, limit: 50, ...(pageCursor ? { cursor: pageCursor } : {}) });
      if (request !== requestID.current) return;
      setRows(current => append ? [...current, ...page.items] : page.items);
      setCursor(page.next_cursor || '');
    } catch (reason) { if (request === requestID.current) setError(message(reason)); }
    finally { if (request === requestID.current) setLoading(false); }
  }, [session]);

  useEffect(() => { void load({ type: '', from: '', to: '', employee_id: '' }); }, [load]);

  async function search(event: FormEvent) {
    event.preventDefault(); setError('');
    if (draft.from && draft.to && draft.from > draft.to) { setError('结束日期不能早于开始日期。'); return; }
    const filters = { ...draft, employee_id: employee ? String(employee.id) : '' };
    setApplied(filters); await load(filters);
  }

  async function view(row: AdminTransaction) {
    const request = ++detailRequestID.current;
    setSelected(row); setDetailLoading(true); setDetailError('');
    try { const result = await adminApi.transaction(session, row.id); if (request === detailRequestID.current) setSelected(result.transaction); }
    catch (reason) { if (request === detailRequestID.current) setDetailError(message(reason)); }
    finally { if (request === detailRequestID.current) setDetailLoading(false); }
  }

  return <Panel title="资金流水" description="按员工、类型和日期查看完整分页流水；每条记录提供服务端核实的退款、冲正状态和原流水关联。" action={<ExportButton session={session} kind="transactions" filters={applied} label="导出当前筛选流水"/>}>
    <form className="admin-inline-form transaction-filters" onSubmit={search}>
      <EmployeeSelector session={session} value={employee} onChange={setEmployee} label="员工（可选）"/>
      <label>交易类型<select value={draft.type} onChange={event => setDraft({ ...draft, type: event.target.value })}><option value="">全部类型</option><option value="RECHARGE">线下充值</option><option value="RECHARGE_REVERSAL">充值冲正</option><option value="CONSUME">餐费消费</option><option value="REFUND">消费退款</option><option value="BALANCE_ADJUSTMENT">余额调整</option><option value="BALANCE_WITHDRAWAL">余额退还</option></select></label>
      <label>开始日期<input type="date" value={draft.from} onChange={event => setDraft({ ...draft, from: event.target.value })}/></label><label>结束日期<input type="date" value={draft.to} onChange={event => setDraft({ ...draft, to: event.target.value })}/>
      </label><button className="admin-secondary" disabled={loading}>筛选流水</button><button className="admin-link" type="button" onClick={() => { const empty = { type: '', from: '', to: '', employee_id: '' }; setDraft({ type: '', from: '', to: '' }); setEmployee(null); setApplied(empty); void load(empty); }}>清除筛选</button>
    </form>
    {error && <ErrorBox retry={() => void load(applied)}>{error}</ErrorBox>}
    {loading && rows.length === 0 ? <Loading/> : <TransactionTable rows={rows} onView={row => void view(row)} onNavigate={onNavigate}/>}
    {loading && rows.length > 0 && <p className="admin-muted" role="status">正在更新流水…</p>}
    {cursor && <button className="admin-secondary load-more" type="button" disabled={loading} onClick={() => void load(applied, true, cursor)}>加载更多流水</button>}
    {selected && <TransactionDetail transaction={selected} busy={detailLoading} error={detailError} onNavigate={onNavigate}/>}
  </Panel>;
}

function MoneyRecordFlow({ kind, session, initialTransactionId, onNotice, onNavigateToWithdrawal }: {
  kind: 'refund' | 'reversal'; session: AdminSession; initialTransactionId?: string; onNotice: (value: string) => void;
  onNavigateToWithdrawal?: (employeeId: number, refundId: number) => void;
}) {
  const expectedType = kind === 'refund' ? 'CONSUME' : 'RECHARGE';
  const title = kind === 'refund' ? '消费退款' : '充值冲正';
  const actionWord = kind === 'refund' ? '退款' : '冲正';
  const [employee, setEmployee] = useState<AdminEmployee | null>(null);
  const [draft, setDraft] = useState({ from: '', to: '' }); const [applied, setApplied] = useState({ from: '', to: '', employee_id: '' });
  const [rows, setRows] = useState<AdminTransaction[]>([]); const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(false); const [searchError, setSearchError] = useState('');
  const [selected, setSelected] = useState<AdminTransaction | null>(null); const [detailLoading, setDetailLoading] = useState(false); const [detailError, setDetailError] = useState('');
  const [reason, setReason] = useState(''); const [saving, setSaving] = useState(false); const [actionError, setActionError] = useState(''); const [result, setResult] = useState<AdminTransaction | null>(null);
  const listRequest = useRef(0); const detailRequest = useRef(0); const retryIntent = useRef<{ signature: string; key: string } | null>(null);

  const loadRows = useCallback(async (filters: typeof applied, append = false, pageCursor = '') => {
    const request = ++listRequest.current; setLoading(true); setSearchError('');
    if (!append) { setRows([]); setCursor(''); setSelected(null); setResult(null); }
    try {
      const page = await adminApi.transactions(session, { ...filters, type: expectedType, limit: 50, ...(pageCursor ? { cursor: pageCursor } : {}) });
      if (request !== listRequest.current) return;
      setRows(current => append ? [...current, ...page.items] : page.items);
      setCursor(page.next_cursor || '');
    } catch (error) { if (request === listRequest.current) setSearchError(message(error)); }
    finally { if (request === listRequest.current) setLoading(false); }
  }, [session, expectedType]);

  useEffect(() => { void loadRows({ from: '', to: '', employee_id: '' }); }, [loadRows]);

  async function search(event: FormEvent) {
    event.preventDefault(); setSearchError('');
    if (draft.from && draft.to && draft.from > draft.to) { setSearchError('结束日期不能早于开始日期。'); return; }
    const filters = { ...draft, employee_id: employee ? String(employee.id) : '' };
    setApplied(filters); await loadRows(filters);
  }

  async function selectRow(row: AdminTransaction) {
    const request = ++detailRequest.current; setSelected(row); setDetailLoading(true); setDetailError(''); setActionError(''); setResult(null);
    try {
      const response = await adminApi.transaction(session, row.id);
      if (request !== detailRequest.current) return;
      if (response.transaction.type !== expectedType) { setDetailError(`所选流水不是${kind === 'refund' ? '消费' : '充值'}记录。`); return; }
      setSelected(response.transaction);
    } catch (error) { if (request === detailRequest.current) setDetailError(message(error)); }
    finally { if (request === detailRequest.current) setDetailLoading(false); }
  }

  useEffect(() => {
    if (!initialTransactionId) return;
    const request = ++detailRequest.current; setDetailLoading(true); setDetailError('');
    void adminApi.transaction(session, initialTransactionId).then(response => {
      if (request !== detailRequest.current) return;
      if (response.transaction.type !== expectedType) { setDetailError(`流水 #${initialTransactionId} 不是可用于${actionWord}的${kind === 'refund' ? '消费' : '充值'}记录。`); return; }
      setSelected(response.transaction);
    }).catch(error => { if (request === detailRequest.current) setDetailError(message(error)); })
      .finally(() => { if (request === detailRequest.current) setDetailLoading(false); });
  }, [session, initialTransactionId, expectedType, actionWord, kind]);

  const amount = selected ? Math.abs(selected.amount_cents) : 0;
  const projected = selected?.current_balance_cents == null ? null : kind === 'refund' ? selected.current_balance_cents + amount : selected.current_balance_cents - amount;
  const accountClosed = selected?.account_status === 'CLOSED';
  const notAllowed = !selected || (kind === 'refund' ? !selected.can_refund : !selected.can_reverse || accountClosed || (selected.current_balance_cents != null && selected.current_balance_cents < amount));

  async function submit(event: FormEvent) {
    event.preventDefault(); setActionError('');
    if (!selected || !reason.trim() || notAllowed) { setActionError('请核对原流水、账户状态和必填原因。'); return; }
    const signature = `${kind}|${selected.id}|${reason.trim()}`;
    if (retryIntent.current?.signature !== signature) retryIntent.current = { signature, key: idempotencyKey() };
    setSaving(true);
    try {
      let actual: AdminTransaction;
      if (kind === 'refund') {
        const response = await adminApi.refundTransaction(session, selected.id, reason.trim(), retryIntent.current.key);
        actual = response.transaction;
      } else {
        const response = await adminApi.reverseRecharge(session, selected.id, reason.trim(), retryIntent.current.key) as { transaction?: AdminTransaction };
        if (!response.transaction) throw new Error('服务端未返回冲正流水');
        actual = response.transaction;
      }
      retryIntent.current = null; setReason('');
      onNotice(kind === 'refund' ? '消费退款已退回储值账户。' : '充值已按原金额冲正，原充值凭据仍保留。');
      await loadRows(applied);
      setResult(actual);
      if (kind === 'refund' && accountClosed) setActionError('员工账户已关闭。退款已回到储值账户，请继续登记实际线下付款。');
      setSelected({ ...selected, ...(kind === 'refund' ? { can_refund: false, refund_status: 'REFUNDED' } : { can_reverse: false, reversal_status: 'REVERSED' }), current_balance_cents: actual.after_balance_cents });
    } catch (error) { setActionError(message(error)); }
    finally { setSaving(false); }
  }

  const titleDescription = kind === 'refund'
    ? '查找原消费并核对身份、金额和账户余额；只按原金额全额退回，不能手输流水编号。'
    : '查找错误充值并核对收款凭据和账户余额；按原金额等额冲正，不能手输流水编号。';

  return <Panel title={title} description={titleDescription}>
    <form className="admin-inline-form record-search" onSubmit={search}>
      <EmployeeSelector session={session} value={employee} onChange={setEmployee} label="员工（可选）"/>
      <label>开始日期<input type="date" value={draft.from} onChange={event => setDraft({ ...draft, from: event.target.value })}/></label><label>结束日期<input type="date" value={draft.to} onChange={event => setDraft({ ...draft, to: event.target.value })}/></label>
      <button className="admin-secondary" disabled={loading}>查找{kind === 'refund' ? '消费' : '充值'}</button><button className="admin-link" type="button" onClick={() => { const filters = { from: '', to: '', employee_id: '' }; setEmployee(null); setDraft({ from: '', to: '' }); setApplied(filters); void loadRows(filters); }}>清除条件</button>
    </form>
    {searchError && <ErrorBox retry={() => void loadRows(applied)}>{searchError}</ErrorBox>}
    {detailError && !selected && <ErrorBox>{detailError}</ErrorBox>}
    <div className="record-candidates"><h3>{kind === 'refund' ? '找到的原消费' : '找到的原充值'}</h3>{loading && !rows.length ? <Loading/> : rows.length ? <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>选择</th><th>员工</th><th>流水</th><th>原金额</th><th>时间</th><th>状态</th><th>操作</th></tr></thead><tbody>{rows.map(row => <tr key={row.id} className={selected?.id === row.id ? 'selected-row' : ''}>
      <td><input type="radio" name={`${kind}-transaction`} aria-label={`选择流水 ${row.transaction_no}`} checked={selected?.id === row.id} onChange={() => void selectRow(row)}/></td><td>{row.employee_name}<small>{row.employee_no} · {row.department || '未填写部门'}</small></td><td>{row.transaction_no}<small>稳定流水编号 #{row.id}</small></td><td>{yuan(Math.abs(row.amount_cents))}</td><td>{formatAdminDate(row.created_at)}</td><td>{kind === 'refund' ? row.refund_status === 'REFUNDED' ? '已退款' : '可退款' : row.reversal_status === 'REVERSED' ? '已冲正' : '可冲正'}</td><td><button className="admin-link" type="button" onClick={() => void selectRow(row)}>核对这笔</button></td>
    </tr>)}</tbody></table></div> : !loading ? <div className="admin-empty">没有找到符合条件的{kind === 'refund' ? '消费' : '充值'}记录。</div> : null}
      {cursor && <button className="admin-secondary load-more" type="button" disabled={loading} onClick={() => void loadRows(applied, true, cursor)}>加载更多</button>}
    </div>
    {selected && <section className="record-confirmation">
      {detailError && <ErrorBox>{detailError}</ErrorBox>}
      <header><div><p className="admin-eyebrow">核对原始凭据</p><h3>{selected.employee_name}（{selected.employee_no}）· {transactionTypeLabel(selected.type)}</h3></div>{detailLoading && <span className="admin-muted">正在读取最新账户状态…</span>}</header>
      <div className="record-summary-grid"><div><span>员工与部门</span><strong>{selected.employee_name} · {selected.employee_no}</strong><small>{selected.department || '未填写部门'} · {accountStatusLabel(selected.account_status || '')}账户</small></div><div><span>原记录</span><strong>{selected.transaction_no} · {formatAdminDate(selected.created_at)}</strong><small>{selected.meal_snapshot ? `${mealCodeLabel(selected.meal_snapshot.meal_code || '')} · 营业日 ${selected.meal_snapshot.business_date}` : '交易详情已从服务端读取'}</small></div><div><span>原金额</span><strong>{yuan(amount)}</strong><small>{selected.before_balance_cents != null ? `原流水余额 ${yuan(selected.before_balance_cents)} → ${yuan(selected.after_balance_cents)}` : ''}</small></div><div><span>当前余额</span><strong>{selected.current_balance_cents == null ? '—' : yuan(selected.current_balance_cents)}</strong><small>{projected == null ? '读取详情后显示变化' : `${actionWord}后预计 ${yuan(projected)}`}</small></div></div>
      {kind === 'reversal' && <div className="transaction-source"><strong>线下收款凭据</strong><span>{selected.recharge_receipt?.receipt_ref || '未找到凭据'} · {selected.recharge_receipt ? `${selected.recharge_receipt.payment_method} · ${formatAdminDate(selected.recharge_receipt.collected_at)}` : '请先核查原始收款记录'} · 录入人 {selected.entered_by?.username || `管理员 #${selected.administrator_id}`}</span></div>}
      {kind === 'refund' && accountClosed && <div className="admin-note"><h3>该账户已关闭</h3><p>本次退款会回到已关闭账户的余额。完成后还需在“余额退还与关户”登记实际线下付款，避免将账户退款误认为已经付给员工现金。</p></div>}
      {kind === 'reversal' && selected.account_status === 'FROZEN' && <div className="admin-note"><p>账户当前冻结。按现有规则可冲正，但冲正金额不会超过当前余额。</p></div>}
      {kind === 'reversal' && selected.account_status === 'CLOSED' && <div className="admin-alert error" role="alert">账户已关闭，不能冲正充值。</div>}
      <form className="admin-form record-action-form" onSubmit={submit}><label>{kind === 'refund' ? '退款原因' : '冲正原因'}<textarea required maxLength={512} rows={3} value={reason} onChange={event => setReason(event.target.value)} placeholder={kind === 'refund' ? '说明本次全额退回的原因' : '说明本次等额冲正的原因'}/><small>{reason.length}/512</small></label>
        <div className="confirm-summary"><h3>确认{actionWord}</h3><p>{selected.employee_name}（{selected.employee_no}）· {transactionTypeLabel(selected.type)} · {actionWord}原金额 {yuan(amount)} · 当前余额 {selected.current_balance_cents == null ? '—' : yuan(selected.current_balance_cents)} · 预计余额 {projected == null ? '—' : yuan(projected)}</p></div>
        {actionError && <div className="admin-alert error" role="alert">{actionError}{kind === 'refund' && accountClosed && result && onNavigateToWithdrawal && <button className="admin-link" type="button" onClick={() => onNavigateToWithdrawal(selected.employee_id, result.id)}>前往登记线下退还</button>}</div>}
        {result && <div className="admin-alert success" role="status">服务端已完成，流水 {result.transaction_no || `#${result.id}`} · 余额 {yuan(result.before_balance_cents)} → {yuan(result.after_balance_cents)}。</div>}
        <button className="admin-primary" disabled={saving || detailLoading || notAllowed || !reason.trim()}>{saving ? `正在${actionWord}…` : notAllowed ? kind === 'refund' && selected.refund_status === 'REFUNDED' ? '该消费已退款' : kind === 'reversal' && selected.reversal_status === 'REVERSED' ? '该充值已冲正' : kind === 'reversal' && accountClosed ? '账户已关闭，不能冲正' : kind === 'reversal' && projected != null && projected < 0 ? '余额不足，不能冲正' : '此流水不可操作' : `确认${kind === 'refund' ? '退回储值账户' : '扣减余额'} ${yuan(amount)}`}</button>
      </form>
    </section>}
  </Panel>;
}

function mealMinutes(value: string): number | null {
  if (value === '24:00') return 1440;
  if (!/^(?:[01]\d|2[0-3]):[0-5]\d$/.test(value)) return null;
  const [hours, minutes] = value.split(':').map(Number);
  return hours * 60 + minutes;
}

function MealConfiguration({ meals, savedMeals, busy, onChange, onDiscard, onSave }: {
  meals: MealDraft[]; savedMeals: AdminMeal[]; busy: boolean; loading: boolean; error: string;
  onChange: (next: MealDraft[]) => void; onDiscard: () => void; onSave: (next: MealDraft[]) => Promise<void>;
}) {
  const [saving, setSaving] = useState(false); const [error, setError] = useState('');
  const original = savedMeals.map(mealToDraft);
  const changedCodes = meals.filter((meal, index) => JSON.stringify(meal) !== JSON.stringify(original[index])).map(meal => meal.code);
  const dirty = changedCodes.length > 0;
  const validated = meals.map(meal => {
    const start = mealMinutes(meal.start_time); const end = mealMinutes(meal.end_time);
    const price = nonnegativeMoneyInputCents(meal.price_text);
    const validPrice = price != null;
    return { meal, start, end, price, valid: Boolean(meal.name.trim() && meal.name.length <= 64 && start != null && end != null && end > start && validPrice && (meal.enabled ? (price ?? 0) > 0 : (price ?? (meal.price_text === '0' ? 0 : -1)) >= 0)) };
  });
  const malformed = validated.some(item => !item.valid);
  const times = validated.filter(item => item.start != null && item.end != null);
  const overlaps = times.some((left, index) => times.slice(index + 1).some(right => left.start! < right.end! && left.end! > right.start!));
  const canSave = dirty && !malformed && !overlaps && !busy && !saving;

  useEffect(() => {
    if (!dirty) return;
    const preventUnload = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', preventUnload);
    return () => window.removeEventListener('beforeunload', preventUnload);
  }, [dirty]);

  function update(code: string, patch: Partial<MealDraft>) {
    setError(''); onChange(meals.map(meal => meal.code === code ? { ...meal, ...patch } : meal));
  }
  async function save(event: FormEvent) {
    event.preventDefault(); setError('');
    if (!canSave) return;
    const payload = meals.map(meal => {
      const parsed = nonnegativeMoneyInputCents(meal.price_text);
      return { ...meal, price_cents: parsed ?? meal.price_cents };
    });
    // Suspending every period is legitimate — a holiday, a closed kitchen — but
    // taking the canteen from serving to not serving must not happen as a side
    // effect of editing times and prices. Re-saving an already suspended set
    // only re-states what the banner under the grid already says.
    const serving = savedMeals.some(meal => meal.enabled);
    if (serving && payload.every(meal => !meal.enabled)
      && !window.confirm('三个餐次都设为“暂停供应”，保存后员工无法消费。确定保存吗？')) return;
    setSaving(true);
    try { await onSave(payload); }
    catch (reason) {
      if (reason instanceof AdminApiError && reason.status === 409) setError('餐次时间与其他餐次重叠。请调整后再一次保存全部配置。');
      else if (reason instanceof AdminApiError && reason.status === 400) setError('餐次名称、时段或价格格式不正确，请检查所有面板。');
      else setError(message(reason));
    } finally { setSaving(false); }
  }

  return <form className="meal-configuration" onSubmit={save}>
    <div className="meal-configuration-grid">{meals.map(meal => {
      const baseline = original.find(item => item.code === meal.code);
      const changed = !baseline || JSON.stringify(baseline) !== JSON.stringify(meal);
      const mealLabel = meal.code === 'BREAKFAST' ? '早餐' : meal.code === 'LUNCH' ? '午餐' : '晚餐';
      return <section className={`meal-config-card ${meal.code.toLowerCase()} ${changed ? 'changed' : ''}`} key={meal.code} aria-label={`${mealLabel}配置`}>
        <header className="meal-config-head"><div><span>{mealLabel}</span><small>{changed ? '有未保存修改' : '已保存'}</small></div><label className="meal-toggle"><input type="checkbox" checked={meal.enabled} onChange={event => update(meal.code, { enabled: event.target.checked })}/><span>{meal.enabled ? '开放供应' : '暂停供应'}</span></label></header>
        <div className="meal-config-fields">
          <label>餐次名称<input required maxLength={64} value={meal.name} onChange={event => update(meal.code, { name: event.target.value })}/></label>
          <label>开始时间<input required type="text" inputMode="numeric" pattern="(?:[01][0-9]|2[0-3]):[0-5][0-9]" placeholder="07:30" value={meal.start_time} onChange={event => update(meal.code, { start_time: event.target.value })}/></label>
          <label>结束时间<input required type="text" inputMode="numeric" pattern="(?:(?:[01][0-9]|2[0-3]):[0-5][0-9]|24:00)" placeholder="09:00 或 24:00" value={meal.end_time} onChange={event => update(meal.code, { end_time: event.target.value })}/><small>全天结束可填 24:00</small></label>
          <label>消费价格（元）<input required type="number" inputMode="decimal" min="0" step="0.01" value={meal.price_text} onChange={event => {
            const priceText = event.target.value;
            const parsed = nonnegativeMoneyInputCents(priceText);
            update(meal.code, { price_text: priceText, ...(parsed == null ? {} : { price_cents: parsed }) });
          }}/></label>
        </div>
        <p className="meal-price-note">{meal.enabled ? '开放时需设置大于 0 的餐费。' : '暂停供应仍需保持有效时段，避免与其他餐次重叠。'}</p>
      </section>;
    })}</div>
    {savedMeals.length > 0 && savedMeals.every(meal => !meal.enabled) && <div className="admin-alert warn" role="status">三个餐次全部“暂停供应”，员工当前无法消费。开放任一餐次并保存后即可恢复。</div>}
    {(malformed || overlaps) && <div className="admin-alert error" role="alert">{overlaps ? '餐次时间存在重叠，请修正后保存。' : '请检查餐次名称、时段和价格；开放餐次的价格必须大于 0。'}</div>}
    {error && <div className="admin-alert error" role="alert">{error}</div>}
    <div className="meal-savebar" aria-live="polite"><div><strong>{dirty ? `${changedCodes.length} 个餐次有未保存修改` : '全部餐次配置已保存'}</strong><small>{dirty ? '保存会一次提交早餐、午餐和晚餐的完整配置。' : '修改任一面板后，在此统一保存。'}</small></div><div className="admin-inline-actions">{dirty && <button className="admin-secondary" type="button" disabled={saving || busy} onClick={() => { if (window.confirm('确定放弃所有餐次修改？')) { onDiscard(); setError(''); } }}>放弃修改</button>}<button className="admin-primary" disabled={!canSave} type="submit">{saving ? '正在保存全部餐次…' : dirty ? '保存全部餐次' : '全部已保存'}</button></div></div>
  </form>;
}

function RemoteTable({ session, path }: { session: AdminSession; path: string }) {
  const [state, setState] = useState<LoadState>({ ...emptyLoad, busy: true });
  const refresh = useCallback(async () => { setState({ ...emptyLoad, busy: true }); try { const data = await adminApi.generic<unknown>(session, path); setState({ busy: false, data, error: '' }); } catch (error) { setState({ busy: false, data: null, error: message(error) }); } }, [session, path]);
  useEffect(() => { void refresh(); }, [refresh]);
  return state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void refresh()}>{state.error}</ErrorBox> : <Table rows={rowsFrom(state.data)}/>;
}

type ReceiptReviewItem = {
  transaction_id: number; transaction_no: string; type: string; amount_cents: number; entered_by: number; status: string;
  reviewer_id: number; note: string; reviewed_at: string; employee_id: number; employee_no: string; employee_name: string;
  department: string; receipt_id: number; receipt_ref: string; collected_at: string; payment_method: string;
  recharge_transaction_id: number; related_transaction_id: number;
};

function ReceiptReviews({ session, onNotice }: { session: AdminSession; onNotice: (value: string) => void }) {
  const [draft, setDraft] = useState({ status: 'PENDING', from: '', to: '' }); const [employee, setEmployee] = useState<AdminEmployee | null>(null);
  const [applied, setApplied] = useState({ status: 'PENDING', from: '', to: '', employee_id: '' });
  const [items, setItems] = useState<ReceiptReviewItem[]>([]); const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(false); const [error, setError] = useState(''); const [saving, setSaving] = useState(false);
  const [selected, setSelected] = useState<ReceiptReviewItem | null>(null); const [decision, setDecision] = useState('MATCHED'); const [note, setNote] = useState('');
  const requestID = useRef(0);

  const load = useCallback(async (filters: typeof applied, append = false, pageCursor = '') => {
    const request = ++requestID.current; setLoading(true); setError('');
    if (!append) { setItems([]); setCursor(''); setSelected(null); setNote(''); }
    const query = new URLSearchParams({ status: filters.status, limit: '50' });
    for (const [key, value] of Object.entries(filters)) if (key !== 'status' && value) query.set(key, value);
    if (pageCursor) query.set('cursor', pageCursor);
    try {
      const result = await adminApi.generic<{ items: ReceiptReviewItem[]; next_cursor?: string }>(session, `/api/admin/receipt-reviews?${query.toString()}`);
      if (request !== requestID.current) return;
      setItems(current => append ? [...current, ...result.items] : result.items); setCursor(result.next_cursor || '');
    } catch (reason) { if (request === requestID.current) setError(message(reason)); }
    finally { if (request === requestID.current) setLoading(false); }
  }, [session]);

  useEffect(() => { void load({ status: 'PENDING', from: '', to: '', employee_id: '' }); }, [load]);

  async function search(event: FormEvent) {
    event.preventDefault();
    if (draft.from && draft.to && draft.from > draft.to) { setError('结束日期不能早于开始日期。'); return; }
    const filters = { ...draft, employee_id: employee ? String(employee.id) : '' };
    setApplied(filters); await load(filters);
  }

  function choose(item: ReceiptReviewItem) {
    setSelected(item); setNote(item.note || ''); setError('');
    setDecision(item.status === 'DIFFERENCE' ? 'RESOLVED' : 'MATCHED');
  }

  async function submit(event: FormEvent) {
    event.preventDefault(); setError('');
    if (!selected) return;
    if (selected.entered_by === session.administrator.id) { setError('录入人不能复核自己的收款记录，请由另一位管理员处理。'); return; }
    if ((decision === 'DIFFERENCE' || decision === 'RESOLVED') && !note.trim()) { setError('存在差异或差异已处理时，请填写复核说明。'); return; }
    setSaving(true);
    try {
      const result = await adminApi.generic<{ transaction_id: number; status: string; reviewer_id: number; note: string }>(session, `/api/admin/receipt-reviews/${selected.transaction_id}`, { method: 'POST', body: JSON.stringify({ status: decision, note: note.trim() }) });
      setSelected({ ...selected, status: result.status, reviewer_id: result.reviewer_id, note: result.note });
      onNotice('复核结论已保存；账户余额未改变。');
      await load(applied);
    } catch (reason) { setError(message(reason)); }
    finally { setSaving(false); }
  }

  const isAuthor = selected?.entered_by === session.administrator.id;
  const canReview = Boolean(selected && !isAuthor && (selected.status === 'PENDING' || selected.status === 'DIFFERENCE'));
  return <Panel title="线下收款复核" description="按待办状态、员工或日期筛选充值和冲正记录，选中记录后直接核对收款凭据。录入管理员不能复核自己的记录。">
    <form className="admin-inline-form receipt-filters" onSubmit={search}>
      <label>复核状态<select value={draft.status} onChange={event => setDraft({ ...draft, status: event.target.value })}><option value="PENDING">待复核</option><option value="MATCHED">凭据匹配</option><option value="DIFFERENCE">存在差异</option><option value="RESOLVED">差异已处理</option><option value="ALL">全部记录</option></select></label>
      <EmployeeSelector session={session} value={employee} onChange={setEmployee} label="员工（可选）"/>
      <label>开始日期<input type="date" value={draft.from} onChange={event => setDraft({ ...draft, from: event.target.value })}/></label><label>结束日期<input type="date" value={draft.to} onChange={event => setDraft({ ...draft, to: event.target.value })}/></label>
      <button className="admin-secondary" disabled={loading}>查找收款记录</button>
    </form>
    {error && !selected && <ErrorBox retry={() => void load(applied)}>{error}</ErrorBox>}
    {loading && !items.length ? <Loading/> : items.length ? <div className="admin-table-wrap"><table className="admin-table receipt-review-table"><thead><tr><th>员工</th><th>交易</th><th>凭据</th><th>录入人</th><th>复核状态</th><th>操作</th></tr></thead><tbody>{items.map(item => <tr className={selected?.transaction_id === item.transaction_id ? 'selected-row' : ''} key={item.transaction_id}>
      <td><strong>{item.employee_name}</strong><small>{item.employee_no} · {item.department || '未填写部门'}</small></td><td>{transactionTypeLabel(item.type)}<small>{item.transaction_no}</small><small>{signedYuan(item.amount_cents)}</small></td><td>{item.receipt_ref || '未找到收款凭据'}<small>{paymentMethodLabel(item.payment_method)} · {item.collected_at ? formatAdminDate(item.collected_at) : '—'}</small></td><td>管理员 #{item.entered_by}{item.entered_by === session.administrator.id ? <small>由你录入</small> : null}</td><td>{reviewStatusLabel(item.status)}{item.note && <small>{item.note}</small>}</td><td><button className="admin-link" type="button" onClick={() => choose(item)}>打开复核</button></td>
    </tr>)}</tbody></table></div> : !loading ? <div className="admin-empty">当前筛选下没有收款记录。</div> : null}
    {cursor && <button className="admin-secondary load-more" type="button" disabled={loading} onClick={() => void load(applied, true, cursor)}>加载更多收款记录</button>}
    {selected && <section className="receipt-review-detail"><header><div><p className="admin-eyebrow">收款复核记录</p><h3>{selected.employee_name}（{selected.employee_no}）· {transactionTypeLabel(selected.type)}</h3></div><span className={`admin-status ${selected.status === 'PENDING' ? 'warn' : 'ok'}`}>{reviewStatusLabel(selected.status)}</span></header>
      <div className="record-summary-grid"><div><span>交易金额</span><strong>{signedYuan(selected.amount_cents)}</strong><small>{selected.transaction_no}</small></div><div><span>收款凭据</span><strong>{selected.receipt_ref || '无凭据编号'}</strong><small>{paymentMethodLabel(selected.payment_method)} · {selected.collected_at ? formatAdminDate(selected.collected_at) : '收款时间缺失'}</small></div><div><span>凭据关联充值</span><strong>{selected.recharge_transaction_id ? `#${selected.recharge_transaction_id}` : '未关联'}</strong><small>{selected.type === 'RECHARGE_REVERSAL' ? `冲正原流水 #${selected.related_transaction_id}` : '充值原始流水'}</small></div><div><span>录入人与复核人</span><strong>管理员 #{selected.entered_by}</strong><small>{selected.reviewer_id ? `复核管理员 #${selected.reviewer_id}` : '尚未复核'}</small></div></div>
      {isAuthor && <div className="admin-alert error" role="alert">这笔收款由你录入。系统要求另一位管理员完成复核。</div>}
      {canReview && <form className="admin-form receipt-decision" onSubmit={submit}>
        <label>复核结论<select value={decision} onChange={event => setDecision(event.target.value)}>{selected.status === 'PENDING' ? <><option value="MATCHED">凭据匹配</option><option value="DIFFERENCE">存在差异</option></> : <option value="RESOLVED">差异已处理</option>}</select></label>
        {(decision === 'DIFFERENCE' || decision === 'RESOLVED') && <label>处理说明<textarea required maxLength={512} rows={3} value={note} onChange={event => setNote(event.target.value)} placeholder={decision === 'DIFFERENCE' ? '描述凭据与实际收款的差异' : '描述差异如何处理'}/></label>}
        {error && <div className="admin-alert error" role="alert">{error}</div>}
        <button className="admin-primary" disabled={saving}>{saving ? '正在保存复核…' : decision === 'MATCHED' ? '确认凭据匹配' : decision === 'DIFFERENCE' ? '记录存在差异' : '确认差异已处理'}</button>
      </form>}
      {selected.status === 'MATCHED' && <div className="admin-note"><p>凭据已匹配。复核结果仅记录结论，不修改余额。</p></div>}
      {selected.status === 'RESOLVED' && <div className="admin-note"><p>差异已处理并留存复核说明。</p></div>}
    </section>}
  </Panel>;
}

function reviewStatusLabel(status: string): string {
  return status === 'PENDING' ? '待复核' : status === 'MATCHED' ? '凭据匹配' : status === 'DIFFERENCE' ? '存在差异' : status === 'RESOLVED' ? '差异已处理' : status;
}
function paymentMethodLabel(method: string): string {
  return method === 'CASH' ? '现金' : method === 'BANK_TRANSFER' ? '银行转账' : method === 'WECHAT' ? '微信' : method === 'ALIPAY' ? '支付宝' : method === 'OTHER' ? '其他线下方式' : method || '—';
}

type ManualSupplyItem = {
  id: number; receipt_ref: string; employee_id: number; employee_name: string; employee_no: string; department: string;
  employee_status: string; account_status: string; meal_code: string; meal_name: string; business_date: string;
  amount_cents: number; status: string; note: string; exception_reason: string; created_by: number; resolved_by: number;
  transaction_id: number; created_at: string; resolved_at: string; transaction_created_at: string;
};

function ManualSupply({ session }: { session: AdminSession }) {
  const [form, setForm] = useState({ meal_code: '', amount: '', receipt_ref: '', business_date: businessToday(), note: '' });
  const [employee, setEmployee] = useState<AdminEmployee | null>(null); const [meals, setMeals] = useState<AdminMeal[]>([]);
  const [items, setItems] = useState<ManualSupplyItem[]>([]); const [cursor, setCursor] = useState('');
  const [filter, setFilter] = useState('PENDING,EXCEPTION'); const [loading, setLoading] = useState(true); const [listError, setListError] = useState(''); const [formError, setFormError] = useState(''); const [saving, setSaving] = useState(false);
  const [selected, setSelected] = useState<ManualSupplyItem | null>(null); const [posting, setPosting] = useState(false);
  const [dialog, setDialog] = useState<'register' | 'confirm' | 'result' | null>(null);
  const [result, setResult] = useState<{ kind: 'success' | 'error'; title: string; message: string } | null>(null);
  const postKey = useRef<{ id: number; key: string } | null>(null); const requestID = useRef(0);

  useEffect(() => { void adminApi.mealPeriods(session).then(response => setMeals(response.meal_periods || [])).catch(() => setMeals([])); }, [session]);

  const refresh = useCallback(async (status = filter, append = false, pageCursor = '') => {
    const request = ++requestID.current; setLoading(true); setListError('');
    const query = new URLSearchParams({ status, limit: '50' }); if (pageCursor) query.set('cursor', pageCursor);
    try {
      const response = await adminApi.generic<{ items: ManualSupplyItem[]; next_cursor?: string }>(session, `/api/admin/manual-supplies?${query.toString()}`);
      if (request !== requestID.current) return [];
      setItems(current => append ? [...current, ...response.items] : response.items); setCursor(response.next_cursor || '');
      return response.items;
    } catch (reason) { if (request === requestID.current) setListError(message(reason)); return []; }
    finally { if (request === requestID.current) setLoading(false); }
  }, [session, filter]);

  useEffect(() => { void refresh(filter); }, [refresh, filter]);

  const configuredMeal = meals.find(meal => meal.code === form.meal_code);
  const amountCents = moneyInputCents(form.amount);
  function closeResult() {
    const failed = result?.kind === 'error';
    setDialog(failed ? selected ? 'confirm' : 'register' : null);
    setResult(null);
  }
  async function submit(event: FormEvent) {
    event.preventDefault(); setFormError('');
    if (!employee || !form.meal_code || amountCents == null || !form.receipt_ref.trim() || !form.business_date) { setFormError('请选员工和餐次，并填写实际供餐金额、凭据与日期。'); return; }
    setSaving(true);
    try {
      await adminApi.generic(session, '/api/admin/manual-supplies', { method: 'POST', body: JSON.stringify({ employee_id: employee.id, meal_code: form.meal_code, amount_cents: amountCents, receipt_ref: form.receipt_ref.trim(), business_date: form.business_date, note: form.note.trim() }) });
      setForm({ meal_code: '', amount: '', receipt_ref: '', business_date: businessToday(), note: '' }); setEmployee(null); setFormError('');
      setResult({ kind: 'success', title: '供餐登记成功', message: '记录已加入列表，状态为“待核对”。此时尚未扣款，请在列表中核实后再补录。' });
      setDialog('result');
      await refresh(filter);
    } catch (reason) {
      setResult({ kind: 'error', title: '登记失败', message: message(reason) }); setDialog('result');
    } finally { setSaving(false); }
  }

  async function postSelected() {
    if (!selected) return;
    if (postKey.current?.id !== selected.id) postKey.current = { id: selected.id, key: idempotencyKey() };
    setPosting(true);
    try {
      const response = await adminApi.generic<Record<string, unknown>>(session, `/api/admin/manual-supplies/${selected.id}/post`, { method: 'POST', body: JSON.stringify({ idempotency_key: postKey.current.key }) });
      postKey.current = null;
      const transaction = response.transaction as Record<string, unknown> | undefined;
      const transactionID = transaction?.id || response.transaction_id || '已补录';
      const postedAt = transaction?.created_at || response.transaction_created_at;
      setSelected(null);
      setResult({ kind: 'success', title: '补录成功', message: `记录已入账，消费流水 #${String(transactionID)}。${postedAt ? `日结按实际入账时间 ${formatAdminDate(String(postedAt))} 归属。` : ''}关闭提示后，可在列表确认状态和流水号。` });
      setDialog('result');
      await refresh(filter);
    } catch (reason) {
      const refreshed = await refresh(filter);
      const latest = refreshed.find(item => item.id === selected.id);
      if (latest) setSelected(latest);
      setResult({ kind: 'error', title: '补录失败', message: message(reason) }); setDialog('result');
    } finally { setPosting(false); }
  }

  function closeDialog() {
    if (saving || posting) return;
    if (dialog === 'result') { closeResult(); return; }
    setDialog(null);
    if (dialog === 'confirm') setSelected(null);
  }

  return <div className="manual-supply-flow">
    <section className="manual-records manual-records-primary">
      <header className="manual-list-toolbar">
        <div><h3>供餐记录</h3><p className="admin-muted">登记供餐事实后，在列表中核实并补录；补录成功的记录会显示消费流水。</p></div>
        <div className="manual-list-actions">
          <label>记录状态<select value={filter} onChange={event => { setCursor(''); setFilter(event.target.value); setSelected(null); }}><option value="PENDING,EXCEPTION">待处理和异常</option><option value="PENDING">待核对</option><option value="EXCEPTION">异常待处理</option><option value="POSTED">已补录</option><option value="ALL">全部记录</option></select></label>
          <button className="admin-secondary" type="button" disabled={loading} onClick={() => void refresh(filter)}>刷新列表</button>
          <button className="admin-primary" type="button" onClick={() => { setFormError(''); setDialog('register'); }}>登记供餐</button>
        </div>
      </header>
      {listError && <ErrorBox retry={() => void refresh(filter)}>{listError}</ErrorBox>}
      {loading && !items.length ? <Loading/> : items.length ? <div className="admin-table-wrap"><table className="admin-table manual-supply-table"><thead><tr><th>员工</th><th>供餐事实</th><th>实际入账</th><th>状态与原因</th><th>操作</th></tr></thead><tbody>{items.map(item => <tr key={item.id}>
        <td><strong>{item.employee_name}</strong><small>{item.employee_no} · {item.department || '未填写部门'}</small><small>{accountStatusLabel(item.account_status)}</small></td><td>{item.meal_name}<small>{item.business_date} · {yuan(item.amount_cents)} · {item.receipt_ref}</small></td><td>{item.transaction_id ? <>{formatAdminDate(item.transaction_created_at)}<small>消费流水 #{item.transaction_id}</small></> : '尚未入账'}</td><td><span className={`admin-status ${item.status === 'POSTED' ? 'ok' : item.status === 'EXCEPTION' ? 'warn' : ''}`}>{manualStatusLabel(item.status)}</span>{item.exception_reason && <small className="inline-error">{manualExceptionLabel(item.exception_reason)}</small>}</td><td>{item.status === 'PENDING' || item.status === 'EXCEPTION' ? <button className="admin-link" type="button" onClick={() => { setSelected(item); setResult(null); setDialog('confirm'); }}>核对并补录</button> : <span className="admin-muted">已完成</span>}</td>
      </tr>)}</tbody></table></div> : !loading ? <div className="admin-empty">当前筛选下没有供餐记录，可点击“登记供餐”新建。</div> : null}
      {cursor && <button className="admin-secondary load-more" type="button" disabled={loading} onClick={() => void refresh(filter, true, cursor)}>加载更多记录</button>}
    </section>

    {dialog === 'register' && <Modal title="登记实际供餐" className="manual-supply-modal" onClose={closeDialog}>
      <p className="admin-muted manual-dialog-intro">登记已经发生的供餐事实。登记不会扣款，核对记录并确认补录后才会记入员工账户。</p>
      <form className="admin-form manual-dialog-form" onSubmit={submit}>
        <EmployeeSelector session={session} value={employee} onChange={setEmployee} label="员工"/>
        <div className="manual-input-grid">
          <label>餐次<select required value={form.meal_code} onChange={event => setForm({ ...form, meal_code: event.target.value })}><option value="">选择餐次</option><option value="BREAKFAST">早餐</option><option value="LUNCH">午餐</option><option value="DINNER">晚餐</option></select>{configuredMeal && <small>当前配置参考价格：{yuan(configuredMeal.price_cents)}；按实际凭据填写。</small>}</label>
          <label>实际供餐金额（元）<input required inputMode="decimal" type="number" min="0.01" step="0.01" value={form.amount} onChange={event => setForm({ ...form, amount: event.target.value })}/></label>
          <label>供餐日期<input required type="date" value={form.business_date} onChange={event => setForm({ ...form, business_date: event.target.value })}/></label>
          <label>唯一凭据<input required maxLength={128} value={form.receipt_ref} onChange={event => setForm({ ...form, receipt_ref: event.target.value })} placeholder="纸质供餐登记编号"/></label>
          <label className="manual-note-field">备注<input maxLength={512} value={form.note} onChange={event => setForm({ ...form, note: event.target.value })}/></label>
        </div>
        {formError && <div className="admin-alert error" role="alert">{formError}</div>}
        <div className="admin-form-actions"><button className="admin-secondary" type="button" disabled={saving} onClick={closeDialog}>取消</button><button className="admin-primary" disabled={saving || !employee || amountCents == null}>{saving ? '正在登记…' : '登记为待核对记录'}</button></div>
      </form>
    </Modal>}

    {dialog === 'confirm' && selected && <Modal title="核对供餐记录" className="manual-supply-modal" onClose={closeDialog}>
      <div className="record-summary-grid manual-confirm-grid"><div><span>员工</span><strong>{selected.employee_name}（{selected.employee_no}）</strong><small>{selected.department || '未填写部门'} · {accountStatusLabel(selected.account_status)}</small></div><div><span>供餐日期与餐次</span><strong>{selected.business_date} · {selected.meal_name}</strong><small>员工状态：{accountStatusLabel(selected.employee_status)}</small></div><div><span>实际供餐金额</span><strong>{yuan(selected.amount_cents)}</strong><small>凭据：{selected.receipt_ref}</small></div><div><span>登记备注</span><strong>{selected.note || '无'}</strong><small>登记时间 {formatAdminDate(selected.created_at)}</small></div></div>
      {selected.exception_reason && <div className="admin-alert error" role="alert">上次处理未完成：{manualExceptionLabel(selected.exception_reason)}。处理后可从原记录重试。</div>}
      <p className="manual-charge-warning">{selected.account_status !== 'ACTIVE' || selected.employee_status !== 'ACTIVE' ? '员工或账户不可用，确认后只会将记录标记为异常，不会扣款。' : `确认后将扣款 ${yuan(selected.amount_cents)}，并按实际入账时间计入日结。`}</p>
      <div className="admin-form-actions"><button className="admin-secondary" type="button" disabled={posting} onClick={closeDialog}>返回列表</button><button className="admin-primary" type="button" disabled={posting} onClick={() => void postSelected()}>{posting ? '正在核对并入账…' : selected.account_status !== 'ACTIVE' || selected.employee_status !== 'ACTIVE' ? '标记异常' : `确认补录并扣款 ${yuan(selected.amount_cents)}`}</button></div>
    </Modal>}

    {dialog === 'result' && result && <Modal title={result.title} className="manual-result-modal" onClose={closeDialog}>
      <div className={`admin-alert ${result.kind === 'success' ? 'success' : 'error'} manual-result-message`} role={result.kind === 'success' ? 'status' : 'alert'}><strong>{result.kind === 'success' ? '操作已完成' : '操作未完成'}</strong><p>{result.message}</p></div>
      <div className="admin-form-actions"><button className="admin-primary" type="button" onClick={closeResult}>{result.kind === 'success' ? '返回记录列表核实' : selected ? '返回核对并重试' : '返回修改'}</button></div>
    </Modal>}
  </div>;
}

function manualStatusLabel(status: string): string {
  return status === 'PENDING' ? '待核对' : status === 'EXCEPTION' ? '异常待处理' : status === 'POSTED' ? '已补录' : status;
}
function manualExceptionLabel(reason: string): string {
  return reason === 'INSUFFICIENT_FUNDS' ? '账户余额不足，请补足余额后重试' : reason === 'ACCOUNT_UNAVAILABLE' ? '员工或账户当前不可用，请处理状态后重试' : reason;
}

function ImportPanel({ session, onNotice }: { session: AdminSession; onNotice: (value: string) => void }) {
  type Preview = { preview_id: string; rows: Record<string, unknown>[]; errors: Record<string, unknown>[]; total_rows: number; valid_rows: number; error_rows: number };
  type CreatedEmployee = { employee?: Record<string, unknown>; temporary_password?: string };
  const [file, setFile] = useState<File | null>(null); const [preview, setPreview] = useState<Preview | null>(null); const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<Record<string, unknown>[]>([]); const [confirmed, setConfirmed] = useState(false); const [error, setError] = useState('');
  const [resultSummary, setResultSummary] = useState<{ created: number; errors: number } | null>(null);
  async function previewFile() {
    if (!file) return;
    setBusy(true); setError(''); setResultSummary(null); onNotice('');
    try {
      const form = new FormData(); form.append('file', file);
      const value = await adminApi.generic<Preview>(session, '/api/admin/imports/preview', { method: 'POST', body: form });
      setPreview(value); setCreated([]); setConfirmed(false);
    } catch (reason) { setError(message(reason)); }
    finally { setBusy(false); }
  }
  async function confirmImport() {
    if (!preview || preview.valid_rows < 1 || confirmed || busy) return;
    if (!window.confirm(`将新建 ${preview.valid_rows} 名员工，并跳过 ${preview.error_rows} 条错误记录。导入仅创建新员工，不会更新现有档案。确认继续？`)) return;
    setBusy(true); setError(''); onNotice('');
    try {
      const result = await adminApi.generic<{ created?: CreatedEmployee[]; errors?: Record<string, unknown>[]; created_rows?: number; error_rows?: number }>(session, `/api/admin/imports/${encodeURIComponent(preview.preview_id)}/confirm`, { method: 'POST', body: JSON.stringify({}) });
      const createdRows = result.created_rows ?? result.created?.length ?? 0;
      const errorRows = result.error_rows ?? result.errors?.length ?? 0;
      setCreated((result.created || []).map(item => ({ ...(item.employee || {}), temporary_password: item.temporary_password || '' })));
      setPreview(current => current ? { ...current, errors: result.errors || [], error_rows: errorRows } : current);
      setResultSummary({ created: createdRows, errors: errorRows }); setConfirmed(true);
      onNotice(`导入完成：实际新建 ${createdRows} 名员工，失败 ${errorRows} 条。`);
    } catch (reason) { setError(message(reason)); }
    finally { setBusy(false); }
  }
  return <div className="admin-import-flow">
    <section className="import-step">
      <header><div><p className="admin-eyebrow">第一步</p><h3>准备并选择员工表格</h3></div><ExportButton session={session} kind="template" path="/api/admin/imports/template" filename="employee-import-template.xlsx" label="下载 Excel 模板"/></header>
      <p className="admin-muted import-template">请在模板“员工导入”表第 2 行起填写员工编号、姓名、手机号、部门、状态，字段要求见“填写说明”表。前四项必填；状态填“正常”或“冻结”，留空默认为正常。此导入只新建员工，不会覆盖已有档案。</p>
      <div className="import-upload-row"><label className="file-picker">选择 XLSX 文件<input type="file" accept=".xlsx" onChange={event => { setFile(event.target.files?.[0] || null); setPreview(null); setCreated([]); setConfirmed(false); setResultSummary(null); setError(''); onNotice(''); }}/></label><button className="admin-secondary" type="button" disabled={!file || busy} onClick={() => void previewFile()}>{busy && !confirmed ? '正在校验…' : '上传并预览'}</button></div>
    </section>
    {error && <div className="admin-alert error" role="alert">{error}</div>}
    {preview && <section className="import-step import-preview">
      <header><div><p className="admin-eyebrow">第二步</p><h3>核对导入预览</h3></div><span className="admin-status">预览有效期 1 小时 · 仅可确认一次</span></header>
      <div className="import-counts" aria-label="员工导入行数"><div><small>总记录</small><strong>{preview.total_rows}</strong></div><div><small>可新建</small><strong className="count-valid">{preview.valid_rows}</strong></div><div><small>错误/跳过</small><strong className={preview.error_rows ? 'count-error' : ''}>{preview.error_rows}</strong></div></div>
      <p className="admin-muted">请检查有效员工资料与错误行。错误行不会导入，预览后的工号或手机号冲突也会在最终结果中列出。</p>
      <h4>表格记录</h4><Table rows={preview.rows}/>
      {preview.errors.length > 0 && <div className="import-error-report"><header><h4>错误行及原因</h4><ExportButton session={session} kind="error-report" path={`/api/admin/imports/${encodeURIComponent(preview.preview_id)}/errors`} filename="import-errors.csv" label="下载错误报告"/></header><Table rows={preview.errors}/></div>}
      {!confirmed && <div className="import-confirm-row"><p>{preview.valid_rows > 0 ? `确认将新建 ${preview.valid_rows} 名员工，跳过 ${preview.error_rows} 条错误记录。` : '没有可导入的有效员工。请下载错误报告，修正源表后重新预览。'}</p><button className="admin-primary" type="button" disabled={busy || preview.valid_rows < 1} onClick={() => void confirmImport()}>{busy ? '正在创建员工…' : `确认新建 ${preview.valid_rows} 名员工并跳过 ${preview.error_rows} 条错误`}</button></div>}
      {resultSummary && <div className={`admin-alert ${resultSummary.errors ? 'error' : 'success'}`} role="status">导入请求已完成：实际新建 {resultSummary.created} 名员工，失败 {resultSummary.errors} 条。{resultSummary.errors > 0 ? '失败记录已加入下方错误报告。' : ''}</div>}
      {created.length > 0 && <div className="import-created"><h4>新建员工与临时密码</h4><p className="admin-muted">临时密码仅本次响应后在当前页面展示。请安全交付给对应员工；离开此页或刷新后无法再次查看。</p><Table rows={created}/></div>}
    </section>}
  </div>;
}

/** The signed-in administrator's own authenticator. Binding is optional, so the
 * panel works in three states: unbound, pending, bound. */
function SelfSecurityPanel({ session, security, busy, onNotice, onExpired }: {
  session: AdminSession; security: SecurityState | null; busy: boolean;
  onNotice: (value: string) => void; onExpired: () => void;
}) {
  const [step, setStep] = useState<'idle' | 'credentials' | 'scan'>('idle');
  // Which change the administrator asked for. Binding and replacing both collect
  // the password (and, when already bound, the current code) and then show a new
  // QR code; unbinding collects the same credentials and removes the binding.
  const [intent, setIntent] = useState<'bind' | 'replace' | 'unbind'>('bind');
  const [password, setPassword] = useState('');
  const [currentCode, setCurrentCode] = useState('');
  const [confirmCode, setConfirmCode] = useState('');
  const [enrollment, setEnrollment] = useState<SecondFactorEnrollment | null>(null);
  const [qrData, setQrData] = useState('');
  const [localBusy, setLocalBusy] = useState(false);
  const bound = Boolean(security?.second_factor_bound);

  // The QR code is rendered from the URI the server returned for this pending
  // binding only. Nothing is cached: the secret is shown once and never again.
  useEffect(() => {
    let cancelled = false;
    setQrData('');
    if (enrollment) void QRCode.toDataURL(enrollment.otpauth_uri, {
      errorCorrectionLevel: 'M', margin: 2, width: 260, color: { dark: '#17324D', light: '#FFFFFF' },
    }).then(data => { if (!cancelled) setQrData(data); });
    return () => { cancelled = true; };
  }, [enrollment]);

  const reset = () => { setStep('idle'); setIntent('bind'); setPassword(''); setCurrentCode(''); setConfirmCode(''); setEnrollment(null); };
  const choose = (next: 'bind' | 'replace' | 'unbind') => { setIntent(next); setStep('credentials'); setPassword(''); setCurrentCode(''); onNotice(''); };

  async function start() {
    setLocalBusy(true); onNotice('');
    try {
      const value = await adminApi.startEnrollment(session, password, currentCode.trim());
      setEnrollment(value); setStep('scan'); setConfirmCode('');
    } catch (reason) { onNotice(message(reason)); }
    finally { setLocalBusy(false); }
  }

  async function confirm() {
    setLocalBusy(true); onNotice('');
    try {
      await adminApi.confirmEnrollment(session, confirmCode.trim());
      reset();
      // Binding revokes every session, including this one, so the page must sign
      // in again instead of pretending the old session still works.
      saveAdminSession(null);
      onNotice('动态验证码已启用，请使用新验证码重新登录。');
      onExpired();
    } catch (reason) { onNotice(message(reason)); }
    finally { setLocalBusy(false); }
  }

  async function remove() {
    setLocalBusy(true); onNotice('');
    try {
      await adminApi.removeSecondFactor(session, password, currentCode.trim());
      reset();
      saveAdminSession(null);
      onNotice('动态验证码已解除绑定，请重新登录。');
      onExpired();
    } catch (reason) { onNotice(message(reason)); }
    finally { setLocalBusy(false); }
  }

  return <Panel title="我的登录安全" description="动态验证码（TOTP）为可选绑定；未绑定时仅凭密码登录，绑定时每次登录都需要验证码。"
    action={<span className={`admin-status ${bound ? 'ok' : 'warn'}`}>{bound ? '已绑定' : '未绑定'}</span>}>
    {security?.enrollment_pending && step !== 'scan' && <div className="admin-alert" role="status">有一个未完成的绑定过程{security.enrollment_expires_at ? `，将在 ${new Date(security.enrollment_expires_at).toLocaleTimeString('zh-CN', { hour12: false })} 失效` : ''}。重新开始会作废它。</div>}

    {step === 'idle' && <div className="security-actions">
      <p className="admin-muted">{bound
        ? '已绑定动态验证码。更换需先输入密码和当前的验证码，解绑后仅凭密码登录。'
        : '尚未绑定动态验证码。绑定后每次登录都需要输入验证码；请同时保存恢复方式，遗忘时由运维在服务器上执行恢复命令。'}</p>
      <div className="admin-inline-actions">
        <button className="admin-primary" type="button" onClick={() => choose(bound ? 'replace' : 'bind')} disabled={busy}>{bound ? '更换动态验证码' : '绑定动态验证码'}</button>
        {bound && <button className="admin-secondary" type="button" onClick={() => choose('unbind')}>解除绑定</button>}
      </div>
    </div>}

    {step === 'credentials' && <form className="admin-form" onSubmit={event => { event.preventDefault(); void (intent === 'unbind' ? remove() : start()); }}>
      <label>当前登录密码<input type="password" autoComplete="current-password" required value={password} onChange={e => setPassword(e.target.value)}/></label>
      {bound && <label>当前动态验证码<input inputMode="numeric" autoComplete="one-time-code" required value={currentCode} onChange={e => setCurrentCode(e.target.value)}/><small className="admin-muted">来自当前已绑定的验证器。</small></label>}
      {!bound && <p className="admin-muted">首次绑定无需旧验证码；输入密码后系统会给出二维码。</p>}
      <div className="admin-form-actions">
        <button className="admin-secondary" type="button" onClick={reset} disabled={localBusy}>取消</button>
        <button className={intent === 'unbind' ? 'admin-secondary' : 'admin-primary'} disabled={localBusy}>{localBusy ? '正在处理…' : intent === 'unbind' ? '确认解除绑定' : '生成新二维码'}</button>
      </div>
      {intent === 'unbind'
        ? <p className="admin-muted">解除绑定后所有管理员会话都会被注销，需要使用密码重新登录。</p>
        : bound
          ? <p className="admin-muted">验证通过后系统会给出新的二维码；用新验证器扫码并输入新验证码即完成更换。</p>
          : <p className="admin-muted">验证通过后系统会给出二维码，扫码并输入验证码即完成绑定。</p>}
    </form>}

    {step === 'scan' && enrollment && <div className="enrollment-panel">
      <p className="admin-muted">用验证器扫描下方二维码，或手动输入密钥，然后填写验证器生成的 6 位验证码完成绑定。二维码与密钥仅在本次显示，页面刷新后需重新开始。</p>
      {qrData ? <img className="enrollment-qr" src={qrData} alt="动态验证码绑定二维码"/> : <div className="admin-empty">正在生成二维码…</div>}
      <p className="enrollment-secret"><span>手动输入密钥</span><code>{enrollment.secret}</code></p>
      <p className="admin-muted">该二维码将在 {new Date(enrollment.expires_at).toLocaleTimeString('zh-CN', { hour12: false })} 失效。</p>
      <form className="admin-form" onSubmit={event => { event.preventDefault(); void confirm(); }}>
        <label>验证器显示的验证码<input inputMode="numeric" autoComplete="one-time-code" required minLength={6} maxLength={6} value={confirmCode} onChange={e => setConfirmCode(e.target.value)}/></label>
        <div className="admin-form-actions">
          <button className="admin-secondary" type="button" onClick={reset} disabled={localBusy}>取消</button>
          <button className="admin-primary" disabled={localBusy || confirmCode.trim().length !== 6}>{localBusy ? '正在验证…' : '完成绑定'}</button>
        </div>
      </form>
    </div>}
  </Panel>;
}

/** Creating further administrators and seeing who exists. Every administrator is
 * equal: there is deliberately no role or permission tier. */
function AdministratorPanel({ session, me, accounts, busy, onNotice, onChanged }: {
  session: AdminSession; me: { id: number; username: string } | null; accounts: AdminAccount[];
  busy: boolean; onNotice: (value: string) => void; onChanged: () => void;
}) {
  const [showCreate, setShowCreate] = useState(false);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [localBusy, setLocalBusy] = useState(false);

  async function create(event: FormEvent) {
    event.preventDefault();
    if (password.length < 12) { onNotice('初始密码至少需要 12 个字符。'); return; }
    setLocalBusy(true); onNotice('');
    try {
      const created = await adminApi.createAdministrator(session, username.trim(), password);
      onNotice(`管理员 ${created.administrator.username} 已创建。请通过安全渠道交付初始密码；该账号未绑定动态验证码，首次登录后可在“管理员与安全”中自行绑定。`);
      setShowCreate(false); setUsername(''); setPassword('');
      onChanged();
    } catch (reason) { onNotice(message(reason)); }
    finally { setLocalBusy(false); }
  }

  return <Panel title="管理员账号" description="所有管理员权限相同，没有角色区分。新建账号初始未绑定动态验证码。" action={<button className="admin-primary compact" type="button" onClick={() => setShowCreate(true)}>新建管理员</button>}>
    {accounts.length ? <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>账号</th><th>动态验证码</th><th>创建时间</th></tr></thead><tbody>{accounts.map(item => <tr key={item.id}><td>{item.username}{me?.id === item.id && <span className="admin-muted"> （当前登录）</span>}</td><td><span className={`admin-status ${item.second_factor_bound ? 'ok' : 'warn'}`}>{item.second_factor_bound ? '已绑定' : '未绑定'}</span></td><td>{item.created_at ? new Date(item.created_at).toLocaleString('zh-CN', { hour12: false }) : '—'}</td></tr>)}</tbody></table></div> : <div className="admin-empty">暂无管理员记录</div>}
    <p className="admin-muted">其他管理员的动态验证码无法由这里代为设置、查看或解除；每位管理员只能管理自己的绑定。</p>
    {showCreate && <Modal title="新建管理员" onClose={() => setShowCreate(false)}>
      <form className="admin-form" onSubmit={create}>
        <label>管理员账号<input autoComplete="off" required minLength={3} maxLength={64} pattern="[a-z0-9._\-]+" value={username} onChange={e => setUsername(e.target.value)}/><small className="admin-muted">3–64 位小写字母、数字、点、下划线或短横线。</small></label>
        <label>初始密码<input type="password" autoComplete="new-password" required minLength={12} value={password} onChange={e => setPassword(e.target.value)}/><small className="admin-muted">至少 12 个字符，请通过安全渠道交付。</small></label>
        <div className="admin-form-actions"><button className="admin-secondary" type="button" onClick={() => setShowCreate(false)} disabled={localBusy}>取消</button><button className="admin-primary" disabled={localBusy || busy}>{localBusy ? '正在创建…' : '创建管理员'}</button></div>
      </form>
    </Modal>}
  </Panel>;
}

type DailyReconciliationRecord = {
  business_date: string; opening_cents: number; movement_cents: number; expected_cents: number;
  actual_cents: number; difference_cents: number; generated_by?: number; generated_at?: string;
  breakdown_cents?: Record<string, number>;
};

function DailyReconciliation({ date, records, loading, error, busy, onDate, onRetry, exportButton, onGenerate }: {
  date: string; records: Record<string, unknown>[]; loading: boolean; error: string; busy: boolean;
  onDate: (date: string) => void; onRetry: () => void; exportButton: ReactNode; onGenerate: () => void;
}) {
  const record = records.find(item => item.business_date === date) as unknown as DailyReconciliationRecord | undefined;
  const maxDate = businessToday();
  const breakdown = Object.entries(record?.breakdown_cents || {});
  const breakdownLabels: Record<string, string> = {
    RECHARGE: '线下充值', RECHARGE_REVERSAL: '充值冲正', CONSUME: '餐费消费',
    REFUND: '消费退款', BALANCE_ADJUSTMENT: '余额调整', BALANCE_WITHDRAWAL: '余额退还',
  };
  return <Panel title="每日余额日结" description="按食堂业务日期核对余额变化。日结只展示差异，不会自动调整任何员工账户。">
    <div className="daily-toolbar"><label>业务日期<input type="date" value={date} max={maxDate} onChange={event => onDate(event.target.value)}/></label><div><small>当前选择</small><strong>{date}</strong></div>{exportButton}<button className="admin-primary" type="button" disabled={busy || loading || !date || date > maxDate} onClick={onGenerate}>{busy ? '正在生成所选日期…' : record ? `重新生成 ${date} 日结` : `生成 ${date} 日结`}</button></div>
    {error && <ErrorBox retry={onRetry}>{error}</ErrorBox>}
    {loading && !record ? <Loading/> : record ? <section className="daily-result" aria-live="polite">
      <header className="daily-result-head"><div><p className="admin-eyebrow">{record.business_date} 日结结果</p><h3>{record.difference_cents === 0 ? '账面一致' : '发现余额差异'}</h3></div><span className={`admin-status ${record.difference_cents === 0 ? 'ok' : 'warn'}`}>{record.difference_cents === 0 ? '无差异' : record.difference_cents < 0 ? `实际少 ${yuan(Math.abs(record.difference_cents))}` : `实际多 ${yuan(record.difference_cents)}`}</span></header>
      <div className="daily-metric-grid"><div><small>期初余额</small><strong>{yuan(record.opening_cents)}</strong></div><div><small>当日资金变动</small><strong className={record.movement_cents < 0 ? 'amount-negative' : 'amount-positive'}>{signedYuan(record.movement_cents)}</strong></div><div><small>理论期末</small><strong>{yuan(record.expected_cents)}</strong></div><div><small>实际期末</small><strong>{yuan(record.actual_cents)}</strong></div><div className={record.difference_cents === 0 ? '' : 'daily-difference'}><small>实际 − 理论</small><strong>{signedYuan(record.difference_cents)}</strong></div></div>
      {record.difference_cents !== 0 && <div className="admin-alert error" role="status">差异为 {signedYuan(record.difference_cents)}。请进一步核对流水与收款复核；本页面不会自动调账。</div>}
      <div className="daily-breakdown"><h4>当日资金变动组成</h4>{breakdown.length ? <dl>{breakdown.map(([kind, amount]) => <div key={kind}><dt>{breakdownLabels[kind] || kind}</dt><dd className={amount < 0 ? 'amount-negative' : amount > 0 ? 'amount-positive' : ''}>{signedYuan(amount)}</dd></div>)}</dl> : <p className="admin-muted">服务端未返回变动明细。</p>}</div>
      <p className="daily-generated">生成于 {record.generated_at ? formatAdminDate(record.generated_at) : '—'} · 生成管理员 {record.generated_by ? `#${record.generated_by}` : '系统'}</p>
    </section> : !loading ? <div className="daily-empty"><span aria-hidden="true">◷</span><h3>尚无 {date} 的日结结果</h3><p>生成后会显示期初、变动、理论期末、实际期末和差异。历史日与今天按服务端账务规则计算。</p></div> : null}
  </Panel>;
}

type BackupRecord = { id: number; filename: string; status: string; created_at: string; external: boolean };

function BackupManager({ session }: { session: AdminSession }) {
  const [items, setItems] = useState<BackupRecord[]>([]); const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false); const [historyError, setHistoryError] = useState(''); const [actionError, setActionError] = useState(''); const [actionMessage, setActionMessage] = useState('');
  const requestID = useRef(0);
  const refresh = useCallback(async () => {
    const request = ++requestID.current; setLoading(true); setHistoryError('');
    try {
      const response = await adminApi.generic<{ items: BackupRecord[] }>(session, '/api/admin/backups');
      if (request === requestID.current) setItems(response.items || []);
    } catch (reason) { if (request === requestID.current) setHistoryError(message(reason)); }
    finally { if (request === requestID.current) setLoading(false); }
  }, [session]);
  useEffect(() => { void refresh(); }, [refresh]);

  const recentSuccess = items.find(item => item.status === 'COMPLETE' && item.external);
  async function createBackup() {
    if (saving || !window.confirm('将为当前数据库创建一致性快照，并按现有策略复制到外部备份目录。此操作不会暂停系统。是否现在备份？')) return;
    setSaving(true); setHistoryError(''); setActionError(''); setActionMessage('');
    try {
      const response = await adminApi.generic<{ backup: BackupRecord }>(session, '/api/admin/backups', { method: 'POST', body: '{}' });
      const item = response.backup;
      setItems(current => [item, ...current.filter(existing => existing.id !== item.id)]);
      setActionMessage(item.status === 'COMPLETE' && item.external ? `服务端确认备份完成：${item.filename}` : `本地快照已生成，但外部副本状态为 ${item.status}。`);
      await refresh();
    } catch (reason) {
      setActionError(message(reason));
      // Failed external copies are still recorded by the service. Reload history
      // so the operator can see that real outcome and keep prior success visible.
      await refresh();
    } finally { setSaving(false); }
  }

  return <Panel title="备份管理" description="沿用 SQLite 一致性快照、外部副本和既有留存策略；备份完成状态来自服务端同步结果。" action={<button className="admin-secondary" type="button" onClick={() => void refresh()} disabled={loading || saving}>刷新历史</button>}>
    <section className="backup-overview"><div className="backup-last-success"><span className="backup-icon" aria-hidden="true">✓</span><div><small>最近成功备份</small><strong>{recentSuccess ? formatAdminDate(recentSuccess.created_at) : '尚无成功记录'}</strong><span>{recentSuccess ? '本地快照与外部副本均已完成' : '首次备份完成后会显示在这里'}</span></div></div><div className="backup-action"><button className="admin-primary" type="button" onClick={() => void createBackup()} disabled={saving}>{saving ? '备份处理中…' : '立即创建备份'}</button><p>{saving ? '正在等待服务端完成数据库快照和外部副本，请不要重复提交。' : '提交后等待真实完成结果；大数据库可能需要一些时间。'}</p></div></section>
    {actionMessage && <div className={`admin-alert ${actionMessage.includes('但外部副本') ? 'error' : 'success'}`} role="status">{actionMessage}</div>}
    {actionError && <div className="admin-alert error" role="alert">{actionError} 如果备份已落库，请查看下方历史记录确认服务端状态。</div>}
    {historyError && <ErrorBox retry={() => void refresh()}>{historyError}</ErrorBox>}
    <section className="backup-history"><header><div><p className="admin-eyebrow">最近 100 条</p><h3>备份历史</h3></div></header>
      {loading && items.length === 0 ? <Loading/> : items.length ? <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>备份时间</th><th>文件名</th><th>结果</th><th>外部副本</th></tr></thead><tbody>{items.map(item => <tr key={item.id}><td>{formatAdminDate(item.created_at)}</td><td>{item.filename}</td><td><span className={`admin-status ${item.status === 'COMPLETE' ? 'ok' : 'warn'}`}>{item.status === 'COMPLETE' ? '成功' : item.status === 'EXTERNAL_FAILED' ? '外部副本失败' : item.status}</span></td><td>{item.external ? '已完成' : item.status === 'EXTERNAL_FAILED' ? '未完成' : '—'}</td></tr>)}</tbody></table></div> : !loading ? <div className="admin-empty">暂无备份记录。可创建第一份一致性备份。</div> : null}
      {loading && items.length > 0 && <p className="admin-muted" role="status">正在刷新历史；当前已知记录仍保留显示…</p>}
    </section>
  </Panel>;
}

function ExportButton({ session, kind, path, filename, filters, all = false, label }: {
  session: AdminSession; kind: string; path?: string; filename?: string; filters?: Record<string, string | number | undefined>;
  all?: boolean; label?: string;
}) {
  const [busy, setBusy] = useState(false); const [error, setError] = useState(''); const [status, setStatus] = useState('');
  async function download() {
    if (busy) return;
    setBusy(true); setError(''); setStatus('');
    try {
      let blob: Blob; let serverFilename: string | undefined;
      if (path) {
        blob = await adminApi.generic<Blob>(session, path);
      } else {
        const query = new URLSearchParams();
        if (all) query.set('all', '1');
        else for (const [key, value] of Object.entries(filters || {})) if (value !== undefined && value !== '') query.set(key, String(value));
        const queryString = query.toString();
        const result = await adminApi.exportCSV(session, `/api/admin/exports/${encodeURIComponent(kind)}${queryString ? `?${queryString}` : ''}`);
        if (result.empty) { setStatus('没有符合条件的数据，无需下载文件。'); return; }
        if (!result.blob) throw new Error('服务端没有返回导出文件。');
        blob = result.blob; serverFilename = result.filename;
      }
      const url = URL.createObjectURL(blob); const anchor = document.createElement('a');
      anchor.href = url; anchor.download = filename || serverFilename || `${kind.replaceAll('/', '-')}.csv`; anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
      setStatus(`已下载 ${anchor.download}`);
    } catch (reason) { setError(message(reason)); }
    finally { setBusy(false); }
  }
  return <div className="export-item"><button className="admin-secondary" type="button" disabled={busy} onClick={() => void download()}>{busy ? '正在准备文件…' : label || (path ? '下载报告' : all ? `导出${kind}全部数据` : `导出筛选的${kind}`)}</button>{status && <small role="status">{status}</small>}{error && <small className="inline-error" role="alert">{error}</small>}</div>;
}
