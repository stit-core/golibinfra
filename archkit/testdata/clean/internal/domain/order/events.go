package order

import "time"

// Placed — заказ оформлен.
type Placed struct {
	ID string
	At time.Time
}
