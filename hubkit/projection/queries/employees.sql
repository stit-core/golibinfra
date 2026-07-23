-- Эталонные sqlc-запросы проекции employees. Скопировать в sqlc-пакет продукта.

-- name: UpsertEmployee :exec
INSERT INTO hub_employees (
    id, subject, username, full_name, email, department, role, is_active,
    version, created_at, updated_at, updated_seq
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
ON CONFLICT (id) DO UPDATE SET
    subject     = EXCLUDED.subject,
    username    = EXCLUDED.username,
    full_name   = EXCLUDED.full_name,
    email       = EXCLUDED.email,
    department  = EXCLUDED.department,
    role        = EXCLUDED.role,
    is_active   = EXCLUDED.is_active,
    version     = EXCLUDED.version,
    created_at  = EXCLUDED.created_at,
    updated_at  = EXCLUDED.updated_at,
    updated_seq = EXCLUDED.updated_seq
WHERE hub_employees.updated_seq < EXCLUDED.updated_seq;

-- name: GetEmployee :one
SELECT * FROM hub_employees WHERE id = $1;

-- name: GetEmployeeBySubject :one
SELECT * FROM hub_employees WHERE subject = $1;

-- name: ListEmployees :many
SELECT * FROM hub_employees
WHERE (sqlc.arg(include_inactive)::bool OR is_active)
ORDER BY full_name
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);
