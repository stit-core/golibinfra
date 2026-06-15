DROP INDEX IF EXISTS outbox_unpublished_idx;
CREATE INDEX IF NOT EXISTS outbox_unpublished_idx
    ON outbox (created_at, id) WHERE published_at IS NULL;

ALTER TABLE outbox DROP COLUMN IF EXISTS dead_at;
ALTER TABLE outbox DROP COLUMN IF EXISTS last_error;
ALTER TABLE outbox DROP COLUMN IF EXISTS attempts;
