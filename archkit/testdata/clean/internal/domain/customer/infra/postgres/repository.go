// Package postgres — хранилище покупателей на sqlc.
package postgres

import (
	"context"
	"time"

	"git.stit.tech/stit-core/golibinfra/pgkit"

	"example.org/shop/internal/domain/customer"
	"example.org/shop/internal/domain/customer/infra/postgres/db"
	"example.org/shop/pkg/paging"
)

// Repository — покупатели в Postgres.
type Repository struct{ q *db.Queries }

// NewRepository собирает хранилище.
func NewRepository(d *pgkit.DB) *Repository { return &Repository{q: db.New(d)} }

// Get читает покупателя.
func (r *Repository) Get(ctx context.Context, id string) (*customer.Customer, error) {
	row, err := r.q.GetCustomer(ctx, id)
	if pgkit.IsNoRows(err) {
		return nil, customer.ErrNotFound
	}
	return &customer.Customer{ID: row.ID, Email: row.Email, Name: row.Name}, err
}

// List читает страницу покупателей.
func (r *Repository) List(context.Context, paging.Page, time.Time) ([]*customer.Customer, error) {
	return nil, nil
}
