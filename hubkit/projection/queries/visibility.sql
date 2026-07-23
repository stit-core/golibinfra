-- Эталонные sqlc-запросы проекции entity_visibility и курсоров синка.
-- Скопировать в sqlc-пакет продукта.

-- name: UpsertVisibility :exec
INSERT INTO hub_entity_visibility (service, entity_kind, entity_id, hidden, updated_seq)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (service, entity_kind, entity_id) DO UPDATE SET
    hidden      = EXCLUDED.hidden,
    updated_seq = EXCLUDED.updated_seq
WHERE hub_entity_visibility.updated_seq < EXCLUDED.updated_seq;

-- name: ListHiddenForService :many
SELECT entity_kind, entity_id
FROM hub_entity_visibility
WHERE service = $1 AND hidden;

-- name: GetSyncCursor :one
SELECT cursor FROM hub_sync_cursors WHERE resource = $1;

-- name: SaveSyncCursor :exec
INSERT INTO hub_sync_cursors (resource, cursor, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (resource) DO UPDATE SET
    cursor     = EXCLUDED.cursor,
    updated_at = NOW();
