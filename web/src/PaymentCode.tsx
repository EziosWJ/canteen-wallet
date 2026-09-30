import { useCallback, useEffect, useRef, useState } from 'react';
import QRCode from 'qrcode';
import { api, ApiError, clearPaymentCache, paymentCacheKey, readPaymentCache, savePaymentCache,
  type Employee, type PaymentPresentation, type PaymentToken, type Session } from './api';
import { Icon } from './icons';

function money(cents = 0): string { return `¥${(cents / 100).toFixed(2)}`; }
function dateTime(value?: string): string {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date);
}
function errorMessage(error: unknown): string {
  if (!(error instanceof ApiError)) return '请求失败，请稍后重试。';
  if (error.code === 'NETWORK') return '手机暂时无法连接服务；当前就餐码在到期前仍可出示。';
  if (error.code === 'REFRESH_TOO_SOON') return '正在等待下一个就餐码。';
  if (error.code === 'ENTRANCE_DISABLED') return '食堂已关闭就餐码消费，请改用其他消费模式。';
  return error.message;
}

/** An entrance an administrator closed mid-session is a final state, not a
 * transient failure: retrying would only ask the server to refuse again. */
function entranceClosed(error: unknown): boolean {
  return error instanceof ApiError && error.code === 'ENTRANCE_DISABLED';
}

