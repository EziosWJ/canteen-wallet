import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError, clearLastSelfServiceIntent, readLastSelfServiceIntent, saveLastSelfServiceIntent,
  type ConsumptionResult, type Employee, type SelfServiceOutcome, type SelfServicePreview, type Session } from './api';
import { messageFor } from './Auth';

const A = '/assets/ui/';

function money(cents = 0): string { return `¥${(cents / 100).toFixed(2)}`; }
function dateTime(value?: string): string {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date);
}

/** The existing-consumption reminder. It must report the exact count: telling a
 * third consumption that it is the second would be misleading. */
function existingLabel(count: number): string | null {
  if (count < 1) return null;
  return `本餐次已消费 ${count} 次，继续消费将再扣一笔。`;
}

type Phase = 'loading' | 'ready' | 'unavailable' | 'error';

function Loading({ text }: { text: string }) {
  return <div className="state-box" role="status"><span className="loading-ring"/><p>{text}</p></div>;
}

export default function SelfService({ session, employee, onExpired, onForced }: {
  session: Session; employee: Employee; onExpired: () => void; onForced: () => void;
}) {
  const [phase, setPhase] = useState<Phase>('loading');
  const [preview, setPreview] = useState<SelfServicePreview | null>(null);
  const [consumption, setConsumption] = useState<ConsumptionResult | null>(null);
  const [message, setMessage] = useState('');
  const [notice, setNotice] = useState('');
  // The server reports why nothing can be charged through the payload, so the
  // reason is kept as the server's own code rather than guessed locally: a
  // switched-off entrance, a suspended canteen and an empty window all need
  // different words.
  const [unavailableCode, setUnavailableCode] = useState('');
  const closed = unavailableCode === 'MODE_DISABLED';
  const suspended = unavailableCode === 'MEALS_SUSPENDED';
  const [busy, setBusy] = useState(false);
  // The confirmation in flight is tracked per intent so a double tap or a retry
  // cannot fire two charges, and a refresh can recover the same result.
  const confirming = useRef<string | null>(null);
  const started = useRef(false);

  const handle = useCallback((reason: unknown) => {
    if (reason instanceof ApiError && reason.status === 401) { onExpired(); return; }
    if (reason instanceof ApiError && reason.code === 'PASSWORD_CHANGE_REQUIRED') { onForced(); return; }
    setPhase('error'); setMessage(messageFor(reason));
  }, [onExpired, onForced]);

  // Entering the page is one explicit server operation. It cancels this
  // employee's own waiting scan requests before showing the preview.
  const enter = useCallback(async () => {
    setBusy(true); setPhase('loading'); setMessage('');
    try {
      const current = await api.openSelfService(session);
      if (current.intent_id) saveLastSelfServiceIntent(session, current.intent_id);
      setPreview(current);
      setUnavailableCode(current.code || '');
      setMessage(current.message || '');
      setPhase(current.status === 'READY' ? 'ready' : 'unavailable');
    } catch (reason) { handle(reason); }
    finally { setBusy(false); }
  }, [session, handle]);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    // A refresh on the success page must recover the same consumption rather
    // than offering a new charge.
    const remembered = readLastSelfServiceIntent(session);
    if (remembered) {
      void api.selfServiceResult(session, remembered).then(outcome => {
        if (outcome.status === 'SUCCESS' && outcome.consumption) {
          setConsumption(outcome.consumption); setPhase('ready');
        } else if (outcome.status === 'REJECTED') {
          setUnavailableCode(outcome.code || '');
          setPhase('unavailable'); setMessage(outcome.message || '消费未完成。');
        } else {
          void enter();
        }
      }).catch(reason => {
        if (reason instanceof ApiError && reason.status === 404) { clearLastSelfServiceIntent(session); void enter(); return; }
        handle(reason);
      });
    } else void enter();
  }, [enter, session, handle]);

  const confirm = useCallback(async (intentId: string) => {
    if (!intentId || confirming.current === intentId) return;
    confirming.current = intentId;
    setBusy(true); setNotice('');
    try {
      const outcome: SelfServiceOutcome = await api.confirmSelfService(session, intentId);
      if (outcome.status === 'SUCCESS' && outcome.consumption) {
        setConsumption(outcome.consumption); setPhase('ready'); setPreview(null);
        saveLastSelfServiceIntent(session, intentId);
      } else if (outcome.status === 'CONFIRM_REQUIRED' && outcome.preview) {
        // The meal, price or count changed since the preview: show the updated
        // preview and let the employee decide again at the new values.
        setPreview(outcome.preview);
        setUnavailableCode(outcome.preview.code || '');
        if (outcome.preview.status !== 'READY') setMessage(outcome.preview.message || '');
        setPhase(outcome.preview.status === 'READY' ? 'ready' : 'unavailable');
        setNotice(outcome.message || '餐次信息已更新，请确认后再消费。');
      } else {
        setUnavailableCode(outcome.code || '');
        setPhase('unavailable'); setMessage(outcome.message || '消费未完成。');
      }
    } catch (reason) { handle(reason); }
    finally { setBusy(false); confirming.current = null; }
  }, [session, handle]);

  // "Consume again" is the only path to the next charge: it asks the server for a
  // fresh preview instead of reusing the completed one.
  const consumeAgain = () => { clearLastSelfServiceIntent(session); setConsumption(null); setNotice(''); void enter(); };

  const existing = preview ? existingLabel(preview.existing_count) : null;

  return <div className="page self-service-page">
    <header className="page-heading"><h1>自助消费</h1><p>在当前餐次从储值账户扣款</p></header>
    <section className="surface code-card">
      {consumption ? <SuccessPanel result={consumption} onAgain={consumeAgain}/>
        : phase === 'loading' ? <Loading text="正在读取本餐次信息"/>
        : phase === 'error' ? <div className="payment-outcome"><h2>暂时无法读取餐次</h2><p>{message}</p><button className="outline-button" type="button" onClick={() => void enter()} disabled={busy}>重试</button></div>
        : phase === 'unavailable' ? <div className="payment-outcome"><h2>{closed ? '自助消费已关闭' : suspended ? '食堂当前未开放供餐' : '当前无法自助消费'}</h2><p>{message || '当前不在用餐时段。'}</p>{closed ? <p className="subtle">食堂已关闭自助消费，请改用出示就餐码。关闭前已完成的消费和退款记录仍然可以查询。</p> : null}{suspended ? <p className="subtle">所有餐次都已暂停供应，请联系管理员开放后再来。</p> : null}<button className="outline-button full" type="button" onClick={() => void enter()} disabled={busy}>重新检查</button></div>
        : preview ? <PreviewPanel employee={employee} preview={preview} existing={existing} notice={notice} busy={busy} onConfirm={() => void confirm(preview.intent_id || '')}/>
        : <Loading text="正在读取本餐次信息"/>}
    </section>
    {!consumption && <section className="surface qr-help"><img src={`${A}phone-qr-illustration.png`} alt="自助消费说明插画"/><div><h2>自助消费说明</h2><p>进入本页会取消你尚未确认的扫码请求。只有点击“确认扣款”才会从储值账户扣除餐费，查看页面不会扣款。</p></div></section>}
  </div>;
}

