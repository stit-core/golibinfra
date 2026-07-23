-- Эталонные sqlc-запросы проекции projects. Скопировать в sqlc-пакет продукта.

-- name: UpsertProject :exec
INSERT INTO hub_projects (
    id, company_id, name, slug, description, archived, position, version,
    created_at, updated_at, updated_seq
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
ON CONFLICT (id) DO UPDATE SET
    company_id  = EXCLUDED.company_id,
    name        = EXCLUDED.name,
    slug        = EXCLUDED.slug,
    description = EXCLUDED.description,
    archived    = EXCLUDED.archived,
    position    = EXCLUDED.position,
    version     = EXCLUDED.version,
    created_at  = EXCLUDED.created_at,
    updated_at  = EXCLUDED.updated_at,
    updated_seq = EXCLUDED.updated_seq
WHERE hub_projects.updated_seq < EXCLUDED.updated_seq;

-- name: GetProject :one
SELECT * FROM hub_projects WHERE id = $1;

-- name: ListProjectsByCompany :many
SELECT * FROM hub_projects
WHERE company_id = $1 AND (sqlc.arg(include_archived)::bool OR NOT archived)
ORDER BY position, name;
