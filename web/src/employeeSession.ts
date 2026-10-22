import { useCallback, useEffect, useRef, useState } from 'react';
import { clearSession, EMPLOYEE_SESSION_KEY, readSession, type Session } from './api';

/** Storage events alone miss changes made while a mobile page is frozen.
 * Reconcile on restoration as well as on cross-tab changes. */
export function useEmployeeSession() {
  const [session, setState] = useState<Session | null>(readSession);
  const current = useRef(session);
  const setSession = useCallback((next: Session | null) => {
    current.current = next;
    setState(next);
  }, []);
  const isCurrent = useCallback((expected: Session) => current.current?.access_token === expected.access_token, []);

  const reconcile = useCallback(() => {
    const next = readSession();
    if (next?.access_token !== current.current?.access_token) setSession(next);
  }, [setSession]);

  const expire = useCallback((expected: Session) => {
    if (!isCurrent(expected)) return;
    const stored = readSession();
    // An older response must not erase a login completed in another page.
    if (stored && stored.access_token !== expected.access_token) {
      setSession(stored);
      return;
    }
    clearSession();
    setSession(null);
  }, [isCurrent, setSession]);

  useEffect(() => {
    const storage = (event: StorageEvent) => {
      if (event.key === null || event.key === EMPLOYEE_SESSION_KEY) reconcile();
    };
    const visible = () => { if (document.visibilityState === 'visible') reconcile(); };
    window.addEventListener('storage', storage);
    window.addEventListener('pageshow', reconcile);
    document.addEventListener('visibilitychange', visible);
    return () => {
      window.removeEventListener('storage', storage);
      window.removeEventListener('pageshow', reconcile);
      document.removeEventListener('visibilitychange', visible);
    };
  }, [reconcile]);

  return { session, setSession, expire, isCurrent };
}
