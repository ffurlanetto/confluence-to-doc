import { useState, type FormEvent } from 'react';

import { useAuditEvents } from '../api/hooks';
import type { AuditEvent, AuditFilter } from '../api/types';
import { formatDate } from '../lib/format';

/** Actions recorded by the backend (backend/internal/audit). */
const actions: { value: string; label: string }[] = [
  { value: 'auth.login', label: 'Sign-in' },
  { value: 'auth.logout', label: 'Sign-out' },
  { value: 'pat.set', label: 'Token saved' },
  { value: 'pat.delete', label: 'Token removed' },
  { value: 'preferences.update', label: 'Preferences changed' },
  { value: 'export.create', label: 'Export requested' },
  { value: 'export.complete', label: 'Export generated' },
  { value: 'export.fail', label: 'Export failed' },
  { value: 'export.download', label: 'Document downloaded' },
  { value: 'export.delete', label: 'Export deleted' },
  { value: 'export.expire', label: 'Document expired' },
  { value: 'admin.access', label: 'Administration access' },
  { value: 'admin.audit.read', label: 'Audit trail read' },
];

const actionLabel = (action: string) => actions.find((a) => a.value === action)?.label ?? action;

/** Converts a date input value (local day) to an RFC 3339 instant. */
const dayStart = (day: string, offsetDays = 0): string | undefined => {
  if (!day) return undefined;
  const d = new Date(`${day}T00:00:00`);
  d.setDate(d.getDate() + offsetDays);
  return d.toISOString();
};

export function AdminAuditPage() {
  const [actor, setActor] = useState('');
  const [action, setAction] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [filter, setFilter] = useState<AuditFilter>({});
  const audit = useAuditEvents(filter);

  const apply = (e: FormEvent) => {
    e.preventDefault();
    // "to" is inclusive for the user: the whole selected day is included.
    setFilter({
      actor: actor.trim() || undefined,
      action: action || undefined,
      from: dayStart(from),
      to: dayStart(to, 1),
    });
  };

  const events = audit.data?.pages.flatMap((p) => p.events) ?? [];

  return (
    <section>
      <div className="section-header">
        <h1>Audit trail</h1>
      </div>
      <p className="muted">
        Sign-ins, token changes, exports and downloads of every user, newest first. Viewing this page is
        recorded too.
      </p>

      <form className="filters card" onSubmit={apply} aria-label="Audit filters">
        <div>
          <label className="label" htmlFor="audit-actor">
            User
          </label>
          <input
            id="audit-actor"
            className="input"
            placeholder="Email or part of it"
            value={actor}
            onChange={(e) => setActor(e.target.value)}
          />
        </div>
        <div>
          <label className="label" htmlFor="audit-action">
            Action
          </label>
          <select
            id="audit-action"
            className="input"
            value={action}
            onChange={(e) => setAction(e.target.value)}
          >
            <option value="">All actions</option>
            {actions.map((a) => (
              <option key={a.value} value={a.value}>
                {a.label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className="label" htmlFor="audit-from">
            From
          </label>
          <input
            id="audit-from"
            type="date"
            className="input"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
          />
        </div>
        <div>
          <label className="label" htmlFor="audit-to">
            To
          </label>
          <input
            id="audit-to"
            type="date"
            className="input"
            value={to}
            onChange={(e) => setTo(e.target.value)}
          />
        </div>
        <div className="filters-submit">
          <button type="submit" className="button primary">
            Apply
          </button>
        </div>
      </form>

      {audit.isLoading && <p>Loading…</p>}
      {audit.isError && <p className="error">Unable to load the audit trail.</p>}
      {audit.data && events.length === 0 && <p className="empty">No event matches these filters.</p>}
      {events.length > 0 && (
        <table className="exports audit">
          <thead>
            <tr>
              <th scope="col">When</th>
              <th scope="col">User</th>
              <th scope="col">Action</th>
              <th scope="col">Outcome</th>
              <th scope="col">Details</th>
              <th scope="col">Address</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e) => (
              <AuditRow key={e.id} event={e} />
            ))}
          </tbody>
        </table>
      )}
      {audit.hasNextPage && (
        <div className="row">
          <button
            type="button"
            className="button"
            onClick={() => void audit.fetchNextPage()}
            disabled={audit.isFetchingNextPage}
          >
            {audit.isFetchingNextPage ? 'Loading…' : 'Load older events'}
          </button>
        </div>
      )}
    </section>
  );
}

const outcomeClass: Record<AuditEvent['outcome'], string> = {
  success: 'badge badge-succeeded',
  failure: 'badge badge-failed',
  denied: 'badge badge-failed',
};

function AuditRow({ event }: { event: AuditEvent }) {
  const details = Object.entries(event.details ?? {});
  return (
    <tr>
      <td>{formatDate(event.occurredAt)}</td>
      <td>{event.actorEmail || <span className="muted">{event.actorId ? event.actorId : 'System'}</span>}</td>
      <td>
        {actionLabel(event.action)}
        <div className="muted small">{event.action}</div>
      </td>
      <td>
        <span className={outcomeClass[event.outcome]}>{event.outcome}</span>
      </td>
      <td className="small">
        {event.targetId && (
          <div>
            {event.targetType} <code>{event.targetId}</code>
          </div>
        )}
        {details.map(([k, v]) => (
          <div key={k}>
            <span className="muted">{k}:</span> {typeof v === 'string' ? v : JSON.stringify(v)}
          </div>
        ))}
      </td>
      <td className="small" title={event.userAgent}>
        {event.clientIp ?? '—'}
      </td>
    </tr>
  );
}
