-- Administrators are designated by the identity provider (OIDC_ADMIN_GROUPS);
-- the flag is refreshed at every login.
ALTER TABLE users ADD COLUMN is_admin boolean NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN last_login_at timestamptz;

-- Security audit trail. actor_id deliberately has no foreign key: events must
-- outlive the account they refer to, and actor_email keeps them readable.
CREATE TABLE audit_events (
    id           uuid PRIMARY KEY,
    occurred_at  timestamptz NOT NULL DEFAULT now(),
    actor_id     uuid,
    actor_email  text NOT NULL DEFAULT '',
    action       text NOT NULL,
    outcome      text NOT NULL CHECK (outcome IN ('success', 'failure', 'denied')),
    target_type  text NOT NULL DEFAULT '',
    target_id    text NOT NULL DEFAULT '',
    client_ip    text NOT NULL DEFAULT '',
    user_agent   text NOT NULL DEFAULT '',
    request_id   text NOT NULL DEFAULT '',
    details      jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_events_occurred_idx ON audit_events (occurred_at DESC, id DESC);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_id, occurred_at DESC);
CREATE INDEX audit_events_action_idx ON audit_events (action, occurred_at DESC);

-- Events are never modified. Deleting is left to the retention purge; the
-- tamper-proof copy is the one shipped to the SIEM from the logs.
CREATE FUNCTION audit_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END
$$;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();
