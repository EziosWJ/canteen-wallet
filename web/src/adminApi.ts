export type AdminSession = {
  access_token: string;
  token_type: 'Bearer';
  expires_at: string;
  administrator: { id: number; username: string };
};

export type AdminEmployee = {
  id: number;
  account_id: number;
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

export type AdminMeal = {
  code: string;
  name: string;
  start_time: string;
  end_time: string;
  price_cents: number;
  enabled: boolean;
};

/** Binding state of the signed-in administrator's authenticator. Binding is
 * optional: an unbound administrator signs in with the password alone. */
export type SecurityState = {
  second_factor_bound: boolean;
  enrollment_pending: boolean;
  enrollment_expires_at?: string | null;
};

/** A pending binding. The secret and URI are returned once, at the moment the
 * administrator asks for them, and are never readable again. */
export type SecondFactorEnrollment = {
  otpauth_uri: string;
  secret: string;
  expires_at: string;
  replacement: boolean;
};

export class AdminApiError extends Error {
  constructor(public status: number, public code: string, message: string) { super(message); }
}

const KEY = 'canteen-admin-session';

export function readAdminSession(): AdminSession | null {
  try {
    const value = sessionStorage.getItem(KEY);
    if (!value) return null;
    const session = JSON.parse(value) as AdminSession;
    if (!session.access_token || Date.parse(session.expires_at) <= Date.now()) {
      sessionStorage.removeItem(KEY);
      return null;
    }
    return session;
  } catch {
    sessionStorage.removeItem(KEY);
    return null;
  }
}

export function saveAdminSession(session: AdminSession | null): void {
  if (session) sessionStorage.setItem(KEY, JSON.stringify(session));
  else sessionStorage.removeItem(KEY);
}

async function request<T>(path: string, session: AdminSession | null, options: RequestInit = {}): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, {
      ...options,
      headers: {
        Accept: 'application/json',
        ...(options.body && !(options.body instanceof FormData) ? { 'Content-Type': 'application/json' } : {}),
        ...(session ? { Authorization: `Bearer ${session.access_token}` } : {}),
        ...options.headers,
      },
      cache: 'no-store',
    });
  } catch {
    throw new AdminApiError(0, 'NETWORK', '无法连接服务，请检查网络后重试。');
  }
  if (!response.ok) {
    let body: { code?: string; message?: string } = {};
    try { body = await response.json(); } catch { /* non-JSON response */ }
    throw new AdminApiError(response.status, body.code || 'HTTP_ERROR', body.message || '请求失败，请稍后重试。');
  }
  if (response.status === 204) return undefined as T;
  if (response.headers.get('content-type')?.includes('application/json')) return response.json() as Promise<T>;
  return response.blob() as Promise<T>;
}

export const adminApi = {
  login(username: string, password: string, secondFactorCode: string) {
    return request<AdminSession>('/api/admin/login', null, { method: 'POST', body: JSON.stringify({ username, password, second_factor_code: secondFactorCode }) });
  },
  me(session: AdminSession) {
    return request<{ id: number; username: string; second_factor_bound: boolean; enrollment_pending: boolean }>('/api/admin/me', session);
  },
  logout(session: AdminSession) { return request<void>('/api/admin/logout', session, { method: 'POST' }); },
  employees(session: AdminSession) { return request<{ employees: AdminEmployee[] }>('/api/admin/employees', session); },
  createEmployee(session: AdminSession, input: Pick<AdminEmployee, 'employee_no' | 'name' | 'phone' | 'department'>) {
    return request<{ employee: AdminEmployee; temporary_password: string }>('/api/admin/employees', session, { method: 'POST', body: JSON.stringify({ ...input, status: 'ACTIVE' }) });
  },
  updateEmployee(session: AdminSession, id: number, input: Pick<AdminEmployee, 'employee_no' | 'name' | 'phone' | 'department' | 'photo_url'>) {
    return request<AdminEmployee>(`/api/admin/employees/${id}`, session, { method: 'PATCH', body: JSON.stringify(input) });
  },
  setEmployeeStatus(session: AdminSession, id: number, status: string) {
    return request<AdminEmployee>(`/api/admin/employees/${id}/status`, session, { method: 'PATCH', body: JSON.stringify({ status }) });
  },
  resetEmployeePassword(session: AdminSession, id: number) {
    return request<{ temporary_password: string }>(`/api/admin/employees/${id}/reset-password`, session, { method: 'POST', body: '{}' });
  },
  mealPeriods(session: AdminSession) { return request<{ meal_periods: AdminMeal[] }>('/api/admin/meal-periods', session); },
  updateMeal(session: AdminSession, meal: AdminMeal) {
    const { code, ...input } = meal;
    return request<AdminMeal>(`/api/admin/meal-periods/${encodeURIComponent(code)}`, session, { method: 'PUT', body: JSON.stringify(input) });
  },
  recharge(session: AdminSession, input: { employee_id: number; amount_cents: number; receipt_ref: string; collected_at: string; payment_method: string; idempotency_key: string }) {
    return request<unknown>('/api/admin/recharges', session, { method: 'POST', body: JSON.stringify(input) });
  },
  reverseRecharge(session: AdminSession, id: number, reason: string, idempotencyKey: string) {
    return request<unknown>(`/api/admin/recharges/${id}/reverse`, session, { method: 'POST', body: JSON.stringify({ reason, idempotency_key: idempotencyKey }) });
  },
  adjust(session: AdminSession, accountId: number, amountCents: number, reason: string, idempotencyKey: string) {
    return request<unknown>(`/api/admin/accounts/${accountId}/adjust`, session, { method: 'POST', body: JSON.stringify({ amount_cents: amountCents, reason, idempotency_key: idempotencyKey }) });
  },
  security(session: AdminSession) { return request<SecurityState>('/api/admin/security', session); },
  /** Starts a binding. Replacing an existing one needs the password and a
   * current code of the authenticator being replaced. */
  startEnrollment(session: AdminSession, password: string, currentCode: string) {
    return request<SecondFactorEnrollment>('/api/admin/security/enrollment', session, {
      method: 'POST', body: JSON.stringify({ password, second_factor_code: currentCode }),
    });
  },
  confirmEnrollment(session: AdminSession, code: string) {
    return request<{ second_factor_bound: boolean; replaced: boolean; sessions_revoked: boolean }>(
      '/api/admin/security/enrollment/confirm', session,
      { method: 'POST', body: JSON.stringify({ second_factor_code: code }) });
  },
  removeSecondFactor(session: AdminSession, password: string, currentCode: string) {
    return request<{ second_factor_bound: boolean; sessions_revoked: boolean }>(
      '/api/admin/security/second-factor', session,
      { method: 'DELETE', body: JSON.stringify({ password, second_factor_code: currentCode }) });
  },

  generic<T>(session: AdminSession, path: string, options: RequestInit = {}) { return request<T>(path, session, options); },
};
