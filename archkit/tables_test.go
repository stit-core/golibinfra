package archkit

import (
	"slices"
	"testing"
)

const (
	orderQueries    = "internal/domain/order/infra/postgres/queries/extra.sql"
	customerQueries = "internal/domain/customer/infra/postgres/queries/extra.sql"
)

func TestOwnTables(t *testing.T) {
	t.Parallel()
	neighbour := []string{"table customers is queried by domains customer, order: " + whyTables}
	for name, sql := range map[string]string{
		"join":                     "SELECT c.email FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.id = $1;",
		"join without alias":       "SELECT customers.email FROM orders JOIN customers ON customers.id = orders.customer_id;",
		"comma":                    "SELECT c.email FROM orders o, customers c WHERE c.id = o.customer_id;",
		"comma after join":         "SELECT c.email FROM orders o JOIN order_items i ON i.order_id = o.id, customers c WHERE c.id = o.customer_id;",
		"subquery":                 "SELECT count(*) FROM orders WHERE customer_id IN (SELECT id FROM customers WHERE name <> '');",
		"quoted":                   `SELECT c.email FROM orders o JOIN "customers" c ON c.id = o.customer_id;`,
		"schema":                   `SELECT c.email FROM orders o JOIN public."customers" c ON c.id = o.customer_id;`,
		"upper case":               "SELECT C.EMAIL FROM ORDERS O JOIN CUSTOMERS C ON C.ID = O.CUSTOMER_ID;",
		"using":                    "DELETE FROM orders USING customers WHERE customers.id = orders.customer_id;",
		"update":                   "UPDATE customers SET name = $2 WHERE id = $1;",
		"insert":                   "INSERT INTO customers (id, email, name) VALUES ($1, $2, $3);",
		"delete":                   "DELETE FROM customers WHERE id = $1;",
		"only":                     "SELECT id FROM ONLY customers;",
		"cte":                      "WITH c AS (SELECT id FROM customers) SELECT o.id FROM orders o JOIN c ON c.id = o.customer_id;",
		"cte of the same name":     "WITH customers AS (SELECT id FROM customers) SELECT o.id FROM orders o JOIN customers c ON c.id = o.customer_id;",
		"cte and the table":        "WITH customers AS (SELECT 1 AS id) SELECT c.email FROM orders o JOIN public.customers c ON c.id = o.customer_id;",
		"parenthesized join":       "SELECT c.email FROM (orders o JOIN customers c ON c.id = o.customer_id) WHERE o.id = $1;",
		"parenthesized cross join": "SELECT c.email FROM (customers c CROSS JOIN orders o);",
		"after another":            "SELECT id FROM orders WHERE id = $1;\n\n-- name: Owner :one\nSELECT email FROM customers WHERE id = $1;",
		"alias named set":          "SELECT c.email FROM orders AS set, customers c WHERE c.id = set.customer_id;",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireFailures(t, failures(t, edits{orderQueries: "-- name: Probe :one\n" + sql + "\n"}), neighbour)
		})
	}

	runCases(t, []ruleCase{
		{
			name: "three domains and two tables",
			edits: edits{
				"internal/domain/billing/billing.go":                      goSrc("billing"),
				"internal/domain/billing/infra/postgres/queries/bill.sql": "SELECT o.id, c.email FROM orders o JOIN customers c ON c.id = o.customer_id;\n",
				customerQueries: "SELECT count(*) FROM orders WHERE customer_id = $1;\n",
			},
			want: []string{
				"table customers is queried by domains billing, customer: " + whyTables,
				"table orders is queried by domains billing, customer, order: " + whyTables,
			},
		},
		{
			name:  "table created with IF NOT EXISTS",
			edits: edits{customerQueries: "SELECT count(*) FROM order_items WHERE qty > 0;\n"},
			want:  []string{"table order_items is queried by domains customer, order: " + whyTables},
		},
		{
			name: "schema, quotes and case in a migration",
			edits: edits{
				"migrations/0003_tags.up.sql": "CREATE TABLE public.tags (id UUID PRIMARY KEY);\n" +
					"CREATE TABLE \"public\".\"Labels\" (id UUID PRIMARY KEY);\ncreate table if not exists \"notes\" (id UUID PRIMARY KEY);\n",
				orderQueries:    "SELECT id FROM tags;\nSELECT id FROM labels;\nSELECT id FROM notes;\n",
				customerQueries: "SELECT id FROM tags;\nSELECT id FROM labels;\nSELECT id FROM notes;\n",
			},
			want: []string{
				"table tags is queried by domains customer, order: " + whyTables,
				"table labels is queried by domains customer, order: " + whyTables,
				"table notes is queried by domains customer, order: " + whyTables,
			},
		},
		{
			name: "column named like a table of a neighbour",
			edits: edits{
				"migrations/0003_tags.up.sql": "ALTER TABLE orders ADD COLUMN customers TEXT;\n",
				orderQueries: `-- name: Columns :one
SELECT id, customers FROM orders WHERE customers IS NOT NULL ORDER BY customers;

-- name: SetColumn :exec
UPDATE orders SET customers = $2, note = customers WHERE id = $1;

-- name: InsertColumn :exec
INSERT INTO orders (id, customers) VALUES ($1, $2) RETURNING customers;

-- name: Expressions :one
SELECT TRIM(BOTH ' ' FROM customers), EXTRACT(year FROM created_at), orders.customers
FROM orders
WHERE note IS DISTINCT FROM customers AND note IS NOT DISTINCT FROM customers
GROUP BY customers
HAVING count(*) > 1;
`,
			},
		},
		{
			name: "cte named like a table of a neighbour",
			edits: edits{orderQueries: `-- name: CountBuyers :one
WITH customers AS (
    SELECT DISTINCT customer_id AS id FROM orders WHERE note IS NOT NULL
), recent AS NOT MATERIALIZED (SELECT id FROM customers)
SELECT count(*) FROM customers JOIN recent USING (id);

-- name: Chain :many
WITH RECURSIVE customers (id) AS (SELECT id FROM orders UNION ALL SELECT id FROM customers WHERE false)
SELECT id FROM customers;
`},
		},
		{
			name: "comments and strings",
			edits: edits{orderQueries: `-- name: Noise :one
-- FROM customers
SELECT id /* JOIN customers c */ FROM orders WHERE note = 'FROM customers' OR note = 'it''s FROM customers';

-- name: Inline :one
SELECT id FROM orders -- JOIN customers c ON c.id = orders.customer_id
WHERE id = $1;
`},
		},
		{
			name: "table of a kit",
			edits: edits{
				orderQueries:    "SELECT count(*) FROM audit_log WHERE resource = 'order';\n",
				customerQueries: "SELECT count(*) FROM audit_log WHERE resource = 'customer';\n",
			},
		},
		{
			name: "table created again by a later migration",
			edits: edits{
				"migrations/0003_orders_again.up.sql": "DROP TABLE order_items;\nDROP TABLE orders;\nCREATE TABLE orders (id UUID PRIMARY KEY);\n",
			},
		},
		{
			name: "down migrations and other SQL are not the schema",
			edits: edits{
				"migrations/0002_orders.down.sql": "DROP TABLE order_items;\nDROP TABLE orders;\nCREATE TABLE audit_log (id UUID PRIMARY KEY);\n",
				"migrations/seed/seed.up.sql":     "CREATE TABLE audit_log (id UUID PRIMARY KEY);\n",
				orderQueries:                      "SELECT count(*) FROM audit_log;\n",
				customerQueries:                   "SELECT count(*) FROM audit_log;\n",
			},
		},
	})
}

