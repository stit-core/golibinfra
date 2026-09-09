package sso

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"git.stit.tech/stit-core/golibinfra/hubkit/hubclaims"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Kind* — контуры identity экосистемы: сотрудник (сессия id.stit.tech) и
// заказчик (сессия api.hub.stit.tech). Значения — из hubkit/hubclaims.
const (
	KindEmployee = hubclaims.KindEmployee
	KindCustomer = hubclaims.KindCustomer
)

var (
	// ErrNoIdentity — в ctx нет Identity (запрос не прошёл Extractor) → 401.
	ErrNoIdentity = errors.New("hubkit/sso: identity not in context")
	// ErrWrongKind — identity другого контура → 403 (не 401: сессия валидна).
	ErrWrongKind = errors.New("hubkit/sso: identity of another contour")
)

// Identity — principal hub-сессии, восстановленный Extractor'ом из session-JWT.
// Непустой Subject — признак заполненной Identity: нулевое значение не проходит
// ни один гейт контура.
type Identity struct {
	// EmployeeID — канонический hub-UUID сотрудника (claim employee_id).
	EmployeeID string
	// Subject — Authentik subject у сотрудника, customer_account_id у заказчика.
	Subject string
	// Username — логин сотрудника (напр. n.sycheva).
	Username string
	// Name — ФИО.
	Name string
	// Email — почта принципала.
	Email string
	// Role — роль в экосистеме; семантику трактует сервис, не кит.
	Role string
	// SID — id сессии (для онлайн-отзыва критичных операций).
	SID string

	// Kind — контур сессии: KindEmployee | KindCustomer. Пустой claim токена
	// читается верификатором как employee.
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

// IsEmployee — заполненная identity сотруднического контура (в т.ч. токен без
// claim kind).
func (i Identity) IsEmployee() bool {
	return i.Subject != "" && (i.Kind == "" || i.Kind == KindEmployee)
}

// IsCustomer — заполненная identity контура заказчика.
func (i Identity) IsCustomer() bool { return i.Subject != "" && i.Kind == KindCustomer }

// SubjectKey — стабильный ключ принципала для scope'а идемпотентности и
// логов: контур + subject (subject'ы двух контуров живут в разных
// пространствах имён и могли бы совпасть).
func (i Identity) SubjectKey() string {
	if i.Subject == "" {
		return ""
	}
	kind := i.Kind
	if kind == "" {
		kind = KindEmployee
	}
	return kind + ":" + i.Subject
}

// CanUseProduct — пускать ли identity в продукт по имени (`deal`, `telemost`,
// `task`, `calendar`). Сотруднику — всегда да; заказчику — только если продукт
// перечислен в claim products (пустой набор = никуда: барьер закрыт по
// умолчанию). Это доступ к продукту целиком, а не к конкретной сущности —
// видимость сущностей решает правило customer_visible на стороне продукта.
func (i Identity) CanUseProduct(name string) bool {
	if i.IsEmployee() {
		return true
	}
	if !i.IsCustomer() {
		return false
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
	id, err := requireIdentity(ctx)
	if err != nil {
		return Identity{}, err
	}
	if !id.IsEmployee() {
		return Identity{}, ErrWrongKind
	}
	return id, nil
}

// RequireCustomer — гейт контура заказчика (симметрично RequireEmployee).
func RequireCustomer(ctx context.Context) (Identity, error) {
	id, err := requireIdentity(ctx)
	if err != nil {
		return Identity{}, err
	}
	if !id.IsCustomer() {
		return Identity{}, ErrWrongKind
	}
	return id, nil
}

// requireIdentity — нулевая Identity в ctx неотличима от «принципала нет»:
// гейт не должен принимать литерал Identity{} за сотрудника.
func requireIdentity(ctx context.Context) (Identity, error) {
	id, ok := IdentityFrom(ctx)
	if !ok || id.Subject == "" {
		return Identity{}, ErrNoIdentity
	}
	return id, nil
}

type identityCtxKey struct{}

// WithIdentity кладёт Identity в ctx (ключ неэкспортируемый — подделать нельзя).
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// IdentityFrom достаёт Identity из ctx; второй результат — была ли она положена.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(Identity)
	return id, ok
}

// MustIdentityFrom — Identity из ctx хендлера за security-гейтом; паникует,
// если её нет (значит операция не защищена, а код это предполагает).
func MustIdentityFrom(ctx context.Context) Identity {
	id, ok := IdentityFrom(ctx)
	if !ok {
		panic("hubkit/sso: identity not in context")
	}
	return id
}

// RegisterIdentityLogAttr пробрасывает employee_id сессии в каждый log-record
// как user_id (fallback — subject, если employee_id пуст); повторный вызов
// заменяет регистрацию, а не дублирует поле.
func RegisterIdentityLogAttr() {
	logkit.RegisterAttr("user_id", func(ctx context.Context) (slog.Value, bool) {
		id, ok := IdentityFrom(ctx)
		if !ok || id.Subject == "" {
			return slog.Value{}, false
		}
		uid := id.EmployeeID
		if uid == "" {
			uid = id.Subject
		}
		return slog.StringValue(uid), true
	})
}
