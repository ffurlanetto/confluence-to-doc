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
            Mes exports
          </NavLink>
          <NavLink to="/new">Nouvel export</NavLink>
          <NavLink to="/settings">Préférences</NavLink>
        </nav>
        <div className="user">
          <span title={me.email}>{me.name || me.email}</span>
          <button type="button" className="link" onClick={() => void logout()}>
            Se déconnecter
          </button>
        </div>
      </header>
      {prefs.data && !prefs.data.hasPat && (
        <div className="banner" role="alert">
          Aucun jeton d’accès Confluence n’est configuré.{' '}
          <Link to="/settings">Ajoutez votre PAT dans les préférences</Link> pour pouvoir exporter.
        </div>
      )}
      <main>{children}</main>
    </>
  );
}
