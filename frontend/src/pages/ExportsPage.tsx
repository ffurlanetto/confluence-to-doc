import { Link } from 'react-router-dom';

import { api } from '../api/client';
import { useDeleteExport, useExports, usePreferences } from '../api/hooks';
import type { Export } from '../api/types';
import { StatusBadge } from '../components/StatusBadge';
import { formatBytes, formatDate, formatRemaining } from '../lib/format';

export function ExportsPage() {
  const exports = useExports();
  const prefs = usePreferences();
  const del = useDeleteExport();

  return (
    <section>
      <div className="section-header">
        <h1>Mes exports</h1>
        <Link className="button primary" to="/new">
          Nouvel export
        </Link>
      </div>
      <p className="muted">
        Les documents générés restent disponibles au téléchargement pendant {prefs.data?.retentionHours ?? 48}{' '}
        heures.
      </p>

      {exports.isLoading && <p>Chargement…</p>}
      {exports.isError && <p className="error">Impossible de charger vos exports.</p>}
      {exports.data?.length === 0 && (
        <div className="empty">
          <p>Vous n’avez encore lancé aucun export.</p>
          <Link to="/new">Exporter une page Confluence</Link>
        </div>
      )}
      {exports.data && exports.data.length > 0 && (
        <table className="exports">
          <thead>
            <tr>
              <th scope="col">Page</th>
              <th scope="col">Format</th>
              <th scope="col">Statut</th>
              <th scope="col">Demandé le</th>
              <th scope="col">Expire dans</th>
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {exports.data.map((e) => (
              <ExportRow key={e.id} exp={e} onDelete={() => del.mutate(e.id)} deleting={del.isPending} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function ExportRow({ exp, onDelete, deleting }: { exp: Export; onDelete: () => void; deleting: boolean }) {
  const ready = exp.status === 'succeeded';
  return (
    <tr>
      <td>
        <strong>{exp.title}</strong>
        <div className="muted small">{exp.includeChildren ? 'Avec les sous-pages' : 'Page seule'}</div>
        {exp.error && <div className="error small">{exp.error}</div>}
      </td>
      <td>{exp.format.toUpperCase()}</td>
      <td>
        <StatusBadge exp={exp} />
      </td>
      <td>{formatDate(exp.createdAt)}</td>
      <td>{ready ? formatRemaining(exp.expiresAt) : '—'}</td>
      <td>
        <div className="actions">
          {ready && (
            <a className="button primary" href={api.downloadUrl(exp.id)} download>
              Télécharger <span className="muted small">({formatBytes(exp.fileSize)})</span>
            </a>
          )}
          <button
            type="button"
            className="button"
            onClick={onDelete}
            disabled={deleting}
            aria-label={`Supprimer l’export ${exp.title}`}
          >
            {exp.status === 'queued' || exp.status === 'running' ? 'Annuler' : 'Supprimer'}
          </button>
        </div>
      </td>
    </tr>
  );
}
