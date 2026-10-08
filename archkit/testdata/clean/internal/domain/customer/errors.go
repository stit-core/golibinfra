package customer

import "errors"

// ErrNotFound — покупателя нет.
var ErrNotFound = errors.New("customer: not found")
