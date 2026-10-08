-- Token buckets shared by every instance (see internal/ratelimit). One row
-- per limit, e.g. the request budget all workers together may spend on
-- Confluence.
CREATE TABLE rate_limits (
    name        text PRIMARY KEY,
    tokens      double precision NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT clock_timestamp()
);
