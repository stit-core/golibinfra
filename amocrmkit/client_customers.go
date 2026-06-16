package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// customers + transactions. Реестр: create_customer, update_customer, list_customers,
// create_transaction, delete_transaction, list_transactions, adjust_bonus_points.

// Customer — объект покупателя (GET-ответ).
type Customer struct {
	ID                 int                `json:"id"`
	Name               string             `json:"name"`
	NextPrice          int                `json:"next_price"`
	NextDate           int64              `json:"next_date"`
	ResponsibleUserID  int                `json:"responsible_user_id"`
	StatusID           int                `json:"status_id"`
	Periodicity        int                `json:"periodicity"`
	CreatedBy          int                `json:"created_by"`
	UpdatedBy          int                `json:"updated_by"`
	CreatedAt          int64              `json:"created_at"`
	UpdatedAt          int64              `json:"updated_at"`
	ClosestTaskAt      *int64             `json:"closest_task_at"`
	IsDeleted          bool               `json:"is_deleted"`
	LTV                int                `json:"ltv"`
	PurchasesCount     int                `json:"purchases_count"`
	AverageCheck       int                `json:"average_check"`
	AccountID          int                `json:"account_id"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values"`
	Embedded           *CustomerEmbedded  `json:"_embedded,omitempty"`
}

// CustomerEmbedded — _embedded покупателя (по with=contacts,companies,catalog_elements).
type CustomerEmbedded struct {
	Tags            []Tag               `json:"tags,omitempty"`
	Contacts        []ContactRef        `json:"contacts,omitempty"`
	Companies       []CompanyRef        `json:"companies,omitempty"`
	Segments        []SegmentRef        `json:"segments,omitempty"`
	CatalogElements []CatalogElementRef `json:"catalog_elements,omitempty"`
}

// SegmentRef — элемент _embedded.segments покупателя.
type SegmentRef struct {
	ID int `json:"id"`
}

// CatalogElementRef — элемент _embedded.catalog_elements: id товара + metadata{catalog_id, quantity}.
type CatalogElementRef struct {
	ID       int `json:"id"`
	Metadata struct {
		CatalogID int `json:"catalog_id"`
		Quantity  int `json:"quantity"`
	} `json:"metadata"`
}

// CustomerInput — тело POST/PATCH /customers.
type CustomerInput struct {
	ID                 int                `json:"id,omitempty"`
	Name               string             `json:"name,omitempty"`
	NextPrice          *int               `json:"next_price,omitempty"`
	NextDate           int64              `json:"next_date,omitempty"`
	ResponsibleUserID  int                `json:"responsible_user_id,omitempty"`
	StatusID           int                `json:"status_id,omitempty"`
	Periodicity        int                `json:"periodicity,omitempty"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values,omitempty"`
	TagsToAdd          []TagRef           `json:"tags_to_add,omitempty"`
	TagsToDelete       []TagRef           `json:"tags_to_delete,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
}

// Transaction — объект покупки.
type Transaction struct {
	ID          int                  `json:"id"`
	Price       int                  `json:"price"`
	Comment     string               `json:"comment"`
	CompletedAt int64                `json:"completed_at"`
	CustomerID  int                  `json:"customer_id"`
	CreatedBy   int                  `json:"created_by"`
	UpdatedBy   int                  `json:"updated_by"`
	CreatedAt   int64                `json:"created_at"`
	UpdatedAt   int64                `json:"updated_at"`
	IsDeleted   bool                 `json:"is_deleted"`
	AccountID   int                  `json:"account_id"`
	Embedded    *TransactionEmbedded `json:"_embedded,omitempty"`
}

// TransactionEmbedded — _embedded транзакции: catalog_elements (привязанные товары).
type TransactionEmbedded struct {
	CatalogElements []CatalogElementRef `json:"catalog_elements,omitempty"`
}

// TransactionInput — тело POST /customers/{customer_id}/transactions.
type TransactionInput struct {
	Price              int                `json:"price"`
	Comment            string             `json:"comment,omitempty"`
	CompletedAt        int64              `json:"completed_at,omitempty"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
	Embedded           map[string]any     `json:"_embedded,omitempty"` // catalog_elements при привязке товаров
}

