import { useCallback, useEffect, useState } from 'react';
import './terminal.css';

type TerminalEvent = {
  id?: number | string;
  result_code?: string;
  status?: string;
  code?: string;
  employee_name?: string;
  employee_no?: string;
  meal_name?: string;
  meal_code?: string;
  amount_cents?: number;
  occurred_at?: string;
  created_at?: string;
  transaction_no?: string;
  consumption_no?: string;
  pending_id?: string | number;
  message?: string;
  replayed?: boolean;
  duplicate_trigger?: boolean;
};
type DisplayState = {
  status?: 'WAITING' | 'SUCCESS' | 'FAILED' | 'PENDING' | 'OFFLINE';
  result?: TerminalEvent | null;
  scanner_status?: string;
  voice_status?: string;
  events?: TerminalEvent[];
};

function money(cents?: number): string { return typeof cents === 'number' ? `¥${(cents / 100).toFixed(2)}` : '—'; }
function time(value?: string): string {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }).format(date);
}
function stateLabel(code?: string): { title: string; detail: string; kind: string } {
  switch (code) {
    case 'SUCCESS': case 'CONSUMED': return { title: '核销成功', detail: '本次就餐已完成', kind: 'success' };
    case 'DUPLICATE_MEAL_CONFIRM_REQUIRED': return { title: '同餐次再次消费', detail: '请核对信息并确认继续', kind: 'confirm' };
    case 'PENDING': case 'CONFIRM_REQUIRED': return { title: '等待员工确认', detail: '请员工在手机上确认再次消费', kind: 'confirm' };
    case 'INSUFFICIENT_BALANCE': case 'INSUFFICIENT_FUNDS': return { title: '余额不足', detail: '请联系管理员处理', kind: 'failure' };
    case 'CANCELLED': return { title: '员工已取消', detail: '本次未扣款', kind: 'failure' };
    case 'MEAL_ENDED': case 'PENDING_EXPIRED': return { title: '确认已失效', detail: '本次未扣款，请重新扫码', kind: 'failure' };
    case 'USER_FROZEN': case 'ACCOUNT_FROZEN': case 'ACCOUNT_UNAVAILABLE': return { title: '账户暂不可用', detail: '请联系管理员处理', kind: 'failure' };
    case 'NO_ACTIVE_MEAL': case 'NO_MEAL': return { title: '当前时段未开放', detail: '请确认当前供餐安排', kind: 'failure' };
    case 'INVALID_TOKEN': case 'EXPIRED_TOKEN': case 'INVALID_SESSION': case 'TOKEN_INVALID': case 'TOKEN_UNAVAILABLE': case 'SUPERSEDED': return { title: '就餐码无效或已过期', detail: '请员工刷新就餐码后重试', kind: 'failure' };
    case 'TERMINAL_DISABLED': case 'DEVICE_OFFLINE': case 'BACKEND_UNAVAILABLE': return { title: '终端暂不可用', detail: '请联系现场管理员', kind: 'failure' };
    case 'FAILED': return { title: '核销未完成', detail: '请按提示处理后重试', kind: 'failure' };
    case 'OFFLINE': return { title: '终端离线', detail: '请联系现场管理员', kind: 'failure' };
    default: return { title: '等待扫码', detail: '请出示手机中的动态就餐码', kind: 'waiting' };
  }
}

