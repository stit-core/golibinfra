package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// leads + unsorted. Покрытие реестра _shared/integrations/amocrm:
// create_lead, update_lead, list_leads, create_unsorted_sip, create_unsorted_forms,
// accept_unsorted, decline_unsorted, link_unsorted.
// GET /leads/{id} в реестре нет: gateway отдаёт Get*-геттеры из локального зеркала
// (read-model), а не read-through к амо.

// Lead — объект сделки (GET-ответ).
type Lead struct {
	ID                 int                `json:"id"`
	Name               string             `json:"name"`
	Price              int                `json:"price"`
	ResponsibleUserID  int                `json:"responsible_user_id"`
	GroupID            int                `json:"group_id"`
	StatusID           int                `json:"status_id"`
	PipelineID         int                `json:"pipeline_id"`
	LossReasonID       *int               `json:"loss_reason_id"`
	CreatedBy          int                `json:"created_by"`
	UpdatedBy          int                `json:"updated_by"`
	CreatedAt          int64              `json:"created_at"`
	UpdatedAt          int64              `json:"updated_at"`
	ClosedAt           *int64             `json:"closed_at"`
	ClosestTaskAt      *int64             `json:"closest_task_at"`
	IsDeleted          bool               `json:"is_deleted"`
	LaborCost          *int               `json:"labor_cost"`
	Score              *int               `json:"score"`
	AccountID          int                `json:"account_id"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values"`
	Embedded           *LeadEmbedded      `json:"_embedded,omitempty"`
}

// LeadEmbedded — _embedded сделки.
type LeadEmbedded struct {
	Tags      []Tag        `json:"tags,omitempty"`
	Contacts  []ContactRef `json:"contacts,omitempty"`
	Companies []CompanyRef `json:"companies,omitempty"`
}

// LeadInput — тело POST/PATCH /leads (writable-поля + теги инкрементально).
type LeadInput struct {
	ID                 int                `json:"id,omitempty"` // обязателен при PATCH batch
	Name               string             `json:"name,omitempty"`
	Price              *int               `json:"price,omitempty"`
	StatusID           int                `json:"status_id,omitempty"`
	PipelineID         int                `json:"pipeline_id,omitempty"`
	ResponsibleUserID  int                `json:"responsible_user_id,omitempty"`
	LossReasonID       int                `json:"loss_reason_id,omitempty"`
	CreatedBy          *int               `json:"created_by,omitempty"`
	UpdatedBy          *int               `json:"updated_by,omitempty"`
	CreatedAt          int64              `json:"created_at,omitempty"`
	UpdatedAt          int64              `json:"updated_at,omitempty"`
	ClosedAt           int64              `json:"closed_at,omitempty"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values,omitempty"`
	TagsToAdd          []TagRef           `json:"tags_to_add,omitempty"`
	TagsToDelete       []TagRef           `json:"tags_to_delete,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
}

// ListLeads — GET /api/v4/leads.
func (c *Client) ListLeads(ctx context.Context, acc Account, p ListParams) (Page[Lead], error) {
	return listEntities[Lead](ctx, c, acc, "leads.list", "/api/v4/leads", "leads", p.values())
}

// CreateLeads — POST /api/v4/leads (batch до 250).
func (c *Client) CreateLeads(ctx context.Context, acc Account, in []LeadInput) ([]CreatedEntity, error) {
	return sendBatch[LeadInput, CreatedEntity](ctx, c, acc, http.MethodPost, "leads.create", "/api/v4/leads", "leads", in)
}

// UpdateLeads — PATCH /api/v4/leads (batch до 250; каждый элемент несёт id).
func (c *Client) UpdateLeads(ctx context.Context, acc Account, in []LeadInput) ([]CreatedEntity, error) {
	return sendBatch[LeadInput, CreatedEntity](ctx, c, acc, http.MethodPatch, "leads.update", "/api/v4/leads", "leads", in)
}

// LeadComplexInput — тело POST /api/v4/leads/complex: сделка + ≤1 контакт + ≤1 компания одним
// запросом с контролем дублей. Поля сделки — как у LeadInput; вложенные сущности — в _embedded.
type LeadComplexInput struct {
	Name               string               `json:"name,omitempty"`
	Price              *int                 `json:"price,omitempty"`
	StatusID           int                  `json:"status_id,omitempty"`
	PipelineID         int                  `json:"pipeline_id,omitempty"`
	ResponsibleUserID  int                  `json:"responsible_user_id,omitempty"`
	CustomFieldsValues []CustomFieldValue   `json:"custom_fields_values,omitempty"`
	TagsToAdd          []TagRef             `json:"tags_to_add,omitempty"`
	RequestID          string               `json:"request_id,omitempty"`
	Embedded           *LeadComplexEmbedded `json:"_embedded,omitempty"`
}

// LeadComplexEmbedded — вложенные сущности /complex: до 1 контакта и 1 компании на сделку (лимит amo),
// metadata — для неразобранного (category forms/sip).
type LeadComplexEmbedded struct {
	Contacts  []ComplexContact `json:"contacts,omitempty"`
	Companies []ComplexCompany `json:"companies,omitempty"`
	Metadata  map[string]any   `json:"metadata,omitempty"`
}

// ComplexContact — контакт внутри /complex (first/last/name + доп-поля; телефон/почта — через custom_fields).
type ComplexContact struct {
	Name               string             `json:"name,omitempty"`
	FirstName          string             `json:"first_name,omitempty"`
	LastName           string             `json:"last_name,omitempty"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values,omitempty"`
}

