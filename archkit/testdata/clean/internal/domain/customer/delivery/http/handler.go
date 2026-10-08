// Package http — HTTP-вход домена customer.
package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"example.org/shop/internal/domain/customer"
)

// Usecase — порт входа.
type Usecase interface {
	Get(ctx context.Context, id string) (*customer.Customer, error)
}

// Handler — хендлеры операций.
type Handler struct{ uc Usecase }

// NewHandler собирает хендлер.
func NewHandler(uc Usecase) *Handler { return &Handler{uc: uc} }

// Register регистрирует операции домена.
func Register(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{OperationID: "getCustomer", Method: http.MethodGet, Path: "/api/customers/{id}"}, h.get)
}

func (h *Handler) get(ctx context.Context, in *struct {
	ID string `path:"id"`
},
) (*struct{ Body *customer.Customer }, error) {
	c, err := h.uc.Get(ctx, in.ID)
	return &struct{ Body *customer.Customer }{Body: c}, err
}
