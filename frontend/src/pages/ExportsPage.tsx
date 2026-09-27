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
        <h1>My exports</h1>
        <Link className="button primary" to="/new">
          New export
        </Link>
      </div>
      <p className="muted">
        Generated documents remain available for download for {prefs.data?.retentionHours ?? 48} hours.
      </p>

      {exports.isLoading && <p>Loading…</p>}
      {exports.isError && <p className="error">Unable to load your exports.</p>}
      {exports.data?.length === 0 && (
        <div className="empty">
          <p>You have not started any export yet.</p>
          <Link to="/new">Export a Confluence page</Link>
        </div>
      )}
      {exports.data && exports.data.length > 0 && (
        <table className="exports">
          <thead>
            <tr>
              <th scope="col">Page</th>
              <th scope="col">Format</th>
              <th scope="col">Status</th>
              <th scope="col">Requested on</th>
              <th scope="col">Expires in</th>
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
        <div className="muted small">{exp.includeChildren ? 'With child pages' : 'Single page'}</div>
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
              Download <span className="muted small">({formatBytes(exp.fileSize)})</span>
            </a>
          )}
          <button
            type="button"
            className="button"
            onClick={onDelete}
            disabled={deleting}
            aria-label={`Delete export ${exp.title}`}
          >
            {exp.status === 'queued' || exp.status === 'running' ? 'Cancel' : 'Delete'}
          </button>
        </div>
      </td>
    </tr>
  );
}