// ComplexCompany — компания внутри /complex.
type ComplexCompany struct {
	Name string `json:"name,omitempty"`
}

// ComplexResult — элемент ответа /complex. contact_id/company_id = null, если не создавались;
// request_id — массив (несколько при нескольких дублях одной сделки); merged=true — склейка с дублем.
type ComplexResult struct {
	ID        int      `json:"id"`
	ContactID *int     `json:"contact_id"`
	CompanyID *int     `json:"company_id"`
	RequestID []string `json:"request_id"`
	Merged    bool     `json:"merged"`
}

// CreateLeadsComplex — POST /api/v4/leads/complex (batch до 50; ответ — top-level массив, не _embedded).
func (c *Client) CreateLeadsComplex(ctx context.Context, acc Account, in []LeadComplexInput) ([]ComplexResult, error) {
	var out []ComplexResult
	err := c.doREST(ctx, restReq{acc: acc, method: http.MethodPost, path: "/api/v4/leads/complex", body: in, endpoint: "leads.complex"}, &out)
	return out, err
}

// --- Unsorted (неразобранное) ---

// Unsorted — объект неразобранного.
type Unsorted struct {
	UID        string `json:"uid"`
	SourceUID  string `json:"source_uid"`
	SourceName string `json:"source_name"`
	Category   string `json:"category"`
	PipelineID int    `json:"pipeline_id"`
	CreatedAt  int64  `json:"created_at"`
	AccountID  int    `json:"account_id"`
}

// UnsortedSIPInput — тело POST /leads/unsorted/sip.
type UnsortedSIPInput struct {
	SourceUID  string         `json:"source_uid,omitempty"`
	SourceName string         `json:"source_name,omitempty"`
	PipelineID int            `json:"pipeline_id,omitempty"`
	CreatedAt  int64          `json:"created_at,omitempty"`
	RequestID  string         `json:"request_id,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Embedded   map[string]any `json:"_embedded,omitempty"`
}

// UnsortedFormInput — тело POST /leads/unsorted/forms.
type UnsortedFormInput struct {
	SourceUID  string         `json:"source_uid,omitempty"`
	SourceName string         `json:"source_name,omitempty"`
	PipelineID int            `json:"pipeline_id,omitempty"`
	CreatedAt  int64          `json:"created_at,omitempty"`
	RequestID  string         `json:"request_id,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Embedded   map[string]any `json:"_embedded,omitempty"`
}

// CreateUnsortedSIP — POST /api/v4/leads/unsorted/sip (batch до 250).
func (c *Client) CreateUnsortedSIP(ctx context.Context, acc Account, in []UnsortedSIPInput) ([]Unsorted, error) {
	return sendBatch[UnsortedSIPInput, Unsorted](ctx, c, acc, http.MethodPost, "unsorted.sip", "/api/v4/leads/unsorted/sip", "unsorted", in)
}

// CreateUnsortedForms — POST /api/v4/leads/unsorted/forms (batch до 250).
func (c *Client) CreateUnsortedForms(ctx context.Context, acc Account, in []UnsortedFormInput) ([]Unsorted, error) {
	return sendBatch[UnsortedFormInput, Unsorted](ctx, c, acc, http.MethodPost, "unsorted.forms", "/api/v4/leads/unsorted/forms", "unsorted", in)
}

// UnsortedAcceptInput — тело accept (привязка к пользователю/статусу).
type UnsortedAcceptInput struct {
	UserID   int `json:"user_id,omitempty"`
	StatusID int `json:"status_id,omitempty"`
}

// UnsortedLinkInput — тело link (привязка к существующей сущности).
type UnsortedLinkInput struct {
	Link map[string]any `json:"link"`
}

// UnsortedActionResult — ответ accept/link (несёт привязанные сущности в _embedded).
type UnsortedActionResult struct {
	UID      string         `json:"uid,omitempty"`
	Embedded map[string]any `json:"_embedded,omitempty"`
}

// AcceptUnsorted — POST /api/v4/leads/unsorted/{uid}/accept.
func (c *Client) AcceptUnsorted(ctx context.Context, acc Account, uid string, in UnsortedAcceptInput) (UnsortedActionResult, error) {
	return sendOne[UnsortedActionResult](ctx, c, acc, http.MethodPost, "unsorted.accept", fmt.Sprintf("/api/v4/leads/unsorted/%s/accept", uid), in)
}

// DeclineUnsorted — DELETE /api/v4/leads/unsorted/{uid}/decline.
func (c *Client) DeclineUnsorted(ctx context.Context, acc Account, uid string) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "unsorted.decline", fmt.Sprintf("/api/v4/leads/unsorted/%s/decline", uid), nil)
}

// LinkUnsorted — POST /api/v4/leads/unsorted/{uid}/link.
func (c *Client) LinkUnsorted(ctx context.Context, acc Account, uid string, in UnsortedLinkInput) (UnsortedActionResult, error) {
	return sendOne[UnsortedActionResult](ctx, c, acc, http.MethodPost, "unsorted.link", fmt.Sprintf("/api/v4/leads/unsorted/%s/link", uid), in)
}
