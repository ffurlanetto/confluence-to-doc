import { Navigate, Route, Routes } from 'react-router-dom';

import { isUnauthenticated } from './api/client';
import { useMe } from './api/hooks';
import { Layout } from './components/Layout';
import { AdminAuditPage } from './pages/AdminAuditPage';
import { ExportsPage } from './pages/ExportsPage';
import { LoginPage } from './pages/LoginPage';
import { NewExportPage } from './pages/NewExportPage';
import { SettingsPage } from './pages/SettingsPage';

export function App() {
  const me = useMe();

  if (me.isLoading) {
    return <p className="center">Loading…</p>;
  }
  if (isUnauthenticated(me.error)) {
    return <LoginPage />;
  }
  if (!me.data) {
    return <p className="center error">The service is temporarily unavailable. Try again later.</p>;
  }

  return (
    <Layout me={me.data}>
      <Routes>
        <Route path="/" element={<ExportsPage />} />
        <Route path="/new" element={<NewExportPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        {me.data.isAdmin && <Route path="/admin/audit" element={<AdminAuditPage />} />}
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Layout>
  );
}
