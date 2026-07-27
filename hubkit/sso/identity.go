package sso

import (
	"context"
	"errors"
	"strings"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Kind* — контуры identity экосистемы: сотрудник (сессия id.stit.tech) и
// заказчик (сессия api.hub.stit.tech). Контур — первое, что проверяет доменная
// политика: набор полей и допустимых действий у них разный.
const (
	KindEmployee = "employee"
	KindCustomer = "customer"
)

var (
	// ErrNoIdentity — в ctx нет Identity (запрос не прошёл Extractor) → 401.
	ErrNoIdentity = errors.New("hubkit/sso: identity not in context")
	// ErrWrongKind — identity другого контура → 403 (не 401: сессия валидна).
	ErrWrongKind = errors.New("hubkit/sso: identity of another contour")
)

// Identity — principal hub-сессии, восстановленный Extractor'ом из session-JWT.
// EmployeeID — канонический hub-UUID сотрудника (claim employee_id), Subject —
// Authentik subject у сотрудника и customer_account_id у заказчика. Семантику
// Role kit не трактует — это делает сервис.
//
// Kind разделяет два контура; поля CompanyID/AccountRole/Products заполнены
// только у заказчика. Проверяй контур ДО применения любой доменной политики:
// правила, написанные под сотрудника, к заказчику не применимы.
type Identity struct {
	EmployeeID string
	Subject    string
	Username   string // логин (напр. n.sycheva)
	Name       string // ФИО (напр. Наталья Сычева)
	Email      string
	Role       string
	SID        string // id сессии (для онлайн-отзыва критичных операций)

	// Kind — контур сессии: KindEmployee | KindCustomer. Пустой claim читается
	// как employee — токены, выпущенные до появления второго контура, валидны.
	Kind string
	// CompanyID — компания заказчика (hub-UUID); у сотрудника пуст.
	CompanyID string
	// AccountRole — роль заказчика внутри своей компании (member|admin). К правам
	// в экосистеме отношения не имеет — гейтит разделы его собственного ЛК.
	AccountRole string
	// Products — продукты, куда пущен заказчик (`deal`, `telemost`, …); у
	// сотрудника пуст (доступ ко всем).
	Products []string
}

// IsEmployee — сотрудник (в т.ч. токен без claim kind).
func (i Identity) IsEmployee() bool { return i.Kind == "" || i.Kind == KindEmployee }

// IsCustomer — заказчик.
func (i Identity) IsCustomer() bool { return i.Kind == KindCustomer }

// CanUseProduct — пускать ли identity в продукт по имени (`deal`, `telemost`,
// `task`, `calendar`). Сотруднику — всегда да; заказчику — только если продукт
// перечислен в claim products (пустой набор = никуда: барьер закрыт по
// умолчанию). Это доступ к продукту целиком, а не к конкретной сущности —
// видимость сущностей решает правило customer_visible на стороне продукта.
func (i Identity) CanUseProduct(name string) bool {
	if i.IsEmployee() {
		return true
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, p := range i.Products {
		if strings.EqualFold(strings.TrimSpace(p), name) {
			return true
		}
	}
	return false
}

// RequireEmployee — гейт сотруднического контура для доменных политик: Identity
// из ctx + проверка Kind (ErrNoIdentity → 401, ErrWrongKind → 403).
func RequireEmployee(ctx context.Context) (Identity, error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return Identity{}, ErrNoIdentity
	}
	if !id.IsEmployee() {
		return Identity{}, ErrWrongKind
	}
	return id, nil
}

// RequireCustomer — гейт контура заказчика (симметрично RequireEmployee).
func RequireCustomer(ctx context.Context) (Identity, error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return Identity{}, ErrNoIdentity
	}
	if !id.IsCustomer() {
		return Identity{}, ErrWrongKind
	}
	return id, nil
}

type identityCtxKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(Identity)
	return id, ok
}

func MustIdentityFrom(ctx context.Context) Identity {
	id, ok := IdentityFrom(ctx)
	if !ok {
		panic("hubkit/sso: identity not in context")
	}
	return id
}

// RegisterIdentityLogAttr пробрасывает employee_id сессии в каждый log-record
// как user_id (fallback — subject, если employee_id пуст). Дёргается один раз на
// старте (Module делает это сам).
func RegisterIdentityLogAttr() {
	logkit.RegisterAttr(func(ctx context.Context) (string, any, bool) {
		id, ok := IdentityFrom(ctx)
		if !ok {
			return "", nil, false
		}
		uid := id.EmployeeID
		if uid == "" {
			uid = id.Subject
		}
		return "user_id", uid, true
	})
}
