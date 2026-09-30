import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react';
import QRCode from 'qrcode';
import { adminApi, AdminApiError, readAdminSession, saveAdminSession, type AdminAccount, type AdminEmployee, type AdminMeal, type AdminSession, type ConsumptionModes, type SecondFactorEnrollment, type SecurityState } from './adminApi';
import './admin.css';

type Section = 'employees' | 'ledger' | 'meals' | 'modes' | 'terminals' | 'closing' | 'operations' | 'security' | 'audit';
type LoadState = { busy: boolean; error: string; data: unknown };
const emptyLoad: LoadState = { busy: false, error: '', data: null };
const sectionLabels: Record<Section, string> = { employees: '人员与账户', ledger: '资金流水', meals: '餐次配置', modes: '消费模式', terminals: '终端与事件', closing: '日结与复核', operations: '导入、补录与备份', security: '管理员与安全', audit: '审计记录' };

/** The message shown when the server refuses something the page could have
 * predicted. Every security endpoint answers with a code, and the code is the
 * stable contract; the message is only a fallback. */
function securityMessage(error: unknown): string {
  if (!(error instanceof AdminApiError)) return '操作失败，请稍后重试。';
  switch (error.code) {
    case 'INVALID_CREDENTIALS': return '密码或当前动态验证码不正确。';
    case 'INVALID_STATE': return error.message;
    case 'CONFLICT': return error.message;
    case 'ALREADY_INITIALIZED': return '系统已经有管理员，无法再次初始化。';
    case 'ENTRANCE_DISABLED': return '该消费入口已关闭。';
    default: return message(error);
  }
}

function message(error: unknown): string {
  if (!(error instanceof AdminApiError)) return '操作失败，请稍后重试。';
  if (error.status === 401) return '管理员会话已失效，请重新登录。';
  if (error.status === 404) return '此功能的服务接口尚未开放。';
  if (error.code === 'INVALID_CREDENTIALS') return '账号、密码或动态验证码不正确。';
  if (error.code === 'NETWORK') return error.message;
  return error.message;
}

