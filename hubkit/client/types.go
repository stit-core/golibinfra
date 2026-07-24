package client

import "time"

// Company — каноническая компания хаба (company-backend, api.hub.stit.tech).
// Реквизиты — общая правда экосистемы (00-architecture §2).
type Company struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Slug         string         `json:"slug"`
	LegalName    string         `json:"legal_name,omitempty"`
	INN          string         `json:"inn,omitempty"`
	KPP          string         `json:"kpp,omitempty"`
	OGRN         string         `json:"ogrn,omitempty"`
	LegalAddress string         `json:"legal_address,omitempty"`
	BankDetails  map[string]any `json:"bank_details,omitempty"`
	Contacts     map[string]any `json:"contacts,omitempty"`
	Links        map[string]any `json:"links,omitempty"`
	Archived     bool           `json:"archived"`
	Version      int64          `json:"version"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	UpdatedSeq   int64          `json:"updated_seq"`
}

// CompanyInput — тело create/update компании.
type CompanyInput struct {
	Name         string         `json:"name"`
	LegalName    string         `json:"legal_name,omitempty"`
	INN          string         `json:"inn,omitempty"`
	KPP          string         `json:"kpp,omitempty"`
	OGRN         string         `json:"ogrn,omitempty"`
	LegalAddress string         `json:"legal_address,omitempty"`
	BankDetails  map[string]any `json:"bank_details,omitempty"`
	Contacts     map[string]any `json:"contacts,omitempty"`
	Links        map[string]any `json:"links,omitempty"`
	// Version — для оптимистичной блокировки на update (0 — без проверки).
	Version int64 `json:"version,omitempty"`
}

// Project — канонический проект хаба. Не существует без компании (company_id NOT NULL).
type Project struct {
	ID          string    `json:"id"`
	CompanyID   string    `json:"company_id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description,omitempty"`
	Archived    bool      `json:"archived"`
	Position    int64     `json:"position"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedSeq  int64     `json:"updated_seq"`
}

// ProjectInput — тело create/update проекта.
type ProjectInput struct {
	CompanyID   string `json:"company_id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     int64  `json:"version,omitempty"`
}

// Employee — каноническая проекция сотрудника хаба (employee-backend, id.stit.tech).
type Employee struct {
	ID         string    `json:"id"`
	Subject    string    `json:"subject"`
	Username   string    `json:"username"`
	FullName   string    `json:"full_name"`
	Email      string    `json:"email,omitempty"`
	Role       string    `json:"role,omitempty"`
	IsActive   bool      `json:"is_active"`
	Version    int64     `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	UpdatedSeq int64     `json:"updated_seq"`
	// DepartmentIDs — snapshot набора департаментов сотрудника (M2M хаба,
	// 00-architecture §2). Владелец — employee-backend; строкового поля
	// department в модели нет — принадлежность только через этот массив.
	// Всегда массив в фиде (пустой набор — []); старый сервер без поля →
	// nil (обратная совместимость: продукты со старым фидом не ломаются).
	DepartmentIDs []string `json:"department_ids,omitempty"`
}

// Visibility — per-service флаг видимости сущности (00-architecture §2).
type Visibility struct {
	Service    string `json:"service"`
	EntityKind string `json:"entity_kind"`
	EntityID   string `json:"entity_id"`
	Hidden     bool   `json:"hidden"`
	UpdatedSeq int64  `json:"updated_seq"`
}

// Tag — каноническая запись глобального словаря тегов (tag-backend,
// tags.hub.stit.tech). Дедуп по norm_name; name — написание первого автора
// экосистемы (00-architecture §5).
type Tag struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NormName string `json:"norm_name"`
}

// TagInput — тело write-through upsert тега (POST /api/tags).
type TagInput struct {
	Name string `json:"name"`
}

// EntityTagsSnapshot — snapshot принадлежности тегам одной hub-сущности из
// sync-фида entity-tags (company-backend, владелец глобальной принадлежности
// company/project — 00-architecture §5). Полный набор TagIDs на курсоре
// UpdatedSeq; потребитель применяет как replace. Ключ — (EntityKind, EntityID),
// не id. Пустой набор (все теги сняты) приезжает как TagIDs=[] обычной записью
// (курсор бампается только при фактическом diff'е набора). Продукт
// unmarshal'ит в этот тип элементы SyncFeed(ResourceEntityTags).
type EntityTagsSnapshot struct {
	EntityKind string   `json:"entity_kind"`
	EntityID   string   `json:"entity_id"`
	TagIDs     []string `json:"tag_ids"`
	UpdatedSeq int64    `json:"updated_seq"`
}

// Department — запись глобального справочника департаментов (employee-backend,
// id.stit.tech). Механика тегов: дедуп по norm_name, name — написание первого
// автора (00-architecture §2). Приезжает в sync-фиде ResourceDepartments.
type Department struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	NormName   string    `json:"norm_name"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedSeq int64     `json:"updated_seq"`
}

// ListParams — общие параметры листингов (пагинация/фильтр архива).
type ListParams struct {
	Limit           int
	Offset          int
	IncludeArchived bool
}
