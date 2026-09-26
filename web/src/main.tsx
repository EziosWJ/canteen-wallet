import { createRoot } from 'react-dom/client';
import App from './App';
import Admin from './Admin';
import Terminal from './Terminal';
import './styles.css';

const path = window.location.pathname.replace(/\/$/, '') || '/';
const page = path === '/admin' ? <Admin/> : path === '/terminal' ? <Terminal/> : <App/>;
createRoot(document.getElementById('root')!).render(page);