function yuan(cents: number): string { return `¥${(cents / 100).toFixed(2)}`; }
function cents(value: string): number { return Math.round(Number(value) * 100); }
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
    } catch (reason) { setError(securityMessage(reason)); }
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
  const [section, setSection] = useState<Section>('employees');
  const [identity, setIdentity] = useState('');
  const [state, setState] = useState<LoadState>(emptyLoad);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [employees, setEmployees] = useState<AdminEmployee[]>([]);
  const [meals, setMeals] = useState<AdminMeal[]>([]);
  const [modes, setModes] = useState<ConsumptionModes | null>(null);
  const [accounts, setAccounts] = useState<AdminAccount[]>([]);
  const [security, setSecurity] = useState<SecurityState | null>(null);
  const [me, setMe] = useState<{ id: number; username: string } | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [showEdit, setShowEdit] = useState<AdminEmployee | null>(null);
  const [showRecharge, setShowRecharge] = useState<AdminEmployee | null>(null);
  const [businessDate, setBusinessDate] = useState(new Date().toLocaleDateString('en-CA'));
  const [transactionFilters, setTransactionFilters] = useState({ type: '', employee_id: '', from: '', to: '' });

  const expire = useCallback(() => { saveAdminSession(null); onSession(null); setIdentity(''); setState(emptyLoad); }, [onSession]);
  const load = useCallback(async () => {
    if (!session) return;
    setState({ busy: true, error: '', data: null }); setNotice('');
    try {
      let data: unknown;
      if (section === 'employees') { const response = await adminApi.employees(session); setEmployees(response.employees || []); data = response; }
      else if (section === 'meals') { const response = await adminApi.mealPeriods(session); setMeals(response.meal_periods || []); data = response; }
      else if (section === 'security') {
        const [state, people] = await Promise.all([adminApi.security(session), adminApi.administrators(session)]);
        setSecurity(state); setAccounts(people.administrators || []); data = state;
      }
      else if (section === 'modes') { const response = await adminApi.adminConsumptionModes(session); setModes(response); data = response; }
      else if (section === 'operations') { const [response, people] = await Promise.all([adminApi.generic<unknown>(session, '/api/admin/backups'), adminApi.employees(session)]); setEmployees(people.employees || []); data = response; }
      else {
        const paths: Record<Exclude<Section, 'employees' | 'meals' | 'operations' | 'modes' | 'security'>, string> = {
          ledger: '/api/admin/transactions', terminals: '/api/admin/terminals',
          closing: `/api/admin/reconciliation/daily?business_date=${encodeURIComponent(businessDate)}`,
          audit: '/api/admin/audit-events',
        };
        if (section === 'ledger') {
          const query = new URLSearchParams({ limit: '50' });
          for (const [key, value] of Object.entries(transactionFilters)) if (value) query.set(key, value);
          data = await adminApi.generic<unknown>(session, `${paths.ledger}?${query.toString()}`);
        } else data = await adminApi.generic<unknown>(session, paths[section]);
      }
      setState({ busy: false, error: '', data });
    } catch (reason) {
      if (reason instanceof AdminApiError && reason.status === 401) { expire(); return; }
      setState({ busy: false, error: message(reason), data: null });
    }
  }, [session, section, businessDate, transactionFilters, expire]);

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
    setSection('meals');
    setNotice('系统已初始化。请继续配置餐次供应时间与价格。');
    onInitialStateConsumed();
  }, [initialState, session, onInitialStateConsumed]);

  async function runAction(action: () => Promise<unknown>, success: string) {
    setBusy(true); setNotice('');
    try { await action(); setNotice(success); await load(); }
    catch (reason) { if (reason instanceof AdminApiError && reason.status === 401) expire(); else setNotice(message(reason)); }
    finally { setBusy(false); }
  }
  async function logout() { if (session) { try { await adminApi.logout(session); } catch { /* local credentials must still be cleared */ } } expire(); }

  if (!session) return <Login onLogin={onSession}/>;

  return <div className="admin-app">
    <header className="admin-topbar"><a className="admin-brand" href="/admin" aria-label="食堂储值卡管理后台"><img src="/assets/ui/app-logo.png" alt=""/><span><b>食堂储值卡</b><small>管理后台</small></span></a><div className="admin-user"><span>{identity || session.administrator.username}</span><button type="button" onClick={() => void logout()}>退出</button></div></header>
    <div className="admin-layout">
      <aside className="admin-sidebar" aria-label="管理菜单">{(Object.keys(sectionLabels) as Section[]).map(key => <button type="button" key={key} className={section === key ? 'selected' : ''} onClick={() => { setSection(key); setNotice(''); }}>{sectionLabels[key]}</button>)}</aside>
      <main className="admin-main"><div className="admin-title-row"><div><p className="admin-eyebrow">食堂运营控制台</p><h1>{sectionLabels[section]}</h1></div><button className="admin-secondary" type="button" onClick={() => void load()} disabled={state.busy}>刷新</button></div>
        {notice && <div className={`admin-alert ${notice.includes('失败') || notice.includes('尚未') || notice.includes('不正确') ? 'error' : 'success'}`} role="status">{notice}</div>}
        {section === 'employees' && <>
          <Panel title="员工账户" description="员工档案和余额来自服务端，账户变更均通过审计接口提交。" action={<button className="admin-primary compact" type="button" onClick={() => setShowCreate(true)}>新建员工</button>}>
            {state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void load()}>{state.error}</ErrorBox> : <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>员工编号</th><th>姓名</th><th>部门</th><th>手机号</th><th>账户余额</th><th>状态</th><th>操作</th></tr></thead><tbody>{employees.map(person => <tr key={person.id}><td>{person.employee_no}</td><td>{person.name}</td><td>{person.department || '—'}</td><td>{person.phone}</td><td className="money">{yuan(person.balance)}</td><td><span className={`admin-status ${person.status === 'ACTIVE' && person.account_status === 'ACTIVE' ? 'ok' : 'warn'}`}>{person.status}/{person.account_status}</span></td><td><div className="admin-actions"><button type="button" onClick={() => setShowEdit(person)}>编辑</button><button type="button" onClick={() => setShowRecharge(person)}>充值</button>{person.status === 'ACTIVE' || person.status === 'FROZEN' ? <button type="button" onClick={() => void runAction(() => adminApi.setEmployeeStatus(session, person.id, person.status === 'ACTIVE' ? 'FROZEN' : 'ACTIVE'), '员工状态已更新。')}>{person.status === 'ACTIVE' ? '冻结' : '解冻'}</button> : null}<button type="button" onClick={() => void runAction(async () => { const value = await adminApi.resetEmployeePassword(session, person.id); window.prompt('请安全交付临时密码', value.temporary_password); }, '已重置临时密码。')}>重置密码</button></div></td></tr>)}</tbody></table>{employees.length === 0 && <div className="admin-empty">暂无员工档案</div>}</div>}
          </Panel>
          <Panel title="余额调整" description="必须填写业务原因；服务端校验余额下限并记录审计流水。"><AdjustForm employees={employees} busy={busy} onSubmit={(accountId, amount, reason) => runAction(() => adminApi.adjust(session, accountId, amount, reason, idempotencyKey()), '余额调整已登记。')}/></Panel>
          <Panel title="余额退还" description="登记线下退款凭据；余额变化和原消费关联由服务端处理。"><WithdrawalForm session={session} employees={employees} busy={busy} onDone={() => { setNotice('余额退还已登记。'); void load(); }}/></Panel>
        </>}
        {section === 'ledger' && <Panel title="资金流水" description="筛选条件由服务端应用；退款和冲正由服务端原子处理。" action={<ExportButton session={session} kind="transactions"/>}>
          <TransactionFilters value={transactionFilters} onChange={setTransactionFilters}/>
          {state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void load()}>{state.error}</ErrorBox> : <><Table rows={rowsFrom(state.data)}/>{typeof state.data === 'object' && state.data != null && Boolean((state.data as { next_cursor?: string }).next_cursor) && <button className="admin-secondary load-more" type="button" onClick={async () => { const current = state.data as { items?: Record<string, unknown>[]; next_cursor?: string }; if (!current.next_cursor) return; const query = new URLSearchParams({ limit: '50', cursor: current.next_cursor }); for (const [key, value] of Object.entries(transactionFilters)) if (value) query.set(key, value); setState(value => ({ ...value, busy: true, error: '' })); try { const nextPage = await adminApi.generic<{ items?: Record<string, unknown>[]; next_cursor?: string }>(session, `/api/admin/transactions?${query.toString()}`); setState({ busy: false, error: '', data: { items: [...(current.items || []), ...(nextPage.items || [])], next_cursor: nextPage.next_cursor } }); } catch (error) { setState(value => ({ ...value, busy: false, error: message(error) })); } }}>加载更多流水</button>}<div className="ledger-action-forms"><RefundForm session={session} busy={busy} onDone={() => { setNotice('退款请求已提交。'); void load(); }}/><ReverseRechargeForm session={session} busy={busy} onDone={() => { setNotice('充值冲正请求已提交。'); void load(); }}/></div></>}
        </Panel>}
        {section === 'meals' && <Panel title="餐次配置" description="时间、价格变更由服务端检查重叠并记录审计。">
          {state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void load()}>{state.error}</ErrorBox> : <div className="meal-admin-list">{meals.map(meal => <MealEditor key={meal.code} meal={meal} busy={busy} onSave={next => runAction(() => adminApi.updateMeal(session, next), `${next.name}配置已保存。`)}/>)}</div>}
        </Panel>}
        {section === 'modes' && <Panel title="消费模式" description="决定员工可以使用哪些消费入口；修改立即生效并写入审计记录。">
          {state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void load()}>{state.error}</ErrorBox> : modes ? <ConsumptionModeForm modes={modes} busy={busy} onSubmit={next => runAction(async () => { const saved = await adminApi.updateConsumptionModes(session, next); setModes(saved); }, '消费模式已更新，立即生效。')}/> : null}
          <div className="admin-note"><h3>关闭入口时会发生什么</h3><ul><li>该入口已展示但尚未确认的扫码请求、自助消费意图会被终止，不会扣款。</li><li>重新开启后需要员工重新发起消费。</li><li>已完成的消费、历史流水查询和退款不受影响。</li></ul></div>
        </Panel>}
        {section === 'security' && <>
          <SelfSecurityPanel session={session} security={security} busy={busy} onNotice={setNotice} onExpired={expire}/>
          <AdministratorPanel session={session} me={me} accounts={accounts} busy={busy} onNotice={setNotice} onChanged={() => void load()}/>
        </>}
        {section === 'terminals' && <><Panel title="终端状态" description="设备心跳、扫码枪和服务状态由终端服务提供。">{state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void load()}>{state.error}</ErrorBox> : <Table rows={rowsFrom(state.data)}/>}</Panel><Panel title="扫码事件" description="仅展示脱敏事件摘要，不显示原始 Token。"><RemoteTable session={session} path="/api/admin/scan-events"/></Panel></>}
        {section === 'closing' && <><Panel title="每日余额日结" description="日结以资金变动公式核算，差异只告警，不自动修改账户余额。" action={<ExportButton session={session} kind="reconciliation"/>}><form className="admin-inline-form date-form" onSubmit={e => { e.preventDefault(); void load(); }}><label>营业日期<input type="date" value={businessDate} onChange={e => setBusinessDate(e.target.value)}/></label><button className="admin-secondary">查询日结</button><button className="admin-primary" type="button" disabled={busy} onClick={() => void runAction(() => adminApi.generic(session, '/api/admin/reconciliation/daily', { method: 'POST', body: JSON.stringify({ business_date: businessDate }) }), '日结已生成。')}>生成当日日结</button></form>{state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void load()}>{state.error}</ErrorBox> : <Table rows={rowsFrom(state.data)}/>}</Panel><ReceiptReviews session={session} onNotice={setNotice}/></>}
        {section === 'operations' && <><Panel title="人员 Excel 导入" description="先由服务端校验并预览行级结果，再确认创建有效行；错误行会保留在报告中。"><ImportPanel session={session} onNotice={setNotice}/></Panel><Panel title="故障供餐登记与补录" description="保存唯一凭据和营业日期。待处理记录经核对后单独补录为 CONSUME 交易。"><ManualSupply session={session} employees={employees} onNotice={setNotice}/></Panel><Panel title="备份管理" description="备份由服务端执行 SQLite 一致性备份并报告结果。"><div className="admin-inline-actions"><button className="admin-primary" disabled={busy} onClick={() => void runAction(() => adminApi.generic(session, '/api/admin/backups', { method: 'POST', body: '{}' }), '立即备份请求已提交。')}>立即备份</button><button className="admin-secondary" onClick={() => void load()}>刷新备份记录</button></div>{state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void load()}>{state.error}</ErrorBox> : <Table rows={rowsFrom(state.data)}/>}</Panel><Panel title="导出数据" description="导出由服务端生成，浏览器不重算金额。"><div className="export-grid">{['employees', 'balances', 'transactions', 'reconciliation'].map(kind => <ExportButton key={kind} session={session} kind={kind}/>)}</div></Panel></>}
        {section === 'audit' && <Panel title="管理员审计记录" description="重要操作由后端持续记录；此页面不提供删除或修改入口。">{state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void load()}>{state.error}</ErrorBox> : <Table rows={rowsFrom(state.data)}/>}</Panel>}
      </main>
    </div>
    {showCreate && <CreateEmployee onClose={() => setShowCreate(false)} onSave={input => runAction(async () => { const result = await adminApi.createEmployee(session, input); window.prompt('请安全交付员工临时密码', result.temporary_password); setShowCreate(false); }, '员工档案已创建。')}/>}
    {showEdit && <EditEmployee employee={showEdit} onClose={() => setShowEdit(null)} onSave={input => runAction(async () => { await adminApi.updateEmployee(session, showEdit.id, input); setShowEdit(null); }, '员工档案已更新。')}/>}
    {showRecharge && <RechargeModal employee={showRecharge} onClose={() => setShowRecharge(null)} onSave={input => runAction(async () => { await adminApi.recharge(session, input); setShowRecharge(null); }, '充值及收款凭据已登记。')}/>}
  </div>;
}

