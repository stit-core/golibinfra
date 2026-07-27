package sso

import (
	"context"
	"errors"
	"net/http"
)

var ErrNoSessionCookie = errors.New("hubkit/sso: no session cookie")

// Extractor восстанавливает Identity защищённого запроса: cookie → Verifier
// (ES256/JWKS) → Identity в ctx. Сигнатура Extract совместима с
// httpkit.AuthExtractor.
//
// Слотов два: сотруднический (stit_session) и заказчика (stit_customer, если
// контур включён). Приоритет — за сотрудником: сотрудник и заказчик могут
// оказаться в одном браузере, и внутренний контур главнее. Протухший
// employee-токен не отменяет валидную сессию заказчика.
type Extractor struct {
	verifier *Verifier
	cookie   string

	customer       *CustomerVerifier
	customerCookie string
}

// NewExtractor — однослотовый extractor (только сотрудник). Контур заказчика
// подключается через NewDualExtractor.
func NewExtractor(v *Verifier, cfg Config) *Extractor {
	return &Extractor{verifier: v, cookie: cookieOr(cfg.Cookie, "stit_session")}
}

// NewDualExtractor — extractor обоих контуров. nil customer (пустой
// HUB_SSO_CUSTOMER_JWKS_URL) → контур заказчика выключен и поведение
// совпадает с NewExtractor.
func NewDualExtractor(v *Verifier, customer *CustomerVerifier, cfg Config) *Extractor {
	e := NewExtractor(v, cfg)
	if customer != nil {
		e.customer = customer
		e.customerCookie = cookieOr(cfg.CustomerCookie, "stit_customer")
	}
	return e
}

func (e *Extractor) Extract(r *http.Request) (context.Context, error) {
	ctx := r.Context()
	id, err := verifySlot(r, e.cookie, e.verifier)
	if err == nil {
		return WithIdentity(ctx, id), nil
	}
	if e.customer != nil {
		cid, cerr := verifySlot(r, e.customerCookie, e.customer.Verifier)
		if cerr == nil {
			return WithIdentity(ctx, cid), nil
		}
		// Ошибку приоритетного слота не затираем: «протухла рабочая сессия» —
		// более полезный диагноз, чем ошибка второго слота.
		if errors.Is(err, errSlotEmpty) {
			err = cerr
		}
	}
	if errors.Is(err, errSlotEmpty) {
		return ctx, ErrNoSessionCookie
	}
	return ctx, err
}

// errSlotEmpty — cookie слота нет или она пуста (внутреннее состояние: наружу
// уходит ErrNoSessionCookie, когда пусты все слоты).
var errSlotEmpty = errors.New("hubkit/sso: session slot empty")

func verifySlot(r *http.Request, name string, v *Verifier) (Identity, error) {
	c, err := r.Cookie(name)
	if err != nil || c.Value == "" {
		return Identity{}, errSlotEmpty
	}
	return v.Verify(r.Context(), c.Value)
}

func cookieOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}
