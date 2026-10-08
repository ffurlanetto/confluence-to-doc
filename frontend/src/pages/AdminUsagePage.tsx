import { useState } from 'react';

import { useUsage } from '../api/hooks';
import { AdminNav } from '../components/AdminNav';
import { formatBytes } from '../lib/format';

const periods = [7, 30, 90, 365];

export function AdminUsagePage() {
  const [days, setDays] = useState(30);
  const usage = useUsage(days);
  const u = usage.data;

  return (
    <section>
      <AdminNav />
      <div className="section-header">
        <h1>Usage</h1>
      </div>
      <p className="muted">
        Exports finished over the period, counted from the audit trail (UTC days). Exports cancelled by an
        administrator are not counted.
      </p>

      <div className="filters card">
        <div>
          <label className="label" htmlFor="usage-period">
            Period
          </label>
          <select
            id="usage-period"
            className="input"
            value={days}
            onChange={(e) => setDays(Number(e.target.value))}
          >
            {periods.map((p) => (
              <option key={p} value={p}>
                Last {p} days
              </option>
            ))}
          </select>
        </div>
      </div>

      {usage.isLoading && <p>Loading…</p>}
      {usage.isError && <p className="error">Unable to load the usage figures.</p>}
      {u && (
        <>
          <table className="exports summary" aria-label="Summary">
            <tbody>
              <tr>
                <th scope="row">Documents generated</th>
                <td>{u.succeeded}</td>
              </tr>
              <tr>
                <th scope="row">Failed exports</th>
                <td>{u.failed}</td>
              </tr>
              <tr>
                <th scope="row">Pages exported</th>
                <td>{u.pages}</td>
              </tr>
              <tr>
                <th scope="row">Volume generated</th>
                <td>{formatBytes(u.bytes)}</td>
              </tr>
              <tr>
                <th scope="row">Users who exported</th>
                <td>{u.users}</td>
              </tr>
              <tr>
                <th scope="row">By format</th>
                <td>
                  PDF {u.byFormat.pdf ?? 0} · Word {u.byFormat.docx ?? 0}
                </td>
              </tr>
            </tbody>
          </table>

          <h2>Most active users</h2>
          {u.topUsers.length === 0 ? (
            <p className="empty">No export over the period.</p>
          ) : (
            <table className="exports" aria-label="Most active users">
              <thead>
                <tr>
                  <th scope="col">User</th>
                  <th scope="col">Documents</th>
                  <th scope="col">Pages</th>
                </tr>
              </thead>
              <tbody>
                {u.topUsers.map((t) => (
                  <tr key={t.userId}>
                    <td>{t.email || <span className="muted">Deleted account</span>}</td>
                    <td>{t.exports}</td>
                    <td>{t.pages}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          <h2>Per day</h2>
          {u.daily.length === 0 ? (
            <p className="empty">No export over the period.</p>
          ) : (
            <table className="exports" aria-label="Per day">
              <thead>
                <tr>
                  <th scope="col">Day</th>
                  <th scope="col">Generated</th>
                  <th scope="col">Failed</th>
                </tr>
              </thead>
              <tbody>
                {[...u.daily].reverse().map((d) => (
                  <tr key={d.day}>
                    <td>{d.day}</td>
                    <td>{d.succeeded}</td>
                    <td>{d.failed}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </section>
  );
}
