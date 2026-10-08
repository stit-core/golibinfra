package order

import "errors"

// ErrCustomerNotFound — заказ на покупателя, которого нет.
var ErrCustomerNotFound = errors.New("order: customer not found")
