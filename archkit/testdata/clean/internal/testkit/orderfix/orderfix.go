// Package orderfix — заказы для тестов.
package orderfix

import "example.org/shop/internal/domain/order"

// New — заказ с заполненными полями.
func New(id string) *order.Order { return &order.Order{ID: id, CustomerID: "c-1"} }
