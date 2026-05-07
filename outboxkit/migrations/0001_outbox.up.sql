CREATE TABLE outbox (
    id           BIGSERIAL    NOT NULL,
    subject      TEXT         NOT NULL,
    payload      JSONB        NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX outbox_unpublished_idx
    ON outbox (created_at, id) WHERE published_at IS NULL;

DO $$
DECLARE
    base   DATE := DATE_TRUNC('month', NOW())::DATE;
    i      INT;
    pstart DATE;
    pend   DATE;
    pname  TEXT;
BEGIN
    FOR i IN 0..3 LOOP
        pstart := (base + (i || ' month')::INTERVAL)::DATE;
        pend   := (base + ((i + 1) || ' month')::INTERVAL)::DATE;
        pname  := 'outbox_' || TO_CHAR(pstart, 'YYYY_MM');
        EXECUTE FORMAT(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF outbox FOR VALUES FROM (%L) TO (%L)',
            pname, pstart, pend
        );
    END LOOP;
END $$;
