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
	Department string    `json:"department,omitempty"`
	Role       string    `json:"role,omitempty"`
	IsActive   bool      `json:"is_active"`
	Version    int64     `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	UpdatedSeq int64     `json:"updated_seq"`
}

// Visibility — per-service флаг видимости сущности (00-architecture §2).
type Visibility struct {
	Service    string `json:"service"`
	EntityKind string `json:"entity_kind"`
	EntityID   string `json:"entity_id"`
	Hidden     bool   `json:"hidden"`
	UpdatedSeq int64  `json:"updated_seq"`
}

// ListParams — общие параметры листингов (пагинация/фильтр архива).
type ListParams struct {
	Limit           int
	Offset          int
	IncludeArchived bool
}