function PreviewPanel({ employee, preview, existing, notice, busy, onConfirm }: {
  employee: Employee; preview: SelfServicePreview; existing: string | null; notice: string; busy: boolean; onConfirm: () => void;
}) {
  return <div className="payment-outcome">
    <h2>{preview.meal_name || preview.meal_code || '当前餐次'}</h2>
    <strong className="payment-amount">{money(preview.amount_cents)}</strong>
    <dl>
      <div><dt>用餐人</dt><dd>{employee.name}</dd></div>
      <div><dt>业务日期</dt><dd>{preview.business_date || '—'}</dd></div>
      <div><dt>本餐次已消费</dt><dd>{preview.existing_count} 次</dd></div>
    </dl>
    {existing && <p role="status">{existing}</p>}
    {notice && <div className="alert alert-warn" role="status">{notice}</div>}
    <button className="primary-button full" type="button" onClick={onConfirm} disabled={busy || !preview.intent_id}>{busy ? '正在处理…' : '确认扣款'}</button>
  </div>;
}

function SuccessPanel({ result, onAgain }: { result: ConsumptionResult; onAgain: () => void }) {
  return <div className="payment-outcome">
    <img src={`${A}success-illustration.png`} alt=""/>
    <h2>消费成功</h2>
    <strong className="payment-amount">{money(result.amount_cents)}</strong>
    <dl>
      <div><dt>餐次</dt><dd>{result.meal_name || result.meal_code || '—'}</dd></div>
      <div><dt>时间</dt><dd>{dateTime(result.occurred_at)}</dd></div>
      <div><dt>消费编号</dt><dd>{result.consumption_no || result.transaction_id || '—'}</dd></div>
    </dl>
    <button className="primary-button full" type="button" onClick={onAgain}>再次消费</button>
  </div>;
}
