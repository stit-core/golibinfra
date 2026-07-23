-- Эталонные sqlc-запросы проекции companies. Скопировать в sqlc-пакет продукта.
-- Апсерт идемпотентен по id; updated_seq монотонен — поздний фид не перетирается
-- ранним (WHERE updated_seq).

-- name: UpsertCompany :exec
INSERT INTO hub_companies (
    id, name, slug, legal_name, inn, kpp, ogrn, legal_address,
    bank_details, contacts, links, archived, version,
    created_at, updated_at, updated_seq
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8,
    $9, $10, $11, $12, $13, $14, $15, $16
)
ON CONFLICT (id) DO UPDATE SET
    name          = EXCLUDED.name,
    slug          = EXCLUDED.slug,
    legal_name    = EXCLUDED.legal_name,
    inn           = EXCLUDED.inn,
    kpp           = EXCLUDED.kpp,
    ogrn          = EXCLUDED.ogrn,
    legal_address = EXCLUDED.legal_address,
    bank_details  = EXCLUDED.bank_details,
    contacts      = EXCLUDED.contacts,
    links         = EXCLUDED.links,
    archived      = EXCLUDED.archived,
    version       = EXCLUDED.version,
    created_at    = EXCLUDED.created_at,
    updated_at    = EXCLUDED.updated_at,
    updated_seq   = EXCLUDED.updated_seq
WHERE hub_companies.updated_seq < EXCLUDED.updated_seq;

-- name: GetCompany :one
SELECT * FROM hub_companies WHERE id = $1;

-- name: GetCompanyBySlug :one
SELECT * FROM hub_companies WHERE slug = $1;

-- name: ListCompanies :many
SELECT * FROM hub_companies
WHERE (sqlc.arg(include_archived)::bool OR NOT archived)
ORDER BY name
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);
