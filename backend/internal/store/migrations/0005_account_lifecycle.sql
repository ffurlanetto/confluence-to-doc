-- Inactive accounts are deleted after a retention period; their owner is
-- warned beforehand, once.
ALTER TABLE users ADD COLUMN inactivity_warned_at timestamptz;
CREATE INDEX users_last_activity_idx ON users ((COALESCE(last_login_at, created_at)));

-- When the stored Confluence token expires (Data Center tokens can), so the
-- user can be told before exports start failing. NULL when unknown.
ALTER TABLE user_preferences ADD COLUMN pat_expires_at timestamptz;

-- Session lifecycle: the identity provider's session id (back-channel logout
-- names it), the encrypted refresh token used to re-check the account, and
-- when to re-check it.
ALTER TABLE sessions ADD COLUMN sid text NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN refresh_token bytea;
ALTER TABLE sessions ADD COLUMN revalidate_at timestamptz;
CREATE INDEX sessions_sid_idx ON sessions (sid) WHERE sid <> '';
