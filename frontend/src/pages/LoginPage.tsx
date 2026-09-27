import { loginUrl } from '../api/client';

export function LoginPage() {
  const returnTo = window.location.pathname + window.location.search;
  return (
    <main className="login">
      <div className="card">
        <h1>Confluence Export</h1>
        <p>Export a Confluence page and its whole tree to Word or PDF.</p>
        <a className="button primary" href={loginUrl(returnTo)}>
          Sign in
        </a>
      </div>
    </main>
  );
}
