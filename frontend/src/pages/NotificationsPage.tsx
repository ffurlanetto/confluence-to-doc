import { useEffect } from 'react';
import { Link } from 'react-router-dom';

import { useMarkNotificationsRead, useNotifications } from '../api/hooks';
import { formatDate } from '../lib/format';

export function NotificationsPage() {
  const inbox = useNotifications();
  const markRead = useMarkNotificationsRead();
  const unread = inbox.data?.unread ?? 0;

  // Opening the page reads what is on it.
  useEffect(() => {
    if (unread > 0 && !markRead.isPending) markRead.mutate();
  }, [unread]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <section>
      <div className="section-header">
        <h1>Notifications</h1>
        <Link to="/settings">Notification settings</Link>
      </div>
      {inbox.isLoading && <p>Loading…</p>}
      {inbox.isError && <p className="error">Unable to load your notifications.</p>}
      {inbox.data?.notifications.length === 0 && <p className="empty">Nothing yet.</p>}
      <ul className="notifications">
        {inbox.data?.notifications.map((n) => (
          <li key={n.id} className={n.readAt ? 'notification' : 'notification unread'}>
            <strong>{n.title}</strong>
            <p>{n.body}</p>
            <span className="muted small">{formatDate(n.createdAt)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
