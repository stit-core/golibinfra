CREATE TABLE bruteforce_attempts (
    subject        TEXT        NOT NULL,
    ip             TEXT        NOT NULL,
    failed_count   INT         NOT NULL DEFAULT 0,
    locked_until   TIMESTAMPTZ,
    last_attempt   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (subject, ip)
);
CREATE INDEX bruteforce_attempts_locked_idx ON bruteforce_attempts (locked_until) WHERE locked_until IS NOT NULL;
