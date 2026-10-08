package customer

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"

	"example.org/shop/internal/platform/identity"
	"example.org/shop/pkg/paging"
)

const retention = 24 * time.Hour

// Repository — хранилище покупателей.
type Repository interface {
	Get(ctx context.Context, id string) (*Customer, error)
	List(ctx context.Context, page paging.Page, since time.Time) ([]*Customer, error)
}

// Deps — зависимости usecase.
type Deps struct {
	fx.In

	Repo  Repository
	Tx    pgkit.Transactor
	Clock clockkit.Clock
	Log   logkit.Logger
}

// Usecase — операции над покупателями.
type Usecase struct {
	repo  Repository
	clock clockkit.Clock
	log   logkit.Logger
}

// NewUsecase собирает usecase.
func NewUsecase(d Deps) *Usecase { return &Usecase{repo: d.Repo, clock: d.Clock, log: d.Log} }

// Get отдаёт покупателя вызывающему.
func (uc *Usecase) Get(ctx context.Context, id string) (*Customer, error) {
	caller, _ := identity.From(ctx)
	if d := (Policy{}).CanGet(ctx, caller, id); !d.IsAllowed {
		return nil, fmt.Errorf("customer: get: %s", d.Reason)
	}
	return uc.repo.Get(ctx, id)
}

// Recent — покупатели, созданные за последние сутки.
func (uc *Usecase) Recent(ctx context.Context, page paging.Page) ([]*Customer, error) {
	clock := uc.clock
	return uc.repo.List(ctx, page, clock.Now().Add(-retention))
}