func TestQueriedTables(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		sql  string
		want []string
	}{
		{"SELECT a FROM t1", []string{"t1"}},
		{"SELECT a FROM t1, t2 WHERE t1.id = t2.id", []string{"t1", "t2"}},
		{"SELECT a FROM t1 x JOIN t2 y ON x.id = y.id LEFT JOIN t3 ON true, t4", []string{"t1", "t2", "t3", "t4"}},
		{"SELECT a FROM t1 WHERE b IN (SELECT b FROM t2) AND c = (SELECT max(c) FROM t3, t4)", []string{"t1", "t2", "t3", "t4"}},
		{"SELECT a, (SELECT count(*) FROM t2) FROM t1", []string{"t2", "t1"}},
		{"SELECT a FROM (SELECT a FROM t1) s, t2", []string{"t1", "t2"}},
		{"SELECT a FROM t1 WHERE a = 1, b", []string{"t1"}},
		{"SELECT a FROM t1 GROUP BY a, b HAVING a > 0 ORDER BY a, b LIMIT 1 OFFSET 2", []string{"t1"}},
		{"SELECT a, b FROM t1 UNION SELECT a, b FROM t2 EXCEPT SELECT a, b FROM t3 INTERSECT SELECT a, b FROM t4", []string{"t1", "t2", "t3", "t4"}},
		{"SELECT a FROM t1 ORDER BY a, b", []string{"t1"}},
		{"SELECT a FROM t1 x, t2 y FOR UPDATE OF x, y", []string{"t1", "t2"}},
		{"SELECT a FROM t1 FOR UPDATE SKIP LOCKED", []string{"t1"}},
		{"SELECT a FROM t1 FOR NO KEY UPDATE NOWAIT", []string{"t1"}},
		{"INSERT INTO t1 (a) VALUES ($1) ON CONFLICT (a) DO UPDATE SET a = EXCLUDED.a", []string{"t1"}},
		{"SELECT a FROM t1 WINDOW w AS (PARTITION BY a), b", []string{"t1"}},
		{"INSERT INTO t1 (a, b) SELECT a, b FROM t2 RETURNING a, b", []string{"t1", "t2"}},
		{"UPDATE t1 SET a = t2.a FROM t2 WHERE t1.id = t2.id", []string{"t1", "t2"}},
		{"DELETE FROM t1 USING t2, t3 WHERE t1.id = t2.id", []string{"t1", "t2", "t3"}},
		{`SELECT a FROM "public"."t1" JOIN public.t2 ON true JOIN "T3" ON true`, []string{"t1", "t2", "t3"}},
		{"SELECT a FROM ONLY t1 JOIN LATERAL (SELECT b FROM t2) s ON true", []string{"t1", "t2"}},
		{"SELECT TRIM(LEADING 'x' FROM a), SUBSTRING(a FROM 2), EXTRACT(day FROM b) FROM t1", []string{"t1"}},
		{"SELECT a FROM t1 WHERE a IS DISTINCT FROM b AND c IS NOT DISTINCT FROM d", []string{"t1"}},
		{"SELECT a FROM t1; SELECT a, b FROM t2", []string{"t1", "t2"}},
		{"SELECT a FROM t1 AS set, t2 WHERE set.id = t2.id", []string{"t1", "t2"}},
		{"INSERT INTO t1 (a) SELECT a FROM t2 ON CONFLICT DO NOTHING RETURNING a, b", []string{"t1", "t2"}},
		{"INSERT INTO t1 (a, b) SELECT a, b FROM t2 ON CONFLICT (a) DO UPDATE SET b = EXCLUDED.b, c = 1", []string{"t1", "t2"}},
		{"SELECT a FROM t1) x JOIN t2 ON true", []string{"t1", "t2"}},
		{"WITH c AS (SELECT id FROM t1) SELECT a FROM t2 JOIN c ON true", []string{"t1", "t2"}},
		{"WITH t1 AS (SELECT id FROM t2) SELECT count(*) FROM t1", []string{"t2"}},
		{"WITH t1 AS (SELECT id FROM t1) SELECT id FROM t1", []string{"t1"}},
		{`WITH "T1" AS (SELECT 1) SELECT * FROM t1, public.t1`, []string{"t1"}},
		{"WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r) SELECT n FROM r", nil},
		{"WITH RECURSIVE t1 AS (SELECT id FROM t1) SELECT id FROM t1", nil},
		{"WITH a AS (SELECT 1), b AS (SELECT * FROM a, t1) SELECT * FROM b, a", []string{"t1"}},
		{"WITH a AS (SELECT * FROM b), b AS (SELECT * FROM t1) SELECT * FROM a", []string{"b", "t1"}},
		{"WITH c (a, b) AS (SELECT a, b FROM t1) SELECT a FROM c", []string{"t1"}},
		{"WITH c AS MATERIALIZED (SELECT 1), d AS NOT MATERIALIZED (SELECT 2) SELECT * FROM c, d", nil},
		{"WITH x AS (DELETE FROM t1 RETURNING id) SELECT count(*) FROM x", []string{"t1"}},
		{"SELECT * FROM (WITH t1 AS (SELECT 1) SELECT * FROM t1) s, t1", []string{"t1"}},
		{"WITH t1 AS (SELECT 1) SELECT 1; SELECT a FROM t1", []string{"t1"}},
		{"INSERT INTO t1 WITH c AS (SELECT a FROM t2) SELECT a FROM c", []string{"t1", "t2"}},
		{"SELECT a::timestamp with time zone, b FROM t1", []string{"t1"}},
		{"SELECT * FROM unnest($1::text[]) WITH ORDINALITY AS u(x, n), t1", []string{"unnest", "t1"}},
		{"SELECT a FROM t1 ORDER BY a FETCH FIRST 5 ROWS WITH TIES", []string{"t1"}},
		{"SELECT a FROM (t1 CROSS JOIN t2)", []string{"t1", "t2"}},
		{"SELECT a FROM (t1 x JOIN t2 y ON x.id = y.id) j, t3", []string{"t1", "t2", "t3"}},
		{"SELECT a FROM ((t1 JOIN t2 ON true) JOIN t3 ON true)", []string{"t1", "t2", "t3"}},
		{"SELECT a FROM (VALUES (1, 2), (3, 4)) v(a, b), t1", []string{"t1"}},
		{"SELECT a FROM t1 JOIN t2 USING (id, t3)", []string{"t1", "t2"}},
		{"SELECT a FROM t1, LATERAL (SELECT b, c FROM t2) s", []string{"t1", "t2"}},
		{"SELECT 1", nil},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()
			if got := queriedTables(tc.sql); !slices.Equal(got, tc.want) {
				t.Fatalf("queriedTables = %v, want %v", got, tc.want)
			}
		})
	}
}
