-- Notifications: one row per message, shown in the application, and one
-- delivery per external channel (email, Teams), retried until sent.
CREATE TABLE notifications (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind        text NOT NULL,
    title       text NOT NULL,
    body        text NOT NULL,
    link        text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    read_at     timestamptz
);
CREATE INDEX notifications_user_idx ON notifications (user_id, created_at DESC);

CREATE TABLE notification_deliveries (
    notification_id  uuid NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    channel          text NOT NULL CHECK (channel IN ('email', 'teams')),
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    attempts         integer NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    last_error       text NOT NULL DEFAULT '',
    sent_at          timestamptz,
    PRIMARY KEY (notification_id, channel)
);
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (next_attempt_at) WHERE status = 'pending';

-- Per-user choices. The Teams workflow URL is a secret (whoever has it can
-- post in the user's chat): it is stored encrypted, like the PAT.
ALTER TABLE user_preferences ADD COLUMN notify_email boolean NOT NULL DEFAULT true;
ALTER TABLE user_preferences ADD COLUMN notify_exports boolean NOT NULL DEFAULT true;
ALTER TABLE user_preferences ADD COLUMN teams_webhook bytea;
ALTER TABLE user_preferences ADD COLUMN pat_expiry_notified_at timestamptz;
