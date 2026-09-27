import { useState, type FormEvent } from 'react';

import { ApiError } from '../api/client';
import { useDeletePat, usePreferences, useSetDefaultFormat, useSetPat } from '../api/hooks';
import type { ExportFormat } from '../api/types';
import { formatDate } from '../lib/format';

export function SettingsPage() {
  const prefs = usePreferences();
  if (prefs.isLoading) return <p>Chargement…</p>;
  if (!prefs.data) return <p className="error">Impossible de charger vos préférences.</p>;
  const p = prefs.data;

  return (
    <section>
      <h1>Préférences</h1>
      <PatCard hasPat={p.hasPat} updatedAt={p.patUpdatedAt} baseUrl={p.confluenceBaseUrl} />
      <DefaultFormatCard value={p.defaultFormat} />
    </section>
  );
}

function PatCard({ hasPat, updatedAt, baseUrl }: { hasPat: boolean; updatedAt?: string; baseUrl: string }) {
  const [token, setToken] = useState('');
  const setPat = useSetPat();
  const deletePat = useDeletePat();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setPat.mutate(token, { onSuccess: () => setToken('') });
  };

  return (
    <form className="card" onSubmit={onSubmit} aria-label="Jeton d’accès Confluence">
      <h2>Jeton d’accès Confluence (PAT)</h2>
      <p className="muted small">
        Instance : <a href={baseUrl}>{baseUrl}</a>. Créez un jeton dans Confluence via{' '}
        <em>Profil → Paramètres → Jetons d’accès personnels</em>. Il est vérifié puis stocké chiffré ; il
        n’est jamais réaffiché.
      </p>
      <p>
        Statut :{' '}
        {hasPat ? (
          <strong className="ok">configuré (mis à jour le {formatDate(updatedAt)})</strong>
        ) : (
          <strong className="error">non configuré</strong>
        )}
      </p>
      <label htmlFor="pat" className="label">
        {hasPat ? 'Remplacer le jeton' : 'Jeton'}
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
          {setPat.error instanceof ApiError ? setPat.error.message : 'Erreur inattendue.'}
        </p>
      )}
      {setPat.isSuccess && (
        <p className="ok">Jeton valide pour « {setPat.data.confluenceUser} » et enregistré.</p>
      )}
      <div className="row">
        <button type="submit" className="button primary" disabled={setPat.isPending || token.trim() === ''}>
          {setPat.isPending ? 'Vérification…' : 'Vérifier et enregistrer'}
        </button>
        {hasPat && (
          <button
            type="button"
            className="button danger"
            onClick={() => deletePat.mutate()}
            disabled={deletePat.isPending}
          >
            Supprimer le jeton
          </button>
        )}
      </div>
    </form>
  );
}

function DefaultFormatCard({ value }: { value: ExportFormat }) {
  const setFormat = useSetDefaultFormat();
  return (
    <div className="card">
      <h2>Format par défaut</h2>
      <label htmlFor="default-format" className="label">
        Format proposé lors d’un nouvel export
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
