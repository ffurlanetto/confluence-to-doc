import type { ReactNode } from 'react';
import { Link, NavLink } from 'react-router-dom';

import { api } from '../api/client';
import { usePreferences } from '../api/hooks';
import type { Me, Preferences } from '../api/types';
import { daysUntil, formatDate, PAT_WARNING_DAYS } from '../lib/format';

async function logout() {
  try {
    const { logoutUrl } = await api.logout();
    window.location.assign(logoutUrl ?? '/');
  } catch {
    window.location.assign('/');
  }
}

export function Layout({ me, children }: { me: Me; children: ReactNode }) {
  const prefs = usePreferences();
  return (
    <>
      <header className="topbar">
        <Link to="/" className="brand">
          Confluence Export
        </Link>
        <nav aria-label="Navigation principale">
          <NavLink to="/" end>
            My exports
          </NavLink>
          <NavLink to="/new">New export</NavLink>
          <NavLink to="/settings">Preferences</NavLink>
          {me.isAdmin && <NavLink to="/admin/audit">Audit</NavLink>}
        </nav>
        <div className="user">
          <span title={me.email}>{me.name || me.email}</span>
          <button type="button" className="link" onClick={() => void logout()}>
            Sign out
          </button>
        </div>
      </header>
      {prefs.data && <PatExpiryBanner prefs={prefs.data} />}
      {prefs.data && !prefs.data.hasPat && (
        <div className="banner" role="alert">
          No Confluence personal access token is configured.{' '}
          <Link to="/settings">Add your PAT in the preferences</Link> to be able to export.
        </div>
      )}
      <main>{children}</main>
    </>
  );
}

function PatExpiryBanner({ prefs }: { prefs: Preferences }) {
  if (!prefs.hasPat || !prefs.patExpiresAt) return null;
  const days = daysUntil(prefs.patExpiresAt);
  if (days > PAT_WARNING_DAYS) return null;
  return (
    <div className="banner" role="alert">
      {days < 0
        ? `Your Confluence token expired on ${formatDate(prefs.patExpiresAt)}.`
        : `Your Confluence token expires on ${formatDate(prefs.patExpiresAt)}.`}{' '}
      <Link to="/settings">Save a new one in the preferences</Link> to keep exporting.
    </div>
  );
}
