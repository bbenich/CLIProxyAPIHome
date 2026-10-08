import { StrictMode, useEffect, useMemo, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { QuotaDashboard } from './QuotaDashboard';
import { createQuotaApi } from './api';
import { readHomeSession } from './session';
import './style.css';

function App() {
  const readSession = () => { try { return readHomeSession(localStorage, sessionStorage, location.origin); } catch { return null; } };
  const [session, setSession] = useState(readSession);
  useEffect(() => {
    const update = () => setSession(readSession());
    window.addEventListener('storage', update);
    window.addEventListener('focus', update);
    return () => { window.removeEventListener('storage', update); window.removeEventListener('focus', update); };
  }, []);
  const api = useMemo(() => session ? createQuotaApi(session) : null, [session?.baseUrl, session?.managementKey]);
  return api ? <QuotaDashboard api={api} /> : <section className="connection"><h1>Connect to Home Center</h1><p>Use the Connection button in Home Center to view subscription quotas.</p><a href="/management.html#/admin/connect" target="_top">Open Home Center connection</a></section>;

}
createRoot(document.getElementById('root')!).render(<StrictMode><App /></StrictMode>);
