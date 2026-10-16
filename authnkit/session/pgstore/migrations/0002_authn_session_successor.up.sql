ALTER TABLE authn_session_tokens ADD COLUMN IF NOT EXISTS successor_hash TEXT;
