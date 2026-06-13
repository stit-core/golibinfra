package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// catalogs + elements. Реестр: create_catalogs, update_catalogs, list_catalogs,
// create_catalog_elements, update_catalog_elements, list_catalog_elements.

// Catalog — объект каталога (списки/товары).
type Catalog struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	Sort            int    `json:"sort"`
	CanAddElements  bool   `json:"can_add_elements"`
	CanShowInCards  bool   `json:"can_show_in_cards"`
	CanLinkMultiple bool   `json:"can_link_multiple"`
	CanBeDeleted    bool   `json:"can_be_deleted"`
	SDKWidgetCode   string `json:"sdk_widget_code"`
	AccountID       int    `json:"account_id"`
	CreatedBy       int    `json:"created_by"`
	UpdatedBy       int    `json:"updated_by"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// CatalogInput — тело POST/PATCH /catalogs.
type CatalogInput struct {
	ID             int    `json:"id,omitempty"`
	Name           string `json:"name,omitempty"`
	Type           string `json:"type,omitempty"`
	CanAddElements *bool  `json:"can_add_elements,omitempty"`
	CanShowInCards *bool  `json:"can_show_in_cards,omitempty"`
	Sort           int    `json:"sort,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
}

// CatalogElement — элемент каталога (товар/счёт).
type CatalogElement struct {
	ID                 int                `json:"id"`
	CatalogID          int                `json:"catalog_id"`
	Name               string             `json:"name"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values"`
	IsDeleted          bool               `json:"is_deleted"`
	AccountID          int                `json:"account_id"`
	CreatedBy          int                `json:"created_by"`
	UpdatedBy          int                `json:"updated_by"`
	CreatedAt          int64              `json:"created_at"`
	UpdatedAt          int64              `json:"updated_at"`
	InvoiceLink        string             `json:"invoice_link"`
}

// CatalogElementInput — тело POST/PATCH /catalogs/{id}/elements.
type CatalogElementInput struct {
	ID                 int                `json:"id,omitempty"`
	Name               string             `json:"name,omitempty"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
}

// ListCatalogs — GET /api/v4/catalogs.
func (c *Client) ListCatalogs(ctx context.Context, acc Account, p ListParams) (Page[Catalog], error) {
	return listEntities[Catalog](ctx, c, acc, "catalogs.list", "/api/v4/catalogs", "catalogs", p.values())
}

// CreateCatalogs — POST /api/v4/catalogs (batch до 250).
func (c *Client) CreateCatalogs(ctx context.Context, acc Account, in []CatalogInput) ([]CreatedEntity, error) {
	return sendBatch[CatalogInput, CreatedEntity](ctx, c, acc, http.MethodPost, "catalogs.create", "/api/v4/catalogs", "catalogs", in)
}

// UpdateCatalogs — PATCH /api/v4/catalogs (batch до 250).
func (c *Client) UpdateCatalogs(ctx context.Context, acc Account, in []CatalogInput) ([]CreatedEntity, error) {
	return sendBatch[CatalogInput, CreatedEntity](ctx, c, acc, http.MethodPatch, "catalogs.update", "/api/v4/catalogs", "catalogs", in)
}

// ListCatalogElements — GET /api/v4/catalogs/{catalog_id}/elements.
func (c *Client) ListCatalogElements(ctx context.Context, acc Account, catalogID int, p ListParams) (Page[CatalogElement], error) {
	path := fmt.Sprintf("/api/v4/catalogs/%d/elements", catalogID)
	return listEntities[CatalogElement](ctx, c, acc, "catalog_elements.list", path, "elements", p.values())
}

// CreateCatalogElements — POST /api/v4/catalogs/{catalog_id}/elements (batch до 250).
func (c *Client) CreateCatalogElements(ctx context.Context, acc Account, catalogID int, in []CatalogElementInput) ([]CreatedEntity, error) {
	path := fmt.Sprintf("/api/v4/catalogs/%d/elements", catalogID)
	return sendBatch[CatalogElementInput, CreatedEntity](ctx, c, acc, http.MethodPost, "catalog_elements.create", path, "elements", in)
}

// UpdateCatalogElements — PATCH /api/v4/catalogs/{catalog_id}/elements (batch до 250).
func (c *Client) UpdateCatalogElements(ctx context.Context, acc Account, catalogID int, in []CatalogElementInput) ([]CreatedEntity, error) {
	path := fmt.Sprintf("/api/v4/catalogs/%d/elements", catalogID)
	return sendBatch[CatalogElementInput, CreatedEntity](ctx, c, acc, http.MethodPatch, "catalog_elements.update", path, "elements", in)
}
