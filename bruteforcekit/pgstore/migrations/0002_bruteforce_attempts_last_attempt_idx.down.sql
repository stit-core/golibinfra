DROP INDEX IF EXISTS bruteforce_attempts_last_attempt_idx;
CREATE INDEX bruteforce_attempts_locked_idx ON bruteforce_attempts (locked_until) WHERE locked_until IS NOT NULL;