function Modal({ title, children, onClose }: { title: string; children: ReactNode; onClose: () => void }) {
  return <div className="admin-modal-backdrop" onMouseDown={event => { if (event.target === event.currentTarget) onClose(); }}><section className="admin-modal" role="dialog" aria-modal="true" aria-label={title}><header><h2>{title}</h2><button type="button" aria-label="关闭" onClick={onClose}>×</button></header>{children}</section></div>;
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
    <label>收款方式<select value={method} onChange={e => setMethod(e.target.value)}><option value="CASH">现金</option><option value="BANK_TRANSFER">银行转账</option><option value="OTHER">其他线下方式</option></select></label>
    <div className="admin-form-actions"><button className="admin-secondary" type="button" onClick={onClose}>取消</button><button className="admin-primary">确认已收款并登记</button></div>
  </form></Modal>;
}

function AdjustForm({ employees, busy, onSubmit }: { employees: AdminEmployee[]; busy: boolean; onSubmit: (accountId: number, amount: number, reason: string) => void }) {
  const [employeeId, setEmployeeId] = useState(''); const [amount, setAmount] = useState(''); const [reason, setReason] = useState('');
  return <form className="admin-inline-form" onSubmit={e => { e.preventDefault(); const employee = employees.find(item => item.id === Number(employeeId)); if (employee && reason.trim()) onSubmit(employee.account_id, cents(amount), reason.trim()); }}>
    <label>员工<select required value={employeeId} onChange={e => setEmployeeId(e.target.value)}><option value="">选择员工</option>{employees.map(employee => <option value={employee.id} key={employee.id}>{employee.employee_no} · {employee.name}</option>)}</select></label>
    <label>调整金额（元，可负数）<input required type="number" step="0.01" value={amount} onChange={e => setAmount(e.target.value)}/></label>
    <label className="wide">原因<input required value={reason} onChange={e => setReason(e.target.value)}/></label>
    <button className="admin-primary" disabled={busy || !employeeId}>提交调整</button>
  </form>;
}

