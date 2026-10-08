import { useState, type FormEvent } from 'react';

import { ApiError } from '../api/client';
import { useDeleteAccount, useDeletePat, usePreferences, useSetDefaultFormat, useSetPat } from '../api/hooks';
import type { ExportFormat } from '../api/types';
import { daysUntil, formatDate, PAT_WARNING_DAYS } from '../lib/format';

export function SettingsPage() {
  const prefs = usePreferences();
  if (prefs.isLoading) return <p>Loading…</p>;
  if (!prefs.data) return <p className="error">Unable to load your preferences.</p>;
  const p = prefs.data;

  return (
    <section>
      <h1>Preferences</h1>
      <PatCard
        hasPat={p.hasPat}
        updatedAt={p.patUpdatedAt}
        expiresAt={p.patExpiresAt}
        baseUrl={p.confluenceBaseUrl}
      />
      <DefaultFormatCard value={p.defaultFormat} />
      <DocumentTemplateCard template={p.documentTemplate} />
      <DeleteAccountCard />
    </section>
  );
}

function PatCard({
  hasPat,
  updatedAt,
  expiresAt,
  baseUrl,
}: {
  hasPat: boolean;
  updatedAt?: string;
  expiresAt?: string;
  baseUrl: string;
}) {
  const [token, setToken] = useState('');
  const setPat = useSetPat();
  const deletePat = useDeletePat();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setPat.mutate(token, { onSuccess: () => setToken('') });
  };

  return (
    <form className="card" onSubmit={onSubmit} aria-label="Confluence personal access token">
      <h2>Confluence personal access token (PAT)</h2>
      <p className="muted small">
        Instance: <a href={baseUrl}>{baseUrl}</a>. Create a token in Confluence under{' '}
        <em>Profile → Settings → Personal access tokens</em>. It is verified, then stored encrypted, and never
        shown again.
      </p>
      <p>
        Status:{' '}
        {hasPat ? (
          <strong className="ok">configured (updated on {formatDate(updatedAt)})</strong>
        ) : (
          <strong className="error">not configured</strong>
        )}
      </p>
      {hasPat && expiresAt && <PatExpiry expiresAt={expiresAt} />}
      <label htmlFor="pat" className="label">
        {hasPat ? 'Replace the token' : 'Token'}
      </label>
      <input
        id="pat"
        type="password"
        className="input"
        autoComplete="off"
        value={token}
        onChange={(e) => setToken(e.target.value)}
        required
      />
      {setPat.isError && (
        <p className="error">
          {setPat.error instanceof ApiError ? setPat.error.message : 'Unexpected error.'}
        </p>
      )}
      {setPat.isSuccess && <p className="ok">Token valid for “{setPat.data.confluenceUser}” and saved.</p>}
      <div className="row">
        <button type="submit" className="button primary" disabled={setPat.isPending || token.trim() === ''}>
          {setPat.isPending ? 'Verifying…' : 'Verify and save'}
        </button>
        {hasPat && (
          <button
            type="button"
            className="button danger"
            onClick={() => deletePat.mutate()}
            disabled={deletePat.isPending}
          >
            Delete the token
          </button>
        )}
      </div>
    </form>
  );
}

function PatExpiry({ expiresAt }: { expiresAt: string }) {
  const days = daysUntil(expiresAt);
  if (days < 0) {
    return (
      <p className="error">
        The token expired on {formatDate(expiresAt)}. Create a new one and save it here.
      </p>
    );
  }
  return (
    <p className={days <= PAT_WARNING_DAYS ? 'error' : 'muted'}>
      Expires on {formatDate(expiresAt)}
      {days <= PAT_WARNING_DAYS && ' — create a new token and save it here before then'}.
    </p>
  );
}

function DeleteAccountCard() {
  const [confirming, setConfirming] = useState(false);
  const del = useDeleteAccount();
  return (
    <div className="card">
      <h2>Delete my account</h2>
      <p className="muted">
        Removes your Confluence token, your preferences and all your exports with their documents, and signs
        you out. The audit trail keeps its record of your past actions for its own retention period.
      </p>
      {!confirming ? (
        <button type="button" className="button danger" onClick={() => setConfirming(true)}>
          Delete my account…
        </button>
      ) : (
        <div role="alert">
          <p>
            <strong>This cannot be undone.</strong> Delete your account and all its data?
          </p>
          <div className="row">
            <button
              type="button"
              className="button danger"
              disabled={del.isPending}
              onClick={() => del.mutate(undefined, { onSuccess: () => window.location.assign('/') })}
            >
              {del.isPending ? 'Deleting…' : 'Yes, delete everything'}
            </button>
            <button type="button" className="button" onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </div>
          {del.isError && <p className="error">The account could not be deleted. Try again.</p>}
        </div>
      )}
    </div>
  );
}

function DocumentTemplateCard({ template }: { template?: string }) {
  return (
    <div className="card">
      <h2>Document template</h2>
      {template ? (
        <p>
          Documents are generated with the company template <strong className="ok">{template}</strong> — its
          header, fonts and page layout apply to both Word and PDF exports.
        </p>
      ) : (
        <p className="muted">
          No company template is configured; documents use the built-in styling. An administrator can set one
          with <code>WORD_TEMPLATE_PATH</code>.
        </p>
      )}
    </div>
  );
}

function DefaultFormatCard({ value }: { value: ExportFormat }) {
  const setFormat = useSetDefaultFormat();
  return (
    <div className="card">
      <h2>Default format</h2>
      <label htmlFor="default-format" className="label">
        Format preselected for a new export
      </label>
      <select
        id="default-format"
        className="input"
        value={value}
        onChange={(e) => setFormat.mutate(e.target.value as ExportFormat)}
        disabled={setFormat.isPending}
      >
        <option value="pdf">PDF</option>
        <option value="docx">Word (.docx)</option>
      </select>
    </div>
  );
}
