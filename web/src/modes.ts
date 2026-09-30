import { useCallback, useEffect, useRef, useState } from 'react';
import { api, type ConsumptionModes } from './api';

/** The safe default while the modes are unknown: offer the entrance that the
 * server also enforces. A page must never hide a working entrance because a
 * network call failed, so the fallback keeps the entry visible and lets the
 * server refuse the request if the entrance is actually closed. */
const bothEnabled: ConsumptionModes = { payment_code: true, self_service: true };

// Reading the modes is cheap and the page is long-lived, so one read per mount
// plus one on return-to-tab is enough to notice an administrator closing an
// entrance without polling on a timer.
export function useConsumptionModes(): ConsumptionModes {
  const [modes, setModes] = useState<ConsumptionModes>(bothEnabled);
  const active = useRef(true);

  const load = useCallback(() => {
    void api.consumptionModes().then(value => {
      if (active.current) setModes(value);
    }).catch(() => {
      // A failed read only means the page cannot pre-hide a closed entrance; the
      // server still refuses the request, so keep the entries visible.
    });
  }, []);

  useEffect(() => {
    active.current = true;
    load();
    const onVisible = () => { if (document.visibilityState === 'visible') load(); };
    document.addEventListener('visibilitychange', onVisible);
    return () => { active.current = false; document.removeEventListener('visibilitychange', onVisible); };
  }, [load]);

  return modes;
}
