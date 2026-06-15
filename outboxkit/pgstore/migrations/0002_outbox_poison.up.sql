ALTER TABLE outbox ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0;
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS dead_at TIMESTAMPTZ;

DROP INDEX IF EXISTS outbox_unpublished_idx;
CREATE INDEX IF NOT EXISTS outbox_unpublished_idx
    ON outbox (created_at, id) WHERE published_at IS NULL AND dead_at IS NULL;
