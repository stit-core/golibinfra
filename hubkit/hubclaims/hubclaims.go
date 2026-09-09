// Package hubclaims — контракт hub-session JWT: имена нестандартных claim'ов и
// контуры identity. Общий для стороны выпуска (hubkit/issuer) и стороны
// проверки (hubkit/sso); собственных зависимостей у пакета нет.
package hubclaims

// Имена нестандартных claim'ов hub-session JWT; sub/exp/iat/iss/aud —
// стандартные RFC 7519 и здесь не дублируются.
const (
	// EmployeeID — канонический hub-UUID сотрудника.
	EmployeeID = "employee_id"
	// Username — логин сотрудника.
	Username = "username"
	// Name — ФИО принципала.
	Name = "name"
	// Email — почта принципала.
	Email = "email"
	// Role — роль сотрудника в экосистеме.
	Role = "role"
	// SID — идентификатор сессии (адрес отзыва).
	SID = "sid"
	// Kind — контур сессии: KindEmployee | KindCustomer.
	Kind = "kind"
	// CompanyID — hub-UUID компании заказчика.
	CompanyID = "company_id"
	// AccountRole — роль заказчика внутри своей компании.
	AccountRole = "account_role"
	// Products — продукты, куда пущен заказчик.
	Products = "products"
)

// Значения claim'а Kind — контуры identity экосистемы.
const (
	// KindEmployee — сессия сотрудника (id.stit.tech); пустой claim читается так же.
	KindEmployee = "employee"
	// KindCustomer — сессия заказчика (api.hub.stit.tech).
	KindCustomer = "customer"
)
