import { createRoot } from 'react-dom/client';
import App from './App';
import Admin from './Admin';
import Terminal from './Terminal';
import './styles.css';

// Fixed consumption links are shared by every employee and carry no identity,
// amount or meal. Login and the forced temporary-password change keep the
// employee at the requested entrance.
const path = window.location.pathname.replace(/\/$/, '') || '/';
const page = path === '/admin' || path.startsWith('/admin/') ? <Admin/>
  : path === '/terminal' ? <Terminal/>
  : <App/>;
createRoot(document.getElementById('root')!).render(page);