function TransactionFilters({ value, onChange }: { value: { type: string; employee_id: string; from: string; to: string }; onChange: (next: typeof value) => void }) {
  const [draft, setDraft] = useState(value);
  return <form className="admin-inline-form transaction-filters" onSubmit={event => { event.preventDefault(); onChange(draft); }}>
    <label>交易类型<select value={draft.type} onChange={e => setDraft({ ...draft, type: e.target.value })}><option value="">全部类型</option><option value="RECHARGE">充值</option><option value="RECHARGE_REVERSAL">充值冲正</option><option value="CONSUME">餐费消费</option><option value="REFUND">退款</option><option value="BALANCE_ADJUSTMENT">余额调整</option><option value="BALANCE_WITHDRAWAL">余额退还</option></select></label>
    <label>员工 ID<input type="number" min="1" value={draft.employee_id} onChange={e => setDraft({ ...draft, employee_id: e.target.value })}/></label>
    <label>开始日期<input type="date" value={draft.from} onChange={e => setDraft({ ...draft, from: e.target.value })}/></label>
    <label>结束日期<input type="date" value={draft.to} onChange={e => setDraft({ ...draft, to: e.target.value })}/></label>
    <button className="admin-secondary">筛选流水</button>
  </form>;
}

function WithdrawalForm({ session, employees, busy, onDone }: { session: AdminSession; employees: AdminEmployee[]; busy: boolean; onDone: () => void }) {
  const [employeeId, setEmployeeId] = useState(''); const [refundId, setRefundId] = useState(''); const [reference, setReference] = useState(''); const [method, setMethod] = useState('BANK_TRANSFER');
  return <form className="admin-inline-form" onSubmit={async event => {
    event.preventDefault(); const employee = employees.find(item => item.id === Number(employeeId)); if (!employee) return;
    try {
      await adminApi.generic(session, `/api/admin/accounts/${employee.account_id}/withdraw`, { method: 'POST', body: JSON.stringify({ payout_ref: reference.trim(), paid_at: new Date().toISOString(), payment_method: method, idempotency_key: idempotencyKey(), related_refund_transaction_id: Number(refundId) }) });
      onDone();
    } catch (error) { window.alert(message(error)); }
  }}>
    <label>员工<select required value={employeeId} onChange={e => setEmployeeId(e.target.value)}><option value="">选择员工</option>{employees.map(person => <option key={person.id} value={person.id}>{person.employee_no} · {person.name} · {yuan(person.balance)}</option>)}</select></label>
    <label>关联退款流水 ID<input required type="number" min="1" value={refundId} onChange={e => setRefundId(e.target.value)}/></label>
    <label>退款凭据编号<input required value={reference} onChange={e => setReference(e.target.value)}/></label>
    <label>退款方式<select value={method} onChange={e => setMethod(e.target.value)}><option value="BANK_TRANSFER">银行转账</option><option value="CASH">现金</option><option value="OTHER">其他线下方式</option></select></label>
    <button className="admin-secondary" disabled={busy || !employeeId}>登记线下余额退还</button>
  </form>;
}

