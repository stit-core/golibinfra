package order

import "time"

// Order — заказ покупателя.
type Order struct {
	ID         string
	CustomerID string
	Note       string
	CreatedAt  time.Time
}
