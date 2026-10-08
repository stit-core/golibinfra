-- name: GetCustomer :one
SELECT id, email, name
FROM customers
WHERE id = $1;

-- name: ListCustomers :many
SELECT id, email, name
FROM customers
WHERE created_at >= $1
ORDER BY created_at
LIMIT $2 OFFSET $3;
