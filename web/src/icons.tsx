import type { ReactNode } from 'react';

export type IconName = 'home' | 'qr' | 'list' | 'person' | 'arrow' | 'eye' | 'eyeOff' | 'lock' | 'phone' | 'refresh' | 'wallet' | 'check' | 'clock' | 'logout' | 'alert';

const paths: Record<IconName, ReactNode> = {
  home: <><path d="m3 10 9-7 9 7v10a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z" /></>,
  qr: <><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><path d="M14 14h3v3h-3zm5 0h3m-8 5h3m2-2v4"/></>,
  list: <><rect x="4" y="3" width="16" height="18" rx="2"/><path d="M8 8h8M8 12h8M8 16h5"/></>,
  person: <><circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/></>,
  arrow: <path d="m9 18 6-6-6-6"/>,
  eye: <><path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6-10-6-10-6Z"/><circle cx="12" cy="12" r="2.5"/></>,
  eyeOff: <><path d="M3 3l18 18M10.6 6.1A11.5 11.5 0 0 1 12 6c6.5 0 10 6 10 6a13 13 0 0 1-3.1 3.5M6.2 7.4C3.5 9.2 2 12 2 12s3.5 6 10 6c1.5 0 2.8-.3 4-.8"/></>,
  lock: <><rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></>,
  phone: <><rect x="7" y="2" width="10" height="20" rx="2"/><path d="M11 18h2"/></>,
  refresh: <><path d="M20 7v5h-5M4 17v-5h5"/><path d="M5.6 9A7 7 0 0 1 18 7l2 5M4 12l2 5a7 7 0 0 0 12.4-2"/></>,
  wallet: <><rect x="3" y="5" width="18" height="15" rx="2"/><path d="M3 8h16a2 2 0 0 1 2 2v2h-5a2 2 0 1 0 0 4h5M6 5V3h12"/></>,
  check: <path d="m5 12 4 4L19 6"/>,
  clock: <><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></>,
  logout: <><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/></>,
  alert: <><circle cx="12" cy="12" r="10"/><path d="M12 7v6M12 17h.01"/></>,
};

export function Icon({ name, size = 24 }: { name: IconName; size?: number }) {
  return <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">{paths[name]}</svg>;
}
