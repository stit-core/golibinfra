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
		"schema with spaces":       "SELECT c.email FROM orders o JOIN public . customers c ON c.id = o.customer_id;",
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
			name: "unlogged, temporary and global tables",
			edits: edits{
				"migrations/0003_carts.up.sql": "CREATE UNLOGGED TABLE carts (id UUID);\nCREATE TEMP TABLE drafts (id UUID);\n" +
					"CREATE GLOBAL TEMPORARY TABLE marks (id UUID);\n",
				orderQueries:    "SELECT id FROM carts;\nSELECT id FROM drafts;\nSELECT id FROM marks;\n",
				customerQueries: "SELECT id FROM carts;\nSELECT id FROM drafts;\nSELECT id FROM marks;\n",
			},
			want: []string{
				"table carts is queried by domains customer, order: " + whyTables,
				"table drafts is queried by domains customer, order: " + whyTables,
				"table marks is queried by domains customer, order: " + whyTables,
			},
		},
		{
			name: "table of another schema",
			edits: edits{
				"migrations/0003_billing.up.sql": "CREATE SCHEMA billing;\nCREATE TABLE billing.invoices (id UUID);\n",
				orderQueries:                     "SELECT id FROM invoices;\n",
				customerQueries:                  "SELECT id FROM billing.invoices;\n",
			},
			want: []string{"table invoices is queried by domains customer, order: " + whyTables},
		},
		{
			name: "table created in a DO block",
			edits: edits{
				"migrations/0003_carts.up.sql": "DO $$\nBEGIN\n  IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'carts') THEN\n" +
					"    CREATE TABLE carts (id UUID);\n  END IF;\nEND\n$$;\n",
				orderQueries:    "SELECT id FROM carts;\n",
				customerQueries: "SELECT id FROM carts;\n",
			},
			want: []string{"table carts is queried by domains customer, order: " + whyTables},
		},
		{
			name: "dollar and escape strings, nested comments",
			edits: edits{orderQueries: `-- name: Noise :one
SELECT $$FROM customers$$, $q$ JOIN customers $q$, E'it\'s FROM customers'
FROM orders /* a /* b */ JOIN customers c */
WHERE id = $1;
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

// Таблица соседа за представлением или функцией из миграций — то же обращение к ней; отказ называет обход.
func TestOwnTablesThroughSchemaObjects(t *testing.T) {
	t.Parallel()
	const objects = "migrations/0003_objects.up.sql"
	through := func(via string) []string {
		return []string{"table customers is queried by domains customer, order: " + whyTables + " (order through " + via + ")"}
	}
	for name, tc := range map[string]struct{ schema, query, via string }{
		"view": {
			"CREATE VIEW v AS SELECT id, email FROM customers WHERE name <> 'x';",
			"SELECT o.id FROM orders o JOIN v ON v.id = o.customer_id;", "view v",
		},
		"view with columns and options": {
			"CREATE OR REPLACE VIEW v (id, email) WITH (security_barrier = true) AS SELECT id, email FROM customers WITH LOCAL CHECK OPTION;",
			"SELECT email FROM v;", "view v",
		},
		"temporary view":    {"CREATE TEMPORARY VIEW v AS SELECT * FROM customers;", "SELECT * FROM v;", "view v"},
		"recursive view":    {"CREATE RECURSIVE VIEW v (id) AS SELECT id FROM customers UNION ALL SELECT id FROM v WHERE false;", "SELECT * FROM v;", "view v"},
		"materialized view": {"CREATE MATERIALIZED VIEW IF NOT EXISTS v AS SELECT * FROM public.customers WITH NO DATA;", "SELECT * FROM v;", "view v"},
		"quoted names":      {`create view "V" as select * from "public"."customers";`, "SELECT * FROM public.v;", "view v"},
		"view over a view": {
			"CREATE VIEW base AS SELECT * FROM customers;\nCREATE VIEW v AS SELECT * FROM base;", "SELECT * FROM v;", "view v",
		},
		"view over a function": {
			"CREATE FUNCTION email_of(uid UUID) RETURNS TEXT AS $$ SELECT email FROM customers WHERE id = uid $$ LANGUAGE sql;\n" +
				"CREATE VIEW v AS SELECT email_of(customer_id) FROM orders;",
			"SELECT * FROM v;", "view v",
		},
		"view in a DO block": {
			"DO $$\nBEGIN\n  IF true THEN\n    CREATE VIEW v AS SELECT * FROM customers;\n  END IF;\nEND\n$$;", "SELECT * FROM v;", "view v",
		},
		"function": {
			"CREATE FUNCTION f(uid UUID) RETURNS TEXT AS $$ SELECT email FROM customers WHERE id = uid; $$ LANGUAGE sql STABLE;",
			"SELECT f(o.customer_id) FROM orders o;", "function f",
		},
		"body in quotes": {
			"CREATE OR REPLACE FUNCTION public.f(uid UUID) RETURNS TEXT LANGUAGE sql AS 'SELECT email FROM customers WHERE id = uid AND name <> ''x''';",
			"SELECT public.f(o.customer_id) FROM orders o;", "function f",
		},
		"body in an escape string": {
			`CREATE FUNCTION f(uid UUID) RETURNS TEXT AS E'SELECT email FROM customers WHERE name <> \'x\'' LANGUAGE sql;`,
			"SELECT f($1);", "function f",
		},
		"PL/pgSQL body with a tag": {
			"CREATE FUNCTION f(uid UUID) RETURNS TEXT LANGUAGE plpgsql AS $body$\nDECLARE e text;\nBEGIN\n" +
				"  SELECT email INTO e FROM customers WHERE id = uid;\n  RETURN e;\nEND;\n$body$;",
			"SELECT f($1);", "function f",
		},
		"PERFORM": {"CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN PERFORM 1 FROM customers; END $$;", "SELECT f();", "function f"},
		"BEGIN ATOMIC body": {
			"CREATE FUNCTION f(uid UUID) RETURNS TEXT LANGUAGE sql\nBEGIN ATOMIC\n  SELECT CASE WHEN uid IS NULL THEN 'none' END;\n" +
				"  SELECT email FROM customers WHERE id = uid;\nEND;",
			"SELECT f($1);", "function f",
		},
		"RETURN body": {
			"CREATE FUNCTION f(uid UUID) RETURNS TEXT LANGUAGE sql RETURN (SELECT email FROM customers WHERE id = uid);",
			"SELECT f($1);", "function f",
		},
		"set-returning function in FROM": {
			"CREATE FUNCTION f(uid UUID) RETURNS SETOF customers AS $$ SELECT * FROM customers WHERE id = uid $$ LANGUAGE sql;",
			"SELECT * FROM f($1);", "function f",
		},
		"procedure": {"CREATE PROCEDURE f(uid UUID) LANGUAGE sql AS $$ DELETE FROM customers WHERE id = uid $$;", "CALL f($1);", "function f"},
		"function over a function": {
			"CREATE FUNCTION g(uid UUID) RETURNS TEXT AS $$ SELECT email FROM customers WHERE id = uid $$ LANGUAGE sql;\n" +
				"CREATE FUNCTION f(uid UUID) RETURNS TEXT AS $$ SELECT g(uid) $$ LANGUAGE sql;",
			"SELECT f($1);", "function f",
		},
		"function over a view": {
			"CREATE VIEW v AS SELECT * FROM customers;\nCREATE FUNCTION f(uid UUID) RETURNS TEXT AS $$ SELECT email FROM v WHERE id = uid $$ LANGUAGE sql;",
			"SELECT f($1);", "function f",
		},
		// Определения одного имени из разных миграций складываются: правило не следит за DROP и OR REPLACE.
		"function defined twice": {
			"CREATE FUNCTION f() RETURNS bigint AS $$ SELECT count(*) FROM customers $$ LANGUAGE sql;\n" +
				"CREATE OR REPLACE FUNCTION f() RETURNS bigint AS $$ SELECT count(*) FROM orders $$ LANGUAGE sql;",
			"SELECT f();", "function f",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := failures(t, edits{objects: tc.schema + "\n", orderQueries: "-- name: Probe :one\n" + tc.query + "\n"})
			requireFailures(t, got, through(tc.via))
		})
	}

	runCases(t, []ruleCase{
		{
			name: "direct query next to the view",
			edits: edits{
				objects:      "CREATE VIEW v AS SELECT * FROM customers;\n",
				orderQueries: "SELECT * FROM v;\nSELECT email FROM customers;\n",
			},
			want: []string{"table customers is queried by domains customer, order: " + whyTables},
		},
		{
			name: "two domains through objects",
			edits: edits{
				objects: "CREATE VIEW v AS SELECT * FROM customers;\n" +
					"CREATE FUNCTION f() RETURNS bigint AS $$ SELECT count(*) FROM customers $$ LANGUAGE sql;\n",
				"internal/domain/billing/billing.go":                      goSrc("billing"),
				"internal/domain/billing/infra/postgres/queries/bill.sql": "SELECT f();\n",
				orderQueries: "SELECT * FROM v;\n",
			},
			want: []string{
				"table customers is queried by domains billing, customer, order: " + whyTables +
					" (billing through function f; order through view v)",
			},
		},
		{
			name: "objects over own tables",
			edits: edits{
				objects: "CREATE VIEW open_orders AS SELECT * FROM orders WHERE note IS NULL;\n" +
					"CREATE FUNCTION order_count() RETURNS bigint AS $$ SELECT count(*) FROM orders $$ LANGUAGE sql;\n",
				orderQueries: "SELECT * FROM open_orders;\nSELECT order_count();\n",
			},
		},
		{
			name: "objects that no domain queries",
			edits: edits{objects: "CREATE VIEW report AS SELECT * FROM orders o JOIN customers c ON c.id = o.customer_id;\n" +
				"CREATE FUNCTION report_count() RETURNS bigint AS $$ SELECT count(*) FROM orders JOIN customers ON true $$ LANGUAGE sql;\n"},
		},
		{
			name: "trigger function",
			edits: edits{objects: "CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER AS $$\nBEGIN\n    NEW.updated_at = NOW();\n" +
				"    RETURN NEW;\nEND;\n$$ LANGUAGE plpgsql;\n\n" +
				"CREATE TRIGGER customers_updated_at BEFORE UPDATE ON customers FOR EACH ROW EXECUTE FUNCTION set_updated_at();\n"},
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
		{`SELECT a FROM public . t1 JOIN "public" . "T2" ON true`, []string{"t1", "t2"}},
		{"SELECT $$FROM t2$$, $x$ JOIN t3 $x$ FROM t1", []string{"t1"}},
		{`SELECT 'FROM t2', E'it\'s FROM t3' FROM t1`, []string{"t1"}},
		{"/* a /* b */ FROM t2 */ SELECT a FROM t1 -- JOIN t3\n", []string{"t1"}},
		{"SELECT $tag$ ; FROM t2 $tag$ FROM t1 WHERE b = $1", []string{"t1"}},
		{"SELECT 1", nil},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()
			if got := readRefs(lexSQL(tc.sql)).tables; !slices.Equal(got, tc.want) {
				t.Fatalf("tables = %v, want %v", got, tc.want)
			}
		})
	}
}
