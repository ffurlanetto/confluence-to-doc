import type { ReactNode } from 'react';
import { Link, NavLink } from 'react-router-dom';

import { api } from '../api/client';
import { usePreferences } from '../api/hooks';
import type { Me } from '../api/types';

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
