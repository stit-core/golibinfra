DROP INDEX IF EXISTS bruteforce_attempts_locked_idx;
CREATE INDEX bruteforce_attempts_last_attempt_idx ON bruteforce_attempts (last_attempt);
