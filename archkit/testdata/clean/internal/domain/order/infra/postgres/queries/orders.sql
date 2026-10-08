-- name: InsertOrder :exec
INSERT INTO orders (id, customer_id, note)
VALUES ($1, $2, $3);

-- name: ListOrderItems :many
SELECT i.id, i.sku, i.qty
FROM order_items i
JOIN orders o ON o.id = i.order_id
WHERE o.customer_id = $1
ORDER BY i.id;
