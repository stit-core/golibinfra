// Package postgres — хранилище заказов на sqlc.
package postgres

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/pgkit"

	"example.org/shop/internal/domain/order"
	"example.org/shop/internal/domain/order/infra/postgres/db"
)

// Repository — заказы в Postgres.
type Repository struct{ q *db.Queries }

// NewRepository собирает хранилище.
func NewRepository(d *pgkit.DB) *Repository { return &Repository{q: db.New(d)} }

// Insert пишет заказ.
func (r *Repository) Insert(ctx context.Context, o *order.Order) error {
	return r.q.InsertOrder(ctx, db.Order{ID: o.ID, CustomerID: o.CustomerID, Note: o.Note})
}
