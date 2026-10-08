package module

import (
	"context"
	"errors"

	"example.org/shop/internal/domain/customer"
	"example.org/shop/internal/domain/order"
)

// customerReader — порт order.CustomerReader поверх usecase домена customer: его ошибки
// переводятся в исходы потребителя.
type customerReader struct{ uc *customer.Usecase }

func newCustomerReader(uc *customer.Usecase) order.CustomerReader { return &customerReader{uc: uc} }

func (a *customerReader) Exists(ctx context.Context, id string) (bool, error) {
	_, err := a.uc.Get(ctx, id)
	if errors.Is(err, customer.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
