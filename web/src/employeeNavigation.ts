import { useCallback, useEffect, useRef, useState } from 'react';

export type EmployeePage = 'home' | 'qr' | 'ledger' | 'profile' | 'self-service' | 'password' | 'recharge';

const paths: Record<EmployeePage, string> = {
  home: '/', qr: '/payment-code', ledger: '/transactions', profile: '/profile',
  'self-service': '/self-service', password: '/change-password', recharge: '/recharge-info',
};
const historyKey = 'canteenEmployeePage';
const parentPage = (page: EmployeePage): EmployeePage => page === 'password' ? 'profile' : 'home';

function currentPage(): EmployeePage {
  const path = window.location.pathname.replace(/\/$/, '') || '/';
  return (Object.keys(paths) as EmployeePage[]).find(page => paths[page] === path) || 'home';
}

function historyState(page: EmployeePage) {
  return { ...window.history.state, [historyKey]: page };
}

/** A scanned deep link has a real home entry behind it. Tabs share one child
 * entry, so Back returns home without walking through every tab visited.
 * Reloading or restoring a marked entry must not grow the history again. */
export function useEmployeeNavigation() {
  const [page, setPage] = useState(currentPage);
  const traversing = useRef(false);

  useEffect(() => {
    const initial = currentPage();
    if (window.history.state?.[historyKey] !== initial) {
      const destination = window.location.pathname + window.location.search + window.location.hash;
      window.history.replaceState(historyState('home'), '', paths.home);
      if (initial === 'password') window.history.pushState(historyState('profile'), '', paths.profile);
      if (initial !== 'home') window.history.pushState(historyState(initial), '', destination);
    }
    const restore = () => { traversing.current = false; setPage(currentPage()); };
    window.addEventListener('popstate', restore);
    window.addEventListener('pageshow', restore);
    return () => {
      window.removeEventListener('popstate', restore);
      window.removeEventListener('pageshow', restore);
    };
  }, []);

  const navigate = useCallback((next: EmployeePage) => {
    if (traversing.current) return;
    const current = currentPage();
    if (next === current) return;
    if ((current === 'password' || current === 'recharge') && next === parentPage(current)) {
      traversing.current = true;
      window.history.back();
      return;
    }
    if (next === 'home') {
      traversing.current = true;
      window.history.go(current === 'password' ? -2 : -1);
      return;
    }
    if (current === 'home' || next === 'password' || next === 'recharge') {
      window.history.pushState(historyState(next), '', paths[next]);
    } else {
      window.history.replaceState(historyState(next), '', paths[next]);
    }
    setPage(next);
    window.scrollTo(0, 0);
  }, []);

  return { page, navigate };
}
