import { useState } from 'react';

import { useAdminQueue, useCancelExport, useRetryExport } from '../api/hooks';
import type { AdminExport, ExportStatus } from '../api/types';
import { AdminNav } from '../components/AdminNav';
import { StatusBadge } from '../components/StatusBadge';
import { formatDate } from '../lib/format';

const views: { value: string; label: string; statuses: ExportStatus[] }[] = [
  { value: 'active', label: 'Queued and running', statuses: ['queued', 'running'] },
  { value: 'failed', label: 'Failed', statuses: ['failed'] },
  { value: 'all', label: 'All', statuses: [] },
];

export function AdminQueuePage() {
  const [view, setView] = useState('active');
  const statuses = views.find((v) => v.value === view)?.statuses ?? [];
  const queue = useAdminQueue(statuses);
  const cancel = useCancelExport();
  const retry = useRetryExport();
  const exports = queue.data?.exports ?? [];
  const failure = cancel.error ?? retry.error;

  return (
    <section>
      <AdminNav />
      <div className="section-header">
        <h1>Export queue</h1>
      </div>
      <p className="muted">
        Exports of every user, newest first. Cancelling tells the owner; a failed export can be put back in
        the queue once its cause is fixed.
      </p>

      <div className="filters card">
        <div>
          <label className="label" htmlFor="queue-view">
            Show
          </label>
          <select id="queue-view" className="input" value={view} onChange={(e) => setView(e.target.value)}>
            {views.map((v) => (
              <option key={v.value} value={v.value}>
                {v.label}
              </option>
            ))}
          </select>
        </div>
      </div>

      {failure && (
        <p className="error" role="alert">
          {failure.message}
        </p>
      )}
      {queue.isLoading && <p>Loading…</p>}
      {queue.isError && <p className="error">Unable to load the queue.</p>}
      {queue.data && exports.length === 0 && <p className="empty">No export to show.</p>}
      {exports.length > 0 && (
        <table className="exports">
          <thead>
            <tr>
              <th scope="col">Page</th>
              <th scope="col">Owner</th>
              <th scope="col">Status</th>
              <th scope="col">Attempts</th>
              <th scope="col">Requested on</th>
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {exports.map((e) => (
              <QueueRow
                key={e.id}
                exp={e}
                busy={cancel.isPending || retry.isPending}
                onCancel={() => cancel.mutate(e.id)}
                onRetry={() => retry.mutate(e.id)}
              />
            ))}
          </tbody>
        </table>
      )}
      {queue.data && exports.length >= queue.data.limit && (
        <p className="muted small">Only the {queue.data.limit} most recent exports are shown.</p>
      )}
    </section>
  );
}

function QueueRow({
  exp,
  busy,
  onCancel,
  onRetry,
}: {
  exp: AdminExport;
  busy: boolean;
  onCancel: () => void;
  onRetry: () => void;
}) {
  const active = exp.status === 'queued' || exp.status === 'running';
  return (
    <tr>
      <td>
        <strong>{exp.title}</strong>
        <div className="muted small">
          {exp.format.toUpperCase()} · page {exp.pageId}
          {exp.classification && ` · ${exp.classification}`}
        </div>
        {exp.error && <div className="error small">{exp.error}</div>}
      </td>
      <td>{exp.ownerEmail ?? <span className="muted">Deleted account</span>}</td>
      <td>
        <StatusBadge exp={exp} />
      </td>
      <td>{exp.attempts}</td>
      <td>{formatDate(exp.createdAt)}</td>
      <td>
        <div className="actions">
          {active && (
            <button
              type="button"
              className="button danger"
              disabled={busy}
              onClick={onCancel}
              aria-label={`Cancel the export of ${exp.title} for ${exp.ownerEmail ?? 'a deleted account'}`}
            >
              Cancel
            </button>
          )}
          {exp.status === 'failed' && (
            <button
              type="button"
              className="button"
              disabled={busy}
              onClick={onRetry}
              aria-label={`Retry the export of ${exp.title} for ${exp.ownerEmail ?? 'a deleted account'}`}
            >
              Retry
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}
