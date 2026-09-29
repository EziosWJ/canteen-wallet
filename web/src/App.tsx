import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { api, ApiError, clearSession, readSession, type Employee, type MealPeriod, type Session, type Transaction } from './api';
import PaymentCode from './PaymentCode';
import { Brand, Login, PasswordChange, messageFor } from './Auth';
import { Icon, type IconName } from './icons';

const A = '/assets/ui/';
type Page = 'home' | 'qr' | 'ledger' | 'profile';
type LedgerState = { phase: 'idle' | 'loading' | 'ready' | 'error'; items: Transaction[]; cursor: string | null; message: string };
const initialLedger: LedgerState = { phase: 'idle', items: [], cursor: null, message: '' };
type MealState = { phase: 'idle' | 'loading' | 'ready' | 'error'; items: MealPeriod[]; message: string };
const initialMeals: MealState = { phase: 'idle', items: [], message: '' };

function yuan(cents: number): string {
  return `¥${(cents / 100).toFixed(2)}`;
}

function dateTime(value?: string): string {
  if (!value) return '时间待确认';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date);
}

function greeting(): string {
  const hour = new Date().getHours();
  if (hour < 11) return '早上好';
  if (hour < 14) return '中午好';
  if (hour < 18) return '下午好';
  return '晚上好';
}

function Loading({ text }: { text: string }) {
  return <div className="state-box" role="status"><span className="loading-ring"/><p>{text}</p></div>;
}

