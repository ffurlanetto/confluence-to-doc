CREATE TABLE users (
    id          uuid PRIMARY KEY,
    issuer      text NOT NULL,
    subject     text NOT NULL,
    email       text NOT NULL DEFAULT '',
    name        text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject)
);

-- Only a SHA-256 of the session token is stored: a database leak does not
-- allow session hijacking.
CREATE TABLE sessions (
    token_hash  bytea PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL
);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

CREATE TABLE user_preferences (
    user_id         uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    encrypted_pat   bytea,
    pat_updated_at  timestamptz,
    default_format  text NOT NULL DEFAULT 'pdf' CHECK (default_format IN ('pdf', 'docx')),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- The exports table is also the job queue (claimed with FOR UPDATE SKIP LOCKED).
CREATE TABLE exports (
    id                uuid PRIMARY KEY,
    user_id           uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    root_page_id      text NOT NULL,
    root_title        text NOT NULL,
    format            text NOT NULL CHECK (format IN ('pdf', 'docx')),
    include_children  boolean NOT NULL,
    status            text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'expired')),
    attempts          integer NOT NULL DEFAULT 0,
    max_attempts      integer NOT NULL,
    error             text NOT NULL DEFAULT '',
    pages_done        integer NOT NULL DEFAULT 0,
    pages_total       integer NOT NULL DEFAULT 0,
    file_key          text NOT NULL DEFAULT '',
    file_size         bigint NOT NULL DEFAULT 0,
    run_after         timestamptz NOT NULL DEFAULT now(),
    locked_by         text,
    locked_until      timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    started_at        timestamptz,
    finished_at       timestamptz,
    expires_at        timestamptz
);
CREATE INDEX exports_user_created_idx ON exports (user_id, created_at DESC);
CREATE INDEX exports_queue_idx ON exports (created_at) WHERE status = 'queued';
CREATE INDEX exports_lease_idx ON exports (locked_until) WHERE status = 'running';
CREATE INDEX exports_expiry_idx ON exports (expires_at) WHERE status IN ('succeeded', 'failed', 'expired');
