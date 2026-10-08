package order

import (
	"context"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/idkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"

	"example.org/shop/internal/platform/identity"
)

// Repository — хранилище заказов.
type Repository interface {
	Insert(ctx context.Context, o *Order) error
}

// CustomerReader — порт к домену customer.
type CustomerReader interface {
	Exists(ctx context.Context, id string) (bool, error)
}

// Deps — зависимости usecase.
type Deps struct {
	fx.In

	Repo      Repository
	Customers CustomerReader
	Events    outboxkit.Publisher
	Tx        pgkit.Transactor
	Clock     clockkit.Clock
}

// Usecase — операции над заказами.
type Usecase struct{ d Deps }

// NewUsecase собирает usecase.
func NewUsecase(d Deps) *Usecase { return &Usecase{d: d} }

// Place оформляет заказ вызывающего.
func (uc *Usecase) Place(ctx context.Context, note string) (*Order, error) {
	caller, _ := identity.From(ctx)
	if ok, err := uc.d.Customers.Exists(ctx, caller.UserID); err != nil || !ok {
		return nil, ErrCustomerNotFound
	}
	o := &Order{ID: idkit.NewV7(), CustomerID: caller.UserID, Note: note, CreatedAt: uc.d.Clock.Now()}
	return o, uc.d.Tx.WithTx(ctx, func(ctx context.Context) error {
		if err := uc.d.Repo.Insert(ctx, o); err != nil {
			return err
		}
		return uc.d.Events.Publish(ctx, "order.events.placed", Placed{ID: o.ID, At: o.CreatedAt})
	})
}
