-- An administrator can block an account: its sessions end and it cannot sign
-- in again until it is unblocked. The identity provider is not involved.
ALTER TABLE users ADD COLUMN blocked_at timestamptz;
ALTER TABLE users ADD COLUMN blocked_reason text NOT NULL DEFAULT '';

-- The company Word template uploaded from the admin console. It takes
-- precedence over WORD_TEMPLATE_PATH; at most one row exists.
CREATE TABLE document_template (
    id           smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    name         text NOT NULL,
    content      bytea NOT NULL,
    sha256       bytea NOT NULL,
    uploaded_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    uploaded_at  timestamptz NOT NULL DEFAULT now()
);

-- The admin queue lists exports of every user by status.
CREATE INDEX exports_status_created_idx ON exports (status, created_at DESC);