function MealEditor({ meal, busy, onSave }: { meal: AdminMeal; busy: boolean; onSave: (meal: AdminMeal) => void }) {
  const [value, setValue] = useState(meal);
  return <form className="meal-admin-row" onSubmit={e => { e.preventDefault(); onSave(value); }}><div className="meal-admin-name"><b>{meal.name}</b><small>{meal.code}</small></div><label>开始<input type="time" value={value.start_time} onChange={e => setValue({ ...value, start_time: e.target.value })}/></label><label>结束<input type="time" value={value.end_time} onChange={e => setValue({ ...value, end_time: e.target.value })}/></label><label>价格（元）<input type="number" min="0" step="0.01" value={(value.price_cents / 100).toFixed(2)} onChange={e => setValue({ ...value, price_cents: cents(e.target.value) })}/></label><label className="check-label"><input type="checkbox" checked={value.enabled} onChange={e => setValue({ ...value, enabled: e.target.checked })}/>开放</label><button className="admin-secondary" disabled={busy}>保存</button></form>;
}

function RemoteTable({ session, path }: { session: AdminSession; path: string }) {
  const [state, setState] = useState<LoadState>({ ...emptyLoad, busy: true });
  const refresh = useCallback(async () => { setState({ ...emptyLoad, busy: true }); try { const data = await adminApi.generic<unknown>(session, path); setState({ busy: false, data, error: '' }); } catch (error) { setState({ busy: false, data: null, error: message(error) }); } }, [session, path]);
  useEffect(() => { void refresh(); }, [refresh]);
  return state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void refresh()}>{state.error}</ErrorBox> : <Table rows={rowsFrom(state.data)}/>;
}