export default function Terminal() {
  const [data, setData] = useState<DisplayState | null>(null);
  const [online, setOnline] = useState(false);
  const [error, setError] = useState('');
  const refresh = useCallback(async () => {
    try {
      const response = await fetch('/api/display/state', { headers: { Accept: 'application/json' }, cache: 'no-store' });
      if (!response.ok) throw new Error('终端服务暂不可用');
      setData(await response.json() as DisplayState); setOnline(true); setError('');
    } catch {
      setOnline(false); setData(null); setError('与本地核销服务连接中断，请检查 Scan Agent。');
    }
  }, []);
  useEffect(() => { void refresh(); const timer = window.setInterval(() => void refresh(), 1000); return () => window.clearInterval(timer); }, [refresh]);
  const event = data?.result || null;
  const state = stateLabel(event?.code || event?.status || data?.status);
  const isWaiting = !event || data?.status === 'WAITING';
  const isConfirm = data?.status === 'PENDING' || state.kind === 'confirm';
  const recent = data?.events || [];
  return <main className="terminal-app">
    <header className="terminal-head"><div className="terminal-brand"><img src="/assets/ui/app-logo.png" alt="食堂储值卡"/><span><b>食堂储值卡</b><small>核销终端</small></span></div><div className="terminal-meta"><span className={`terminal-pill ${online ? 'on' : 'off'}`}><i/>服务{online ? '在线' : '离线'}</span><span>食堂核销终端</span><span className={`terminal-pill ${data?.scanner_status === 'READY' ? 'on' : 'off'}`}><i/>扫码枪 {data?.scanner_status === 'READY' ? '在线' : '离线'}</span></div></header>
    {error && <div className="terminal-connection" role="alert">{error}</div>}
    <div className={`terminal-grid ${isWaiting ? 'waiting-layout' : ''}`}>
      <section className={`terminal-result ${state.kind}`} aria-live="polite">
        <div className="terminal-result-top"><span className={`terminal-state-pill ${state.kind}`}>{isWaiting ? '等待扫码' : state.kind === 'confirm' ? '需要确认' : state.kind === 'success' ? '交易完成' : '未完成'}</span><time>{time(event?.occurred_at || event?.created_at)}</time></div>
        {isWaiting ? <div className="terminal-wait"><img src="/assets/ui/scan-frame.png" alt="扫码等待提示"/><div><h1>{!online ? '正在连接终端服务' : data?.status === 'OFFLINE' ? '终端设备离线' : '请出示动态就餐码'}</h1><p>{data?.status === 'OFFLINE' ? '请检查扫码枪和 Scan Agent 连接' : '由 USB 扫码枪读取手机屏幕上的就餐码'}</p></div></div> : <>
          <div className="terminal-status-icon">{state.kind === 'success' ? <img src="/assets/ui/success-illustration.png" alt=""/> : <span aria-hidden="true">{state.kind === 'confirm' ? '!' : '×'}</span>}</div>
          <h1>{state.title}</h1><p className="terminal-detail">{event?.duplicate_trigger ? '重复扫码，已返回首次结果' : event?.replayed ? '此码已处理，显示原结果' : event?.message || state.detail}</p>
          <div className="terminal-identity"><span className="terminal-avatar" aria-hidden="true">{event?.employee_name?.slice(0, 1) || '员'}</span><div><strong>{event?.employee_name || '员工身份待确认'}</strong><small>{event?.employee_no || '仅用于本次核销提示'}</small></div></div>
          <div className="terminal-facts"><div><small>本次金额</small><strong>{money(event?.amount_cents)}</strong></div><div><small>餐次</small><strong>{event?.meal_name || event?.meal_code || '—'}</strong></div><div><small>核销时间</small><strong>{time(event?.occurred_at || event?.created_at)}</strong></div></div>
          {isConfirm && <div className="terminal-confirm"><p>请员工在手机上确认本次再次消费；终端会自动显示最终结果。</p></div>}
        </>}
      </section>
      <aside className="terminal-side"><section className="terminal-device-card"><h2>设备状态</h2><div><span>Scan Agent</span><b className={online ? 'good' : 'bad'}>{online ? '在线' : '离线'}</b></div><div><span>扫码枪</span><b className={data?.scanner_status === 'READY' ? 'good' : 'bad'}>{data?.scanner_status === 'READY' ? '在线' : '离线'}</b></div><div><span>核销链路</span><b className={online && data?.status !== 'OFFLINE' ? 'good' : 'bad'}>{online && data?.status !== 'OFFLINE' ? '可用' : '不可用'}</b></div><div><span>语音模块</span><b className={data?.voice_status === 'READY' ? 'good' : 'bad'}>{data?.voice_status === 'READY' ? '在线' : data?.voice_status === 'FAILED' ? '异常' : '未连接'}</b></div></section>
        <section className="terminal-events"><header><h2>近期扫码事件</h2><span>仅显示必要信息</span></header>{recent.length ? <div className="terminal-event-list">{recent.slice(0, 6).map((item, index) => { const label = stateLabel(item.result_code || item.status); return <div className="terminal-event" key={item.id ?? index}><i className={label.kind}/><div><strong>{item.employee_name || label.title}</strong><small>{item.meal_name || item.result_code || item.status || '扫码事件'}</small></div><time>{time(item.occurred_at || item.created_at)}</time></div>; })}</div> : <div className="terminal-events-empty">扫码事件会在这里显示</div>}</section>
      </aside>
    </div>
    <footer className="terminal-footer"><span>请勿向员工或公共区域展示账户余额</span><span>终端状态每秒更新</span></footer>
  </main>;
}
