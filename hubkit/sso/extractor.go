package sso

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// ErrNoSessionCookie — ни один слот сессии не заполнен → 401.
var ErrNoSessionCookie = errors.New("hubkit/sso: no session cookie")

// Extractor восстанавливает Identity защищённого запроса: cookie → Verifier
// (ES256/JWKS) → Identity в ctx. Сигнатура Extract совместима с
// httpkit.AuthExtractor.
type Extractor struct {
	verifier *Verifier
	cookie   string

	customer       *CustomerVerifier
	customerCookie string
}

// NewExtractor — однослотовый extractor (только сотрудник). Контур заказчика
// подключается через NewDualExtractor.
func NewExtractor(v *Verifier, cfg Config) *Extractor {
	return &Extractor{verifier: v, cookie: cookieOr(cfg.Cookie, DefaultCookie)}
}

// NewDualExtractor — extractor обоих контуров. nil customer (пустой
// HUB_SSO_CUSTOMER_JWKS_URL) → контур заказчика выключен и поведение
// совпадает с NewExtractor.
func NewDualExtractor(v *Verifier, customer *CustomerVerifier, cfg Config) *Extractor {
	e := NewExtractor(v, cfg)
	if customer != nil {
		e.customer = customer
		e.customerCookie = cookieOr(cfg.CustomerCookie, DefaultCustomerCookie)
	}
	return e
}

// Extract читает слоты сессии по приоритету (сотрудник, затем заказчик) и
// кладёт в ctx Identity и субъект для scope'а идемпотентности httpkit.
//
// Приоритет за сотрудником: сотрудник и заказчик могут оказаться в одном
// браузере, и внутренний контур главнее; протухший employee-токен не отменяет
// валидную сессию заказчика.
func (e *Extractor) Extract(r *http.Request) (context.Context, error) {
	ctx := r.Context()
	id, err := verifySlot(r, e.cookie, e.verifier)
	if err == nil {
		return e.authenticated(ctx, id), nil
	}
	if e.customer != nil {
		cid, cerr := verifySlot(r, e.customerCookie, e.customer.Verifier)
		if cerr == nil {
			return e.authenticated(ctx, cid), nil
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
	// Недоступный JWKS — не «неверные credentials»: 401 разлогинил бы всех
	// пользователей на время простоя хаба, httpkit по этой обёртке отвечает 503.
	if errors.Is(err, ErrJWKSUnavailable) {
		return ctx, fmt.Errorf("%w: %w", httpkit.ErrAuthUnavailable, err)
	}
	return ctx, err
}

// extractSlot — extractor схемы одного контура: только его cookie, ошибки — как у Extract.
func (e *Extractor) extractSlot(r *http.Request, cookie string, v *Verifier) (context.Context, error) {
	ctx := r.Context()
	id, err := verifySlot(r, cookie, v)
	switch {
	case err == nil:
		return e.authenticated(ctx, id), nil
	case errors.Is(err, errSlotEmpty):
		return ctx, ErrNoSessionCookie
	case errors.Is(err, ErrJWKSUnavailable):
		return ctx, fmt.Errorf("%w: %w", httpkit.ErrAuthUnavailable, err)
	}
	return ctx, err
}

func (e *Extractor) authenticated(ctx context.Context, id Identity) context.Context {
	return httpkit.ContextWithSubject(WithIdentity(ctx, id), id.SubjectKey())
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