function RefundForm({ session, busy, onDone }: { session: AdminSession; busy: boolean; onDone: () => void }) {
  const [transactionId, setTransactionId] = useState(''); const [error, setError] = useState('');
  return <form className="admin-inline-form refund-form" onSubmit={async event => { event.preventDefault(); setError(''); try { await adminApi.generic(session, `/api/admin/transactions/${encodeURIComponent(transactionId)}/refund`, { method: 'POST', body: JSON.stringify({ idempotency_key: idempotencyKey(), reason: '后台全额退款' }) }); onDone(); } catch (reason) { setError(message(reason)); } }}><label>原消费流水 ID<input type="number" min="1" required value={transactionId} onChange={e => setTransactionId(e.target.value)}/></label><button className="admin-secondary" disabled={busy}>提交全额退款</button>{error && <span className="inline-error">{error}</span>}</form>;
}

function ReverseRechargeForm({ session, busy, onDone }: { session: AdminSession; busy: boolean; onDone: () => void }) {
  const [transactionId, setTransactionId] = useState(''); const [reason, setReason] = useState(''); const [error, setError] = useState('');
  return <form className="admin-inline-form refund-form" onSubmit={async event => { event.preventDefault(); setError(''); try { await adminApi.reverseRecharge(session, Number(transactionId), reason.trim(), idempotencyKey()); onDone(); } catch (reasonValue) { setError(message(reasonValue)); } }}><label>原充值流水 ID<input type="number" min="1" required value={transactionId} onChange={e => setTransactionId(e.target.value)}/></label><label className="wide">冲正原因<input required value={reason} onChange={e => setReason(e.target.value)}/></label><button className="admin-secondary" disabled={busy}>提交充值冲正</button>{error && <span className="inline-error">{error}</span>}</form>;
}

function ReceiptReviews({ session, onNotice }: { session: AdminSession; onNotice: (value: string) => void }) {
  const [state, setState] = useState<LoadState>({ ...emptyLoad, busy: true });
  const [transactionId, setTransactionId] = useState(''); const [status, setStatus] = useState('MATCHED'); const [note, setNote] = useState('');
  const refresh = useCallback(async () => { setState({ ...emptyLoad, busy: true }); try { const data = await adminApi.generic<unknown>(session, '/api/admin/receipt-reviews'); setState({ busy: false, error: '', data }); } catch (error) { setState({ busy: false, error: message(error), data: null }); } }, [session]);
  useEffect(() => { void refresh(); }, [refresh]);
  return <Panel title="线下收款独立复核" description="由不同管理员核对收款凭据；资金录入人不能复核自己的记录。"><>{state.busy ? <Loading/> : state.error ? <ErrorBox retry={() => void refresh()}>{state.error}</ErrorBox> : <Table rows={rowsFrom(state.data)}/>}<form className="admin-inline-form" onSubmit={async e => { e.preventDefault(); try { await adminApi.generic(session, '/api/admin/receipt-reviews', { method: 'POST', body: JSON.stringify({ transaction_id: Number(transactionId), status, note }) }); onNotice('复核结果已提交。'); await refresh(); } catch (error) { onNotice(message(error)); } }}><label>充值流水 ID<input type="number" min="1" required value={transactionId} onChange={e => setTransactionId(e.target.value)}/></label><label>复核结论<select value={status} onChange={e => setStatus(e.target.value)}><option value="MATCHED">凭据匹配</option><option value="DIFFERENCE">存在差异</option><option value="RESOLVED">差异已处理</option></select></label><label className="wide">复核说明<input value={note} onChange={e => setNote(e.target.value)}/></label><button className="admin-primary">提交复核</button></form></></Panel>;
}