function Notice({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return <div className="state-box" role="status"><span className="notice-symbol"><Icon name="alert" size={27}/></span><h3>{title}</h3><p>{children}</p>{action}</div>;
}

function Status({ status }: { status: string }) {
  const active = status === 'ACTIVE';
  return <span className={`status ${active ? 'status-good' : 'status-warn'}`}><i/>{active ? '正常使用' : status === 'FROZEN' ? '已冻结' : '不可用'}</span>;
}

function BottomNav({ page, setPage }: { page: Page; setPage: (page: Page) => void }) {
  const entries: { page: Page; label: string; icon: IconName }[] = [
    { page: 'home', label: '首页', icon: 'home' }, { page: 'qr', label: '就餐码', icon: 'qr' },
    { page: 'ledger', label: '记录', icon: 'list' }, { page: 'profile', label: '我的', icon: 'person' },
  ];
  return <nav className="bottom-nav" aria-label="主要导航">{entries.map(entry => <button type="button" key={entry.page} className={page === entry.page ? 'active' : ''} aria-current={page === entry.page ? 'page' : undefined} onClick={() => setPage(entry.page)}><Icon name={entry.icon}/><span>{entry.label}</span></button>)}</nav>;
}

function TransactionRows({ items }: { items: Transaction[] }) {
  return <div className="transaction-list">{items.map(item => {
    const positive = item.amount_cents > 0;
    const labels: Record<string, string> = { CONSUME: '餐费消费', RECHARGE: '线下充值', RECHARGE_REVERSAL: '充值冲正', REFUND: '消费退款', BALANCE_ADJUSTMENT: '余额调整', BALANCE_WITHDRAWAL: '余额清退' };
    const title = labels[item.type] || item.type;
    return <div className="transaction-row" key={item.id}>
      <span className={`transaction-symbol ${positive ? 'positive' : ''}`}><Icon name={positive ? 'wallet' : 'list'} size={22}/></span>
      <span className="transaction-info"><strong>{title}</strong><small>{dateTime(item.created_at)} · {item.transaction_no}</small></span>
      <span className="transaction-amount"><strong className={positive ? 'positive' : ''}>{positive ? '+' : '−'}{yuan(Math.abs(item.amount_cents))}</strong><small>余额 {yuan(item.balance_after_cents)}</small></span>
    </div>;
  })}</div>;
}

function Home({ employee, balance, ledger, meals, setPage, refresh, refreshMeals, onRechargeInfo }: {
  employee: Employee; balance: number; ledger: LedgerState; meals: MealState; setPage: (page: Page) => void; refresh: () => void; refreshMeals: () => void; onRechargeInfo: () => void;
}) {
  const [hidden, setHidden] = useState(false);
  const avatar = employee.photo_url || `${A}default-avatar.png`;
  return <div className="page home-page">
    <header className="home-hero" style={{ backgroundImage: `url(${A}home-header-bg.png)` }}>
      <Brand/>
      <img className="hero-avatar" src={avatar} onError={e => { e.currentTarget.src = `${A}default-avatar.png`; }} alt="员工头像"/>
      <h1>{greeting()}，{employee.name}</h1><p>欢迎使用食堂储值卡</p>
    </header>
    <section className="balance-card" aria-label="储值账户余额">
      <div className="balance-label">账户余额 <button className="inline-icon" type="button" aria-label={hidden ? '显示余额' : '隐藏余额'} onClick={() => setHidden(!hidden)}><Icon name={hidden ? 'eyeOff' : 'eye'} size={23}/></button></div>
      <strong>{hidden ? '••••' : yuan(balance)}</strong>
      <div className="balance-bottom"><Status status={employee.account_status}/><button type="button" onClick={onRechargeInfo}>线下充值说明 <Icon name="arrow" size={18}/></button></div>
    </section>
    <button className="qr-entry" type="button" onClick={() => setPage('qr')}><span className="qr-entry-icon"><Icon name="qr" size={34}/></span><span><strong>出示就餐码</strong><small>扫码后完成餐费消费</small></span><span className="entry-arrow"><Icon name="arrow"/></span></button>
    <div className="quick-links"><button type="button" onClick={() => setPage('ledger')}><span className="quick-icon mint"><Icon name="list" size={28}/></span><strong>资金流水</strong><small>查看余额变化</small></button><button type="button" onClick={() => setPage('profile')}><span className="quick-icon blue"><Icon name="person" size={28}/></span><strong>个人信息</strong><small>查看员工档案</small></button></div>
    <section className="surface meal-section"><div className="section-heading"><h2>今日餐次</h2><span>以食堂实际供应为准</span></div>
      {meals.phase === 'loading' || meals.phase === 'idle' ? <Loading text="正在读取餐次配置"/> : meals.phase === 'error' ? <Notice title="餐次暂不可用" action={<button className="outline-button" type="button" onClick={refreshMeals}>重试</button>}>{meals.message}</Notice> : meals.items.length ? <div className="meal-list">{meals.items.map(period => <div className={`meal-item ${period.enabled ? '' : 'meal-disabled'}`} key={period.code}><strong>{period.name}</strong><span>{period.start_time}–{period.end_time}</span><b>{period.enabled ? yuan(period.price_cents) : '未开放'}</b></div>)}</div> : <Notice title="暂无餐次配置">请等待管理员配置供应时间和餐费。</Notice>}
    </section>
    <section className="surface recent-section"><div className="section-heading"><h2>最近资金流水</h2><button className="text-button" type="button" onClick={() => setPage('ledger')}>查看全部 <Icon name="arrow" size={18}/></button></div>
      {ledger.phase === 'loading' || ledger.phase === 'idle' ? <Loading text="正在读取资金记录"/> : ledger.phase === 'error' ? <Notice title="资金记录暂不可用" action={<button className="outline-button" type="button" onClick={refresh}>重试</button>}>{ledger.message}</Notice> : ledger.items.length ? <TransactionRows items={ledger.items.slice(0, 2)}/> : <Notice title="暂无资金流水">充值或消费后，记录会显示在这里。</Notice>}
    </section>
  </div>;
}

function Ledger({ ledger, loadMore, refresh, balance }: { ledger: LedgerState; loadMore: () => void; refresh: () => void; balance: number }) {
  return <div className="page ledger-page"><header className="page-heading"><h1>资金流水</h1><p>查看每一笔余额变化</p></header><section className="surface ledger-card"><div className="ledger-summary"><span>当前账户余额</span><strong>{yuan(balance)}</strong></div>
    <div className="section-heading"><h2>交易记录</h2><button className="text-button" type="button" onClick={refresh}><Icon name="refresh" size={19}/>刷新</button></div>
    {ledger.phase === 'loading' && !ledger.items.length ? <Loading text="正在加载资金流水"/> : ledger.phase === 'error' && !ledger.items.length ? <Notice title="资金流水暂不可用" action={<button className="outline-button" type="button" onClick={refresh}>重试</button>}>{ledger.message}</Notice> : ledger.items.length ? <><TransactionRows items={ledger.items}/>{ledger.cursor && <button className="outline-button full more-button" type="button" onClick={loadMore} disabled={ledger.phase === 'loading'}>{ledger.phase === 'loading' ? '正在加载…' : '加载更多'}</button>}{ledger.phase === 'error' && <div className="alert alert-error" role="alert">{ledger.message}</div>}</> : <Notice title="暂无资金流水">充值或消费后，记录会显示在这里。</Notice>}
  </section></div>;
}

function Profile({ employee, onChangePassword, onLogout, busy }: { employee: Employee; onChangePassword: () => void; onLogout: () => void; busy: boolean }) {
  return <div className="page profile-page"><header className="page-heading"><h1>个人信息</h1><p>查看员工档案和账户状态</p></header><section className="surface profile-card"><div className="profile-head"><img src={employee.photo_url || `${A}default-avatar.png`} onError={e => { e.currentTarget.src = `${A}default-avatar.png`; }} alt="员工头像"/><div><h2>{employee.name}</h2><p>{employee.employee_no} · {employee.department}</p></div></div><dl><div><dt>手机号</dt><dd>{employee.phone}</dd></div><div><dt>员工编号</dt><dd>{employee.employee_no}</dd></div><div><dt>部门</dt><dd>{employee.department}</dd></div><div><dt>员工状态</dt><dd><Status status={employee.status}/></dd></div><div><dt>账户状态</dt><dd><Status status={employee.account_status}/></dd></div></dl><div className="profile-actions"><button className="outline-button full" type="button" onClick={onChangePassword}><Icon name="lock" size={22}/>修改登录密码</button><button className="danger-button full" type="button" onClick={onLogout} disabled={busy}><Icon name="logout" size={22}/>{busy ? '正在退出…' : '退出登录'}</button></div></section></div>;
}

export default function App() {
  const [session, setSession] = useState<Session | null>(() => readSession());
  const [employee, setEmployee] = useState<Employee | null>(null);
  const [account, setAccount] = useState<{ balance: number; status: string } | null>(null);
  const [booting, setBooting] = useState(!!readSession());
  const [bootError, setBootError] = useState('');
  const [forced, setForced] = useState(false);
  const [changing, setChanging] = useState(false);
  const [page, setPage] = useState<Page>('home');
  const [ledger, setLedger] = useState<LedgerState>(initialLedger);
  const [meals, setMeals] = useState<MealState>(initialMeals);
  const [rechargeInfo, setRechargeInfo] = useState(false);
  const [logoutBusy, setLogoutBusy] = useState(false);
  const sessionRef = useRef(session);
  sessionRef.current = session;

  const expire = useCallback(() => {
    clearSession(); setSession(null); setEmployee(null); setAccount(null); setLedger(initialLedger); setMeals(initialMeals); setForced(false); setChanging(false); setBooting(false); setPage('home');
  }, []);

  const loadProfile = useCallback(async (current: Session) => {
    setBooting(true); setBootError('');
    try {
      const me = await api.me(current);
      if (sessionRef.current?.access_token !== current.access_token) return;
      if (me.must_change_password) { setForced(true); setEmployee(null); setAccount(null); return; }
      const record = me as Employee;
      const funds = await api.account(current);
      if (sessionRef.current?.access_token !== current.access_token) return;
      setEmployee(record); setAccount(funds); setForced(false);
    } catch (reason) {
      if (reason instanceof ApiError && reason.status === 401) expire();
      else if (reason instanceof ApiError && reason.code === 'PASSWORD_CHANGE_REQUIRED') setForced(true);
      else setBootError(messageFor(reason));
    } finally { if (sessionRef.current?.access_token === current.access_token) setBooting(false); }
  }, [expire]);

  useEffect(() => { if (session) void loadProfile(session); }, [session, loadProfile]);

  useEffect(() => {
    if (!session) return;
    const current = session;
    const checkSession = () => {
      if (document.visibilityState !== 'visible') return;
      if (Date.parse(current.expires_at) <= Date.now()) { expire(); return; }
      void api.me(current).then(me => {
        if (sessionRef.current?.access_token === current.access_token && me.must_change_password) setForced(true);
      }).catch(reason => {
        if (reason instanceof ApiError && reason.status === 401 &&
            sessionRef.current?.access_token === current.access_token) expire();
      });
    };
    const timer = window.setInterval(checkSession, 30_000);
    document.addEventListener('visibilitychange', checkSession);
    return () => { window.clearInterval(timer); document.removeEventListener('visibilitychange', checkSession); };
  }, [session, expire]);

  const loadLedger = useCallback(async (cursor?: string) => {
    if (!session) return;
    const current = session;
    setLedger(previous => ({ ...previous, phase: 'loading', message: '', ...(cursor ? {} : { items: [], cursor: null }) }));
    try {
      const result = await api.transactions(current, cursor);
      if (sessionRef.current?.access_token !== current.access_token) return;
      setLedger(previous => ({ phase: 'ready', items: cursor ? [...previous.items, ...result.items] : result.items, cursor: result.next_cursor || null, message: '' }));
    } catch (reason) {
      if (reason instanceof ApiError && reason.status === 401) { expire(); return; }
      if (reason instanceof ApiError && reason.code === 'PASSWORD_CHANGE_REQUIRED') { setForced(true); return; }
      if (sessionRef.current?.access_token !== current.access_token) return;
      setLedger(previous => ({ ...previous, phase: 'error', message: messageFor(reason) }));
    }
  }, [session, expire]);

  const loadMeals = useCallback(async () => {
    if (!session) return;
    const current = session;
    setMeals(previous => ({ ...previous, phase: 'loading', message: '' }));
    try {
      const result = await api.mealPeriods(current);
      if (sessionRef.current?.access_token !== current.access_token) return;
      setMeals({ phase: 'ready', items: result.meal_periods, message: '' });
    } catch (reason) {
      if (reason instanceof ApiError && reason.status === 401) { expire(); return; }
      if (reason instanceof ApiError && reason.code === 'PASSWORD_CHANGE_REQUIRED') { setForced(true); return; }
      if (sessionRef.current?.access_token !== current.access_token) return;
      setMeals(previous => ({ ...previous, phase: 'error', message: messageFor(reason) }));
    }
  }, [session, expire]);

  useEffect(() => { if (session && employee && !forced) void loadLedger(); }, [session?.access_token, employee?.id, forced]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (session && employee && !forced) void loadMeals(); }, [session?.access_token, employee?.id, forced]); // eslint-disable-line react-hooks/exhaustive-deps

  const changePage = (next: Page) => {
    setPage(next);
    if (next === 'home' && session) { void loadProfile(session); void loadLedger(); void loadMeals(); }
    else if (next === 'ledger') void loadLedger();
  };
  const logout = async () => {
    if (!session) return;
    setLogoutBusy(true);
    try { await api.logout(session); } catch { /* Clear local session even if the connection fails. */ }
    finally { setLogoutBusy(false); expire(); }
  };

  if (!session) return <Login onLogin={next => { setSession(next); setBooting(true); setForced(next.must_change_password); }}/ >;
  if (booting) return <main className="simple-page"><header className="simple-header"><Brand/></header><Loading text="正在读取账户信息"/></main>;
  if (bootError) return <main className="simple-page"><header className="simple-header"><Brand/></header><Notice title="账户信息暂不可用" action={<><button className="outline-button" type="button" onClick={() => void loadProfile(session)}>重试</button><button className="text-button" type="button" onClick={expire}>退出登录</button></>}>{bootError}</Notice></main>;
  if (forced || changing) return <PasswordChange session={session} forced={forced} onChanged={next => { setChanging(false); setForced(next.must_change_password); setSession(next); }} onCancel={() => { if (forced) void logout(); else setChanging(false); }} onExpired={expire}/>;
  if (!employee || !account) return <main className="simple-page"><Loading text="正在读取账户信息"/></main>;

  return <div className="app-shell"><main className="app-main">
    {page === 'home' && <Home employee={employee} balance={account.balance} ledger={ledger} meals={meals} setPage={changePage} refresh={() => void loadLedger()} refreshMeals={() => void loadMeals()} onRechargeInfo={() => setRechargeInfo(true)}/>}
    {page === 'qr' && <PaymentCode session={session} employee={employee} onExpired={expire} onForced={() => setForced(true)}/>}
    {page === 'ledger' && <Ledger ledger={ledger} balance={account.balance} refresh={() => void loadLedger()} loadMore={() => { if (ledger.cursor) void loadLedger(ledger.cursor); }}/ >}
    {page === 'profile' && <Profile employee={employee} onChangePassword={() => setChanging(true)} onLogout={() => void logout()} busy={logoutBusy}/>}
  </main><BottomNav page={page} setPage={changePage}/>
    {rechargeInfo && <div className="modal-backdrop" onClick={() => setRechargeInfo(false)}><section className="modal" role="dialog" aria-modal="true" aria-labelledby="recharge-title" onClick={event => event.stopPropagation()}><span className="modal-icon"><Icon name="wallet" size={30}/></span><h2 id="recharge-title">线下充值</h2><p>请联系管理员，在线下完成现金或转账收款。管理员登记充值后，余额会更新，并在资金流水中留下记录。</p><button className="primary-button full" type="button" onClick={() => setRechargeInfo(false)}>知道了</button></section></div>}
  </div>;
}
