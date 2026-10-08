CREATE TABLE orders (
    id          UUID PRIMARY KEY,
    customer_id UUID NOT NULL REFERENCES customers (id),
    note        TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS order_items (
    id       UUID PRIMARY KEY,
    order_id UUID NOT NULL REFERENCES orders (id),
    sku      TEXT NOT NULL,
    qty      INT NOT NULL
);