function ManualSupply({ session, employees, onNotice }: { session: AdminSession; employees: AdminEmployee[]; onNotice: (value: string) => void }) {
  const [form, setForm] = useState({ employee_id: '', meal_code: '', amount: '', receipt_ref: '', business_date: new Date().toLocaleDateString('en-CA'), note: '' });
  const [items, setItems] = useState<Record<string, unknown>[]>([]); const [loading, setLoading] = useState(true);
  const refresh = useCallback(async () => { setLoading(true); try { setItems(rowsFrom(await adminApi.generic<unknown>(session, '/api/admin/manual-supplies'))); } catch (error) { onNotice(message(error)); } finally { setLoading(false); } }, [session, onNotice]);
  useEffect(() => { void refresh(); }, [refresh]);
  async function submit(event: FormEvent) { event.preventDefault(); try { await adminApi.generic(session, '/api/admin/manual-supplies', { method: 'POST', body: JSON.stringify({ employee_id: Number(form.employee_id), meal_code: form.meal_code, amount_cents: cents(form.amount), receipt_ref: form.receipt_ref.trim(), business_date: form.business_date, note: form.note }) }); onNotice('故障供餐记录已登记，尚未扣款。核对后请在记录中执行补录。'); await refresh(); } catch (error) { onNotice(message(error)); } }
  async function post(item: Record<string, unknown>) { const description = `${String(item.receipt_ref || '')} · 员工 ${String(item.employee_id || '')} · ${String(item.meal_code || '')} · ${typeof item.amount_cents === 'number' ? yuan(item.amount_cents) : '金额待确认'}`; if (!window.confirm(`确认核对无误并补录为消费交易？\n${description}`)) return; try { await adminApi.generic(session, `/api/admin/manual-supplies/${encodeURIComponent(String(item.id))}/post`, { method: 'POST', body: JSON.stringify({ idempotency_key: idempotencyKey() }) }); onNotice('补录已提交，CONSUME 交易已由服务端登记。'); await refresh(); } catch (error) { onNotice(message(error)); } }
  return <><form className="admin-inline-form" onSubmit={submit}><label>员工<select required value={form.employee_id} onChange={e => setForm({ ...form, employee_id: e.target.value })}><option value="">选择员工</option>{employees.map(person => <option key={person.id} value={person.id}>{person.employee_no} · {person.name}</option>)}</select></label><label>餐次代码<input required value={form.meal_code} onChange={e => setForm({ ...form, meal_code: e.target.value })}/></label><label>金额（元）<input required type="number" min="0.01" step="0.01" value={form.amount} onChange={e => setForm({ ...form, amount: e.target.value })}/></label><label>唯一凭据<input required value={form.receipt_ref} onChange={e => setForm({ ...form, receipt_ref: e.target.value })}/></label><label>营业日期<input required type="date" value={form.business_date} onChange={e => setForm({ ...form, business_date: e.target.value })}/></label><label>备注<input value={form.note} onChange={e => setForm({ ...form, note: e.target.value })}/></label><button className="admin-primary">登记待核对</button></form><div className="manual-supply-list"><h3>待处理供餐记录</h3>{loading ? <Loading/> : items.length ? <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>凭据</th><th>员工</th><th>餐次</th><th>日期</th><th>金额</th><th>状态</th><th>操作</th></tr></thead><tbody>{items.map(item => <tr key={String(item.id)}><td>{String(item.receipt_ref ?? '')}</td><td>{String(item.employee_id ?? '')}</td><td>{String(item.meal_code ?? '')}</td><td>{String(item.business_date ?? '')}</td><td>{typeof item.amount_cents === 'number' ? yuan(item.amount_cents) : '—'}</td><td>{String(item.status ?? '—')}</td><td><button className="admin-link" type="button" disabled={item.status !== 'PENDING'} onClick={() => void post(item)}>核对并补录</button></td></tr>)}</tbody></table></div> : <div className="admin-empty">暂无补录记录</div>}</div></>;
}

