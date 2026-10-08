package customer

import (
	"net/mail"
	"time"

	"example.org/shop/internal/platform/identity"
)

// Customer — покупатель.
type Customer struct {
	ID        string
	Email     string
	Name      string
	CreatedAt time.Time
	CreatedBy identity.Identity
}

// ValidEmail — адрес разбирается как почтовый.
func ValidEmail(s string) bool {
	_, err := mail.ParseAddress(s)
	return err == nil
}
