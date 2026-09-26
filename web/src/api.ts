export type Session = {
  access_token: string;
  token_type: 'Bearer';
  expires_at: string;
  employee_id: number;
  must_change_password: boolean;
};

export type Employee = {
  id: number;
  employee_no: string;
  name: string;
  phone: string;
  department: string;
  photo_url: string | null;
  status: string;
  account_status: string;
  balance: number;
  must_change_password: boolean;
};

export type PasswordGate = { id: number; must_change_password: true };

export type PaymentToken = {
  token: string;
  presentation_id: string;
  server_time: string;
  refresh_after: string;
  expires_at: string;
};

export type ConsumptionResult = {
  status: 'SUCCESS' | 'PENDING' | 'FAILED';
  code: string;
  message: string;
  employee_name?: string;
  meal_code?: string;
  meal_name?: string;
  amount_cents?: number;
  transaction_id?: number;
  transaction_no?: string;
  consumption_no?: string;
  occurred_at?: string;
  pending_id?: string;
  expires_at?: string;
};

export type PaymentPresentation = {
  presentation_id: string;
  state: 'WAITING' | 'PENDING' | 'SUCCESS' | 'FAILED';
  server_time: string;
  result?: ConsumptionResult;
};

export type Transaction = {
  id: number;
  transaction_no: string;
  type: string;
  amount_cents: number;
  balance_after_cents: number;
  created_at: string;
  related_transaction_id?: number | null;
};

export type TransactionPage = { items: Transaction[]; next_cursor: string | null };

export type MealPeriod = {
  code: string;
  name: string;
  start_time: string;
  end_time: string;
  price_cents: number;
  enabled: boolean;
};

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string,
    public refreshAfter?: string, public presentationId?: string) {
    super(message);
  }
}

const SESSION_KEY = 'canteen-employee-session';

export function readSession(): Session | null {
  try {
    const raw = sessionStorage.getItem(SESSION_KEY);
    if (!raw) return null;
    const session = JSON.parse(raw) as Session;
    if (!session.access_token || Date.parse(session.expires_at) <= Date.now()) {
      clearSession();
      return null;
    }
    return session;
  } catch {
    clearSession();
    return null;
  }
}

export function saveSession(session: Session): void {
  const previous = readStoredSession();
  if (previous && previous.access_token !== session.access_token) clearPaymentCache(previous);
  sessionStorage.setItem(SESSION_KEY, JSON.stringify(session));
}

export function clearSession(): void {
  const session = readStoredSession();
  if (session) clearPaymentCache(session);
  sessionStorage.removeItem(SESSION_KEY);
}

function readStoredSession(): Session | null {
  try {
    const raw = sessionStorage.getItem(SESSION_KEY);
    return raw ? JSON.parse(raw) as Session : null;
  } catch { return null; }
}

export type PaymentCache = {
  session_marker: string;
  presentation_id: string;
  token: PaymentToken | null;
  offset_ms: number;
  status: PaymentPresentation | null;
};

export function paymentCacheKey(session: Session): string { return `canteen-payment-${session.employee_id}`; }

export function readPaymentCache(session: Session): PaymentCache | null {
  try {
    let raw: string | null = null;
    try { raw = localStorage.getItem(paymentCacheKey(session)); } catch { /* Use tab storage below. */ }
    raw ||= sessionStorage.getItem(paymentCacheKey(session));
    if (!raw) return null;
    const cache = JSON.parse(raw) as PaymentCache;
    return cache.session_marker === session.access_token.slice(-12) ? cache : null;
  } catch { return null; }
}

export function savePaymentCache(session: Session, cache: Omit<PaymentCache, 'session_marker'>): void {
  const encoded = JSON.stringify({ ...cache, session_marker: session.access_token.slice(-12) });
  try { localStorage.setItem(paymentCacheKey(session), encoded); }
  catch { try { sessionStorage.setItem(paymentCacheKey(session), encoded); } catch { /* Continue without persistence. */ } }
}

export function clearPaymentCache(session: Session): void {
  try { localStorage.removeItem(paymentCacheKey(session)); } catch { /* Storage can be disabled. */ }
  try { sessionStorage.removeItem(paymentCacheKey(session)); } catch { /* Storage can be disabled. */ }
}

async function request<T>(path: string, options: RequestInit = {}, session: Session | null = readSession()): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, {
      ...options,
      headers: {
        Accept: 'application/json',
        ...(options.body ? { 'Content-Type': 'application/json' } : {}),
        ...(session ? { Authorization: `Bearer ${session.access_token}` } : {}),
        ...options.headers,
      },
      cache: 'no-store',
    });
  } catch {
    throw new ApiError(0, 'NETWORK', '无法连接服务，请检查网络后重试。');
  }
  if (!response.ok) {
    let body: { code?: string; message?: string; refresh_after?: string; presentation_id?: string } = {};
    try { body = await response.json(); } catch { /* Empty or non-JSON error body. */ }
    throw new ApiError(response.status, body.code || 'HTTP_ERROR', body.message || '请求失败，请稍后重试。', body.refresh_after, body.presentation_id);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export const api = {
  login(phone: string, password: string) {
    return request<Session>('/api/auth/login', { method: 'POST', body: JSON.stringify({ phone, password }) }, null);
  },
  logout(session: Session) {
    return request<void>('/api/auth/logout', { method: 'POST' }, session);
  },
  me(session: Session) {
    return request<Employee | PasswordGate>('/api/me', {}, session);
  },
  account(session: Session) {
    return request<{ balance: number; status: string }>('/api/me/account', {}, session);
  },
  changePassword(session: Session, oldPassword: string, newPassword: string) {
    return request<Session>('/api/me/change-password', {
      method: 'POST', body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }),
    }, session);
  },
  paymentToken(session: Session, presentationId?: string) {
    return request<PaymentToken>('/api/me/payment-token', {
      method: 'POST', ...(presentationId ? { body: JSON.stringify({ presentation_id: presentationId }) } : {}),
    }, session);
  },
  paymentPresentation(session: Session, presentationId?: string) {
    const query = presentationId ? `?id=${encodeURIComponent(presentationId)}` : '';
    return request<PaymentPresentation>(`/api/me/payment-presentation${query}`, {}, session);
  },
  decidePending(session: Session, pendingId: string, decision: 'confirm' | 'cancel') {
    return request<ConsumptionResult>(`/api/me/pending-consumptions/${encodeURIComponent(pendingId)}/${decision}`, { method: 'POST' }, session);
  },
  transactions(session: Session, cursor?: string) {
    const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : '';
    return request<TransactionPage>(`/api/me/transactions${query}`, {}, session);
  },
  mealPeriods(session: Session) {
    return request<{ meal_periods: MealPeriod[] }>('/api/me/meal-periods', {}, session);
  },
};