function ImportPanel({ session, onNotice }: { session: AdminSession; onNotice: (value: string) => void }) {
  const [file, setFile] = useState<File | null>(null); const [preview, setPreview] = useState<{ preview_id: string; rows: Record<string, unknown>[]; errors: Record<string, unknown>[] } | null>(null); const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<Record<string, unknown>[]>([]); const [confirmed, setConfirmed] = useState(false);
  async function previewFile() {
    if (!file) return;
    setBusy(true);
    try { const form = new FormData(); form.append('file', file); const value = await adminApi.generic<{ preview_id: string; rows: Record<string, unknown>[]; errors: Record<string, unknown>[] }>(session, '/api/admin/imports/preview', { method: 'POST', body: form }); setPreview(value); setCreated([]); setConfirmed(false); }
    catch (error) { onNotice(message(error)); }
    finally { setBusy(false); }
  }
  async function confirmImport() {
    setBusy(true);
    try { const result = await adminApi.generic<{ created?: { employee?: Record<string, unknown>; temporary_password?: string }[]; errors?: Record<string, unknown>[] }>(session, `/api/admin/imports/${encodeURIComponent(preview?.preview_id || '')}/confirm`, { method: 'POST', body: JSON.stringify({}) }); onNotice(`导入完成：新建 ${result.created?.length || 0} 条，错误 ${result.errors?.length || 0} 条。请安全交付临时密码。`); setCreated((result.created || []).map(item => ({ ...(item.employee || {}), temporary_password: item.temporary_password || '' }))); setPreview(value => value ? { ...value, errors: result.errors || [] } : value); setConfirmed(true); }
    catch (error) { onNotice(message(error)); }
    finally { setBusy(false); }
  }
  return <div className="admin-import"><p className="admin-muted import-template">Excel 首行使用列名：employee_no、name、phone、department、status。status 可为 ACTIVE 或 FROZEN。</p><label className="file-picker">选择 Excel 文件<input type="file" accept=".xlsx" onChange={e => { setFile(e.target.files?.[0] || null); setPreview(null); setCreated([]); setConfirmed(false); }}/></label><button className="admin-secondary" disabled={!file || busy} onClick={() => void previewFile()}>{busy ? '正在校验…' : '上传并预览'}</button>{preview != null && <><p className="admin-muted">校验结果由服务端返回，请核对正确行和错误行。</p><Table rows={preview.rows}/>{preview.errors.length > 0 && <><p className="inline-error">错误行报告</p><Table rows={preview.errors}/><ExportButton session={session} kind="error-report" path={`/api/admin/imports/${encodeURIComponent(preview.preview_id)}/errors`} filename="import-errors.csv"/></>}{!confirmed && <button className="admin-primary" disabled={busy} onClick={() => void confirmImport()}>确认导入有效行</button>}{created.length > 0 && <><p className="admin-muted">以下临时密码只在当前页面显示；请通过安全渠道交付给对应员工。</p><Table rows={created}/></>}</>}</div>;
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
    } catch (reason) { onNotice(securityMessage(reason)); }
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
    } catch (reason) { onNotice(securityMessage(reason)); }
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
    } catch (reason) { onNotice(securityMessage(reason)); }
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
    } catch (reason) { onNotice(securityMessage(reason)); }
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

function ExportButton({ session, kind, path, filename }: { session: AdminSession; kind: string; path?: string; filename?: string }) {
  const [busy, setBusy] = useState(false); const [error, setError] = useState('');
  return <div className="export-item"><button className="admin-secondary" disabled={busy} onClick={async () => {
    setBusy(true); setError('');
    try { const blob = await adminApi.generic<Blob>(session, path || `/api/admin/exports/${encodeURIComponent(kind)}`); const url = URL.createObjectURL(blob); const anchor = document.createElement('a'); anchor.href = url; anchor.download = filename || `${kind.replaceAll('/', '-')}.csv`; anchor.click(); window.setTimeout(() => URL.revokeObjectURL(url), 1000); }
    catch (reason) { setError(message(reason)); }
    finally { setBusy(false); }
  }}>{busy ? '正在准备…' : path ? '下载错误报告' : `导出${kind}`}</button>{error && <small>{error}</small>}</div>;
}
