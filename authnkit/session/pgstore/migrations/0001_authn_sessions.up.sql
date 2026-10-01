CREATE TABLE authn_sessions (
    id          UUID         PRIMARY KEY,
    subject     TEXT         NOT NULL,
    expires_at  TIMESTAMPTZ  NOT NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ  NOT NULL
);
CREATE INDEX authn_sessions_subject_idx ON authn_sessions (subject) WHERE revoked_at IS NULL;
CREATE INDEX authn_sessions_expires_at_idx ON authn_sessions (expires_at);

CREATE TABLE authn_session_tokens (
    token_hash  TEXT         PRIMARY KEY,
    session_id  UUID         NOT NULL REFERENCES authn_sessions (id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ  NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ  NOT NULL
);
CREATE INDEX authn_session_tokens_session_id_idx ON authn_session_tokens (session_id);
