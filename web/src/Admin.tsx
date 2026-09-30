import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { adminApi, AdminApiError, readAdminSession, saveAdminSession, type AdminEmployee, type AdminMeal, type AdminSession } from './adminApi';
import './admin.css';

type Section = 'employees' | 'ledger' | 'meals' | 'terminals' | 'closing' | 'operations' | 'audit';
type LoadState = { busy: boolean; error: string; data: unknown };
const emptyLoad: LoadState = { busy: false, error: '', data: null };
const sectionLabels: Record<Section, string> = { employees: '人员与账户', ledger: '资金流水', meals: '餐次配置', terminals: '终端与事件', closing: '日结与复核', operations: '导入、补录与备份', audit: '审计记录' };

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
  const [session, setSession] = useState<AdminSession | null>(() => readAdminSession());
  const [section, setSection] = useState<Section>('employees');
  const [identity, setIdentity] = useState('');
  const [state, setState] = useState<LoadState>(emptyLoad);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [employees, setEmployees] = useState<AdminEmployee[]>([]);
  const [meals, setMeals] = useState<AdminMeal[]>([]);
  const [showCreate, setShowCreate] = useState(false);
  const [showEdit, setShowEdit] = useState<AdminEmployee | null>(null);
  const [showRecharge, setShowRecharge] = useState<AdminEmployee | null>(null);
  const [businessDate, setBusinessDate] = useState(new Date().toLocaleDateString('en-CA'));
  const [transactionFilters, setTransactionFilters] = useState({ type: '', employee_id: '', from: '', to: '' });

  const expire = useCallback(() => { saveAdminSession(null); setSession(null); setIdentity(''); setState(emptyLoad); }, []);
  const load = useCallback(async () => {
    if (!session) return;
    setState({ busy: true, error: '', data: null }); setNotice('');
    try {
      let data: unknown;
      if (section === 'employees') { const response = await adminApi.employees(session); setEmployees(response.employees || []); data = response; }
      else if (section === 'meals') { const response = await adminApi.mealPeriods(session); setMeals(response.meal_periods || []); data = response; }
      else if (section === 'operations') { const [response, people] = await Promise.all([adminApi.generic<unknown>(session, '/api/admin/backups'), adminApi.employees(session)]); setEmployees(people.employees || []); data = response; }
      else {
        const paths: Record<Exclude<Section, 'employees' | 'meals' | 'operations'>, string> = {
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
    adminApi.me(session).then(value => setIdentity(value.username)).catch(reason => { if (reason instanceof AdminApiError && reason.status === 401) expire(); else setIdentity(session.administrator.username); });
  }, [session, expire]);
  useEffect(() => { void load(); }, [load]);

  async function runAction(action: () => Promise<unknown>, success: string) {
    setBusy(true); setNotice('');
    try { await action(); setNotice(success); await load(); }
    catch (reason) { if (reason instanceof AdminApiError && reason.status === 401) expire(); else setNotice(message(reason)); }
    finally { setBusy(false); }
  }
  async function logout() { if (session) { try { await adminApi.logout(session); } catch { /* local credentials must still be cleared */ } } expire(); }

  if (!session) return <Login onLogin={next => setSession(next)}/>;

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

function ExportButton({ session, kind, path, filename }: { session: AdminSession; kind: string; path?: string; filename?: string }) {
  const [busy, setBusy] = useState(false); const [error, setError] = useState('');
  return <div className="export-item"><button className="admin-secondary" disabled={busy} onClick={async () => {
    setBusy(true); setError('');
    try { const blob = await adminApi.generic<Blob>(session, path || `/api/admin/exports/${encodeURIComponent(kind)}`); const url = URL.createObjectURL(blob); const anchor = document.createElement('a'); anchor.href = url; anchor.download = filename || `${kind.replaceAll('/', '-')}.csv`; anchor.click(); window.setTimeout(() => URL.revokeObjectURL(url), 1000); }
    catch (reason) { setError(message(reason)); }
    finally { setBusy(false); }
  }}>{busy ? '正在准备…' : path ? '下载错误报告' : `导出${kind}`}</button>{error && <small>{error}</small>}</div>;
}
