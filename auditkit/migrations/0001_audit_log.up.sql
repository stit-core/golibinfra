CREATE TABLE audit_log (
    id          BIGSERIAL    PRIMARY KEY,
    actor_id    TEXT,
    action      TEXT         NOT NULL,
    resource    TEXT         NOT NULL,
    resource_id TEXT,
    metadata    JSONB,
    ip          TEXT,
    request_id  TEXT,
    at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, at DESC);
CREATE INDEX audit_log_resource_idx ON audit_log (resource, resource_id, at DESC);
