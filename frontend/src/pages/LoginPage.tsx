import { loginUrl } from '../api/client';

export function LoginPage() {
  const returnTo = window.location.pathname + window.location.search;
  return (
    <main className="login">
      <div className="card">
        <h1>Confluence Export</h1>
        <p>Exportez une page Confluence et toute son arborescence au format Word ou PDF.</p>
        <a className="button primary" href={loginUrl(returnTo)}>
          Se connecter
        </a>
      </div>
    </main>
  );
}