// BonusPointsInput — тело POST /customers/{id}/bonus_points (RU-only). earn XOR redeem.
type BonusPointsInput struct {
	Earn   int `json:"earn,omitempty"`
	Redeem int `json:"redeem,omitempty"`
}

// BonusPointsResult — ответ POST /bonus_points: новый баланс баллов лояльности.
type BonusPointsResult struct {
	BonusPoints int `json:"bonus_points"`
}

// CustomersMode — тело и ответ PATCH /api/v4/customers/mode: режим (segments|periodicity) + флаг включения модуля.
type CustomersMode struct {
	Mode      string `json:"mode"`
	IsEnabled bool   `json:"is_enabled"`
}

// ListCustomers — GET /api/v4/customers.
func (c *Client) ListCustomers(ctx context.Context, acc Account, p ListParams) (Page[Customer], error) {
	return listEntities[Customer](ctx, c, acc, "customers.list", "/api/v4/customers", "customers", p.values())
}

// CreateCustomers — POST /api/v4/customers (batch до 250).
func (c *Client) CreateCustomers(ctx context.Context, acc Account, in []CustomerInput) ([]CreatedEntity, error) {
	return sendBatch[CustomerInput, CreatedEntity](ctx, c, acc, http.MethodPost, "customers.create", "/api/v4/customers", "customers", in)
}

// UpdateCustomers — PATCH /api/v4/customers (batch до 250).
func (c *Client) UpdateCustomers(ctx context.Context, acc Account, in []CustomerInput) ([]CreatedEntity, error) {
	return sendBatch[CustomerInput, CreatedEntity](ctx, c, acc, http.MethodPatch, "customers.update", "/api/v4/customers", "customers", in)
}

// ListTransactions — GET /api/v4/customers/transactions.
func (c *Client) ListTransactions(ctx context.Context, acc Account, p ListParams) (Page[Transaction], error) {
	return listEntities[Transaction](ctx, c, acc, "transactions.list", "/api/v4/customers/transactions", "transactions", p.values())
}

// CreateTransactions — POST /api/v4/customers/{customer_id}/transactions (batch до 250).
func (c *Client) CreateTransactions(ctx context.Context, acc Account, customerID int, in []TransactionInput) ([]CreatedEntity, error) {
	path := fmt.Sprintf("/api/v4/customers/%d/transactions", customerID)
	return sendBatch[TransactionInput, CreatedEntity](ctx, c, acc, http.MethodPost, "transactions.create", path, "transactions", in)
}

// DeleteTransaction — DELETE /api/v4/customers/transactions/{id}.
func (c *Client) DeleteTransaction(ctx context.Context, acc Account, id int) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "transactions.delete", fmt.Sprintf("/api/v4/customers/transactions/%d", id), nil)
}

// AdjustBonusPoints — POST /api/v4/customers/{id}/bonus_points (RU-only). Возвращает новый баланс баллов.
func (c *Client) AdjustBonusPoints(ctx context.Context, acc Account, customerID int, in BonusPointsInput) (BonusPointsResult, error) {
	path := fmt.Sprintf("/api/v4/customers/%d/bonus_points", customerID)
	return sendOne[BonusPointsResult](ctx, c, acc, http.MethodPost, "customers.bonus_points", path, in)
}

// SetCustomersMode — PATCH /api/v4/customers/mode: включить/выключить модуль покупателей и задать режим
// (segments|periodicity). Требует прав администратора аккаунта.
func (c *Client) SetCustomersMode(ctx context.Context, acc Account, in CustomersMode) (CustomersMode, error) {
	return sendOne[CustomersMode](ctx, c, acc, http.MethodPatch, "customers.mode", "/api/v4/customers/mode", in)
}
