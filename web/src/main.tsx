import { createRoot } from 'react-dom/client';
import App from './App';
import Admin from './Admin';
import SelfService from './SelfService';
import Terminal from './Terminal';
import './styles.css';

// The fixed self-service link is shared by every employee and carries no
// identity, amount or meal. It is a normal deep link, so login and the forced
// temporary-password change return the employee straight back to it.
const path = window.location.pathname.replace(/\/$/, '') || '/';
const page = path === '/admin' ? <Admin/>
  : path === '/terminal' ? <Terminal/>
  : path === '/self-service' ? <SelfService/>
  : <App/>;
createRoot(document.getElementById('root')!).render(page);