export default function PaymentCode({ session, employee, onExpired, onForced }: {
  session: Session; employee: Employee; onExpired: () => void; onForced: () => void;
}) {
  const [initial] = useState(() => readPaymentCache(session));
  const [token, setToken] = useState<PaymentToken | null>(initial?.token || null);
  const [presentationId, setPresentationId] = useState(initial?.presentation_id || '');
  const [status, setStatus] = useState<PaymentPresentation | null>(initial?.status || null);
  const [offsetMs, setOffsetMs] = useState(initial?.offset_ms || 0);
  const [qrData, setQrData] = useState('');
  const [message, setMessage] = useState('');
  const [closed, setClosed] = useState(false);
  const closedRef = useRef(false);
  const [now, setNow] = useState(Date.now());
  const [deciding, setDeciding] = useState(false);
  const active = useRef(false);
  const tokenRef = useRef(token);
  const idRef = useRef(presentationId);
  const statusRef = useRef(status);
  const offsetRef = useRef(offsetMs);
  const issueBusy = useRef(false);
  const statusBusy = useRef(false);
  const retryAt = useRef(0);
  const lastStatusAt = useRef(0);
  const flowVersion = useRef(0);
  const initialized = useRef(!!initial?.presentation_id);
  tokenRef.current = token;
  idRef.current = presentationId;
  statusRef.current = status;
  offsetRef.current = offsetMs;

  const apply = useCallback((nextToken: PaymentToken | null, nextId: string,
    nextStatus: PaymentPresentation | null, nextOffset: number, persist = true) => {
    if (nextId !== idRef.current) flowVersion.current++;
    tokenRef.current = nextToken; idRef.current = nextId; statusRef.current = nextStatus; offsetRef.current = nextOffset;
    setToken(nextToken); setPresentationId(nextId); setStatus(nextStatus); setOffsetMs(nextOffset);
    if (persist && nextId) savePaymentCache(session, {
      presentation_id: nextId, token: nextToken, offset_ms: nextOffset, status: nextStatus,
    });
  }, [session]);

  const issue = useCallback(async (nextId?: string) => {
    if (issueBusy.current || !active.current) return;
    issueBusy.current = true;
    const requestedVersion = flowVersion.current;
    try {
      const issued = await api.paymentToken(session, nextId);
      if (!active.current || requestedVersion !== flowVersion.current ||
        (nextId && idRef.current && idRef.current !== nextId) ||
        (statusRef.current && statusRef.current.state !== 'WAITING')) return;
      apply(issued, issued.presentation_id, null, Date.parse(issued.server_time) - Date.now());
      retryAt.current = 0; setMessage('');
    } catch (reason) {
      if (!active.current) return;
      if (reason instanceof ApiError && reason.status === 401) { onExpired(); return; }
      if (reason instanceof ApiError && reason.code === 'PASSWORD_CHANGE_REQUIRED') { onForced(); return; }
      if (entranceClosed(reason)) {
        // The entrance was switched off while this page was open. Stop asking
        // for codes and tell the employee why; the server refuses every retry,
        // so retrying on the timer would only repeat the same refusal.
        clearPaymentCache(session);
        apply(null, '', null, offsetRef.current);
        closedRef.current = true; setClosed(true); setMessage(errorMessage(reason));
        return;
      }
      if (reason instanceof ApiError && reason.code === 'REFRESH_TOO_SOON' && reason.presentationId && !idRef.current) {
        apply(null, reason.presentationId, null, offsetRef.current);
      }
      retryAt.current = reason instanceof ApiError && reason.code === 'REFRESH_TOO_SOON' && reason.refreshAfter
        ? Date.parse(reason.refreshAfter) - offsetRef.current : Date.now() + 5000;
      setMessage(errorMessage(reason));
    } finally { issueBusy.current = false; }
  }, [session, apply, onExpired, onForced]);

  const syncStatus = useCallback(async () => {
    if (statusBusy.current || !active.current || document.visibilityState !== 'visible' ||
      (issueBusy.current && !tokenRef.current)) return;
    statusBusy.current = true;
    lastStatusAt.current = Date.now();
    const requestedId = idRef.current;
    const requestedVersion = flowVersion.current;
    try {
      const current = await api.paymentPresentation(session);
      if (!active.current || requestedVersion !== flowVersion.current) return;
      const nextOffset = Date.parse(current.server_time) - Date.now();
      const currentToken = tokenRef.current?.presentation_id === current.presentation_id ? tokenRef.current : null;
      apply(currentToken, current.presentation_id, current, nextOffset);
      setMessage('');
      if (current.state === 'WAITING' && (!currentToken || Date.parse(currentToken.expires_at) <= Date.now() + nextOffset)) {
        void issue(current.presentation_id);
      }
    } catch (reason) {
      if (!active.current) return;
      if (reason instanceof ApiError && reason.status === 401) { onExpired(); return; }
      if (reason instanceof ApiError && reason.status === 404) {
        if (requestedId) { clearPaymentCache(session); apply(null, '', null, offsetRef.current); }
        void issue(); return;
      }
      if (reason instanceof ApiError && reason.code === 'PASSWORD_CHANGE_REQUIRED') { onForced(); return; }
      setMessage(errorMessage(reason));
    } finally { initialized.current = true; statusBusy.current = false; }
  }, [session, apply, issue, onExpired, onForced]);

  useEffect(() => {
    active.current = true;
    void syncStatus();
    const timer = window.setInterval(() => {
      const tick = Date.now(); setNow(tick);
      if (document.visibilityState !== 'visible' || closedRef.current) return;
      if (statusRef.current && (statusRef.current.state === 'SUCCESS' || statusRef.current.state === 'FAILED')) return;
      if (tick - lastStatusAt.current >= 2000) void syncStatus();
      if (!initialized.current) return;
      if (statusRef.current && statusRef.current.state !== 'WAITING') return;
      const currentToken = tokenRef.current;
      const target = currentToken ? Date.parse(currentToken.refresh_after) - offsetRef.current : 0;
      if (tick >= Math.max(target, retryAt.current) && !issueBusy.current) void issue(idRef.current || undefined);
    }, 1000);
    const visible = () => {
      if (document.visibilityState === 'visible' && statusRef.current?.state !== 'SUCCESS' && statusRef.current?.state !== 'FAILED') void syncStatus();
    };
    const storage = (event: StorageEvent) => {
      if (event.key !== paymentCacheKey(session) || !event.newValue) return;
      const shared = readPaymentCache(session);
      if (shared) apply(shared.token, shared.presentation_id, shared.status, shared.offset_ms, false);
    };
    document.addEventListener('visibilitychange', visible);
    window.addEventListener('storage', storage);
    return () => {
      active.current = false;
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', visible);
      window.removeEventListener('storage', storage);
    };
  }, [session, apply, issue, syncStatus]);

  useEffect(() => {
    let cancelled = false;
    setQrData('');
    if (token) void QRCode.toDataURL(token.token, {
      errorCorrectionLevel: 'M', margin: 2, width: 320, color: { dark: '#17324D', light: '#FFFFFF' },
    }).then(data => { if (!cancelled) setQrData(data); });
    return () => { cancelled = true; };
  }, [token?.token]);

  const startAgain = () => {
    const nextId = `prs_${crypto.randomUUID().replaceAll('-', '')}`;
    apply(null, nextId, null, offsetRef.current);
    setMessage(''); retryAt.current = 0;
    void issue(nextId);
  };
  const decide = async (decision: 'confirm' | 'cancel') => {
    const pendingId = statusRef.current?.result?.pending_id;
    if (!pendingId || deciding) return;
    setDeciding(true);
    try {
      const result = await api.decidePending(session, pendingId, decision);
      apply(tokenRef.current, idRef.current, {
        presentation_id: idRef.current, state: result.status, server_time: new Date().toISOString(), result,
      }, offsetRef.current);
      setMessage('');
    } catch (reason) {
      if (reason instanceof ApiError && reason.status === 401) onExpired();
      else setMessage(errorMessage(reason));
      void syncStatus();
    } finally { setDeciding(false); }
  };

  const validToken = token && now + offsetMs < Date.parse(token.expires_at) - 1000;
  const seconds = token ? Math.max(0, Math.ceil((Date.parse(token.refresh_after) - now - offsetMs) / 1000)) : 0;
  const result = status?.result;
  const state = status?.state;

  if (closed) return <div className="page qr-page"><header className="page-heading"><h1>我的就餐码</h1><p>在食堂终端前出示动态码</p></header>
    <section className="surface code-card"><div className="payment-outcome"><span className="notice-symbol"><Icon name="alert" size={27}/></span><h2>就餐码消费已关闭</h2><p>{message || '食堂已关闭就餐码消费，请改用其他消费模式。'}</p><button className="outline-button" type="button" onClick={() => { closedRef.current = false; setClosed(false); initialized.current = true; void syncStatus(); }}>重新检查</button></div></section>
  </div>;

  return <div className="page qr-page"><header className="page-heading"><h1>我的就餐码</h1><p>在食堂终端前出示动态码</p></header>
    <section className="surface code-card"><div className="code-top"><span>{employee.account_status === 'ACTIVE' ? '账户正常' : '账户暂不可用'}</span><span>{state === 'PENDING' ? '等待本人确认' : state === 'SUCCESS' ? '消费已完成' : '动态更新'}</span></div>
      {state === 'SUCCESS' && result ? <div className="payment-outcome"><img src="/assets/ui/success-illustration.png" alt=""/><h2>消费成功</h2><strong className="payment-amount">{money(result.amount_cents)}</strong><dl><div><dt>餐次</dt><dd>{result.meal_name || result.meal_code || '—'}</dd></div><div><dt>时间</dt><dd>{dateTime(result.occurred_at)}</dd></div><div><dt>消费编号</dt><dd>{result.consumption_no || result.transaction_id || '—'}</dd></div></dl><button className="primary-button full" type="button" onClick={startAgain}>再次出示就餐码</button></div>
      : state === 'PENDING' && result ? <div className="payment-outcome"><h2>确认再次消费</h2><p>本餐次已有一笔消费，请确认是否再次扣款。</p><strong className="payment-amount">{money(result.amount_cents)}</strong><p>{result.meal_name || result.meal_code} · 请在 {dateTime(result.expires_at)} 前确认</p><div className="payment-actions"><button className="outline-button" type="button" onClick={() => void decide('cancel')} disabled={deciding}>取消</button><button className="primary-button" type="button" onClick={() => void decide('confirm')} disabled={deciding || (!!result.expires_at && now + offsetMs >= Date.parse(result.expires_at))}>{deciding ? '正在处理…' : '确认扣款'}</button></div></div>
      : state === 'FAILED' && result ? <div className="payment-outcome"><h2>本次消费未完成</h2><p>{result.message || result.code}</p><button className="primary-button full" type="button" onClick={startAgain}>重新出示就餐码</button></div>
      : validToken && qrData ? <><img className="real-qr" src={qrData} alt="可供食堂扫码枪读取的动态就餐码"/><h2>{employee.name}</h2><p>请将屏幕对准扫码枪</p><div className="refresh-line"><span>{seconds > 0 ? `${seconds} 秒后更新` : '正在更新'}</span><button type="button" onClick={() => void issue(idRef.current || undefined)} disabled={seconds > 0 || issueBusy.current}>刷新</button></div></>
      : message && retryAt.current > now ? <div className="payment-outcome"><h2>暂时无法获取就餐码</h2><p>{message}</p><button className="outline-button" type="button" onClick={() => void syncStatus()}>重试</button></div>
      : <div className="state-box" role="status"><span className="loading-ring"/><p>正在获取就餐码</p></div>}
      {message && validToken && state !== 'SUCCESS' && state !== 'FAILED' && <div className="alert alert-warn" role="status">{message}</div>}
    </section>
    <section className="surface qr-help"><img src="/assets/ui/phone-qr-illustration.png" alt=""/><div><h2>使用就餐码</h2><p>打开手机屏幕，让食堂扫码枪读取上方动态码。核销结果会自动显示在此页。</p></div></section>
  </div>;
}
