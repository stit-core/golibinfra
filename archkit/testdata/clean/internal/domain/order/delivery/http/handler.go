// Package http — HTTP-вход домена order.
package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"example.org/shop/internal/domain/order"
)

// Usecase — порт входа.
type Usecase interface {
	Place(ctx context.Context, note string) (*order.Order, error)
}

// Handler — хендлеры операций.
type Handler struct{ uc Usecase }

// NewHandler собирает хендлер.
func NewHandler(uc Usecase) *Handler { return &Handler{uc: uc} }

// Register регистрирует операции домена.
func Register(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{OperationID: "placeOrder", Method: http.MethodPost, Path: "/api/orders"}, h.place)
}

func (h *Handler) place(ctx context.Context, in *struct {
	Body struct{ Note string }
},
) (*struct{ Body *order.Order }, error) {
	o, err := h.uc.Place(ctx, in.Body.Note)
	return &struct{ Body *order.Order }{Body: o}, err
}
