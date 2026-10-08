import { useState, type FormEvent } from 'react';

import { useAdminUsers, useBlockUser, useMe, useUnblockUser } from '../api/hooks';
import type { AdminUser } from '../api/types';
import { AdminNav } from '../components/AdminNav';
import { formatDate } from '../lib/format';

export function AdminUsersPage() {
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const users = useAdminUsers(query);
  const unblock = useUnblockUser();
  const [blocking, setBlocking] = useState<string>();
  const me = useMe();

  const apply = (e: FormEvent) => {
    e.preventDefault();
    setQuery(search.trim());
  };
  const list = users.data?.users ?? [];

  return (
    <section>
      <AdminNav />
      <div className="section-header">
        <h1>Users</h1>
      </div>
      <p className="muted">
        Everyone who signed in, most recently active first. Blocking signs the person out, cancels their
        pending exports and refuses their next sign-in until they are unblocked. Administrator rights come
        from the identity provider&apos;s groups.
      </p>

      <form className="filters card" onSubmit={apply} role="search" aria-label="Find users">
        <div>
          <label className="label" htmlFor="users-search">
            Name or email
          </label>
          <input
            id="users-search"
            className="input"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <div className="filters-submit">
          <button type="submit" className="button primary">
            Search
          </button>
        </div>
      </form>

      {unblock.error && (
        <p className="error" role="alert">
          {unblock.error.message}
        </p>
      )}
      {users.isLoading && <p>Loading…</p>}
      {users.isError && <p className="error">Unable to load the users.</p>}
      {users.data && list.length === 0 && <p className="empty">No user matches.</p>}
      {list.length > 0 && (
        <table className="exports">
          <thead>
            <tr>
              <th scope="col">User</th>
              <th scope="col">Last active</th>
              <th scope="col">Exports</th>
              <th scope="col">Status</th>
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {list.map((u) => (
              <UserRow
                key={u.id}
                user={u}
                self={u.id === me.data?.id}
                blocking={blocking === u.id}
                onBlock={() => setBlocking(u.id)}
                onDone={() => setBlocking(undefined)}
                onUnblock={() => unblock.mutate(u.id)}
              />
            ))}
          </tbody>
        </table>
      )}
      {users.data && list.length >= users.data.limit && (
        <p className="muted small">Only the first {users.data.limit} users are shown: search to narrow.</p>
      )}
    </section>
  );
}

function UserRow({
  user,
  self,
  blocking,
  onBlock,
  onDone,
  onUnblock,
}: {
  user: AdminUser;
  self: boolean;
  blocking: boolean;
  onBlock: () => void;
  onDone: () => void;
  onUnblock: () => void;
}) {
  return (
    <>
      <tr>
        <td>
          <strong>{user.name || user.email}</strong>
          <div className="muted small">
            {user.email}
            {user.isAdmin && ' · Administrator'}
          </div>
        </td>
        <td>{formatDate(user.lastActiveAt)}</td>
        <td>
          {user.totalExports}
          {user.activeExports > 0 && <span className="muted small"> ({user.activeExports} in progress)</span>}
        </td>
        <td>
          {user.blockedAt ? (
            <>
              <span className="badge badge-failed">Blocked</span>
              <div className="muted small">
                Since {formatDate(user.blockedAt)}: {user.blockedReason}
              </div>
            </>
          ) : (
            <span className="badge badge-succeeded">Active</span>
          )}
        </td>
        <td>
          <div className="actions">
            {user.blockedAt ? (
              <button
                type="button"
                className="button"
                onClick={onUnblock}
                aria-label={`Unblock ${user.email}`}
              >
                Unblock
              </button>
            ) : (
              !self &&
              !blocking && (
                <button
                  type="button"
                  className="button danger"
                  onClick={onBlock}
                  aria-label={`Block ${user.email}`}
                >
                  Block
                </button>
              )
            )}
          </div>
        </td>
      </tr>
      {blocking && (
        <tr>
          <td colSpan={5}>
            <BlockForm user={user} onDone={onDone} />
          </td>
        </tr>
      )}
    </>
  );
}

function BlockForm({ user, onDone }: { user: AdminUser; onDone: () => void }) {
  const [reason, setReason] = useState('');
  const block = useBlockUser();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    block.mutate({ userId: user.id, reason: reason.trim() }, { onSuccess: onDone });
  };
  return (
    <form className="block-form" onSubmit={submit} aria-label={`Block ${user.email}`}>
      <label className="label" htmlFor={`reason-${user.id}`}>
        Reason for blocking {user.email} (recorded in the audit trail)
      </label>
      <input
        id={`reason-${user.id}`}
        className="input"
        value={reason}
        maxLength={500}
        required
        onChange={(e) => setReason(e.target.value)}
      />
      {block.error && (
        <p className="error" role="alert">
          {block.error.message}
        </p>
      )}
      <div className="actions">
        <button type="submit" className="button danger" disabled={block.isPending || !reason.trim()}>
          Confirm the block
        </button>
        <button type="button" className="button" onClick={onDone}>
          Keep the account
        </button>
      </div>
    </form>
  );
}
