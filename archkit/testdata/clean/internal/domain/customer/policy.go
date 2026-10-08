package customer

import (
	"context"

	"example.org/shop/internal/platform/identity"
	"example.org/shop/pkg/policy"
)

// Policy — кто вправе читать покупателя.
type Policy struct{}

// CanGet разрешает чтение себе.
func (Policy) CanGet(_ context.Context, id identity.Identity, customerID string) policy.Decision {
	if id.UserID != customerID {
		return policy.Deny("not the owner")
	}
	return policy.Allow()
}
