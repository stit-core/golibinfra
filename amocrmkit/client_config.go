package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// account-config: pipelines/statuses, custom_fields/groups, tags, loss_reasons,
// sources/buttons/online_chat, users/roles, customer statuses/segments.
// Покрытие — 40 слотов реестра _shared/integrations/amocrm (секции config).

// --- Pipelines + statuses ---

// Pipeline — воронка продаж.
type Pipeline struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Sort         int    `json:"sort"`
	IsMain       bool   `json:"is_main"`
	IsUnsortedOn bool   `json:"is_unsorted_on"`
	IsArchive    bool   `json:"is_archive"`
	AccountID    int    `json:"account_id"`
}

// PipelineInput — тело POST /leads/pipelines, PATCH /leads/pipelines/{id}.
type PipelineInput struct {
	ID           int            `json:"id,omitempty"`
	Name         string         `json:"name,omitempty"`
	Sort         int            `json:"sort,omitempty"`
	IsMain       *bool          `json:"is_main,omitempty"`
	IsUnsortedOn *bool          `json:"is_unsorted_on,omitempty"`
	RequestID    string         `json:"request_id,omitempty"`
	Embedded     map[string]any `json:"_embedded,omitempty"` // statuses при создании
}

// Status — этап воронки.
type Status struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Sort       int    `json:"sort"`
	IsEditable bool   `json:"is_editable"`
	PipelineID int    `json:"pipeline_id"`
	Color      string `json:"color"`
	Type       int    `json:"type"`
	AccountID  int    `json:"account_id"`
}

// StatusInput — тело upsert статусов воронки.
type StatusInput struct {
	ID        int    `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Sort      int    `json:"sort,omitempty"`
	Color     string `json:"color,omitempty"`
	Type      int    `json:"type,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// ListPipelines — GET /api/v4/leads/pipelines.
func (c *Client) ListPipelines(ctx context.Context, acc Account) (Page[Pipeline], error) {
	return listEntities[Pipeline](ctx, c, acc, "pipelines.list", "/api/v4/leads/pipelines", "pipelines", nil)
}

// CreatePipelines — POST /api/v4/leads/pipelines (batch до 50).
func (c *Client) CreatePipelines(ctx context.Context, acc Account, in []PipelineInput) ([]Pipeline, error) {
	return sendBatch[PipelineInput, Pipeline](ctx, c, acc, http.MethodPost, "pipelines.create", "/api/v4/leads/pipelines", "pipelines", in)
}

// UpdatePipeline — PATCH /api/v4/leads/pipelines/{id}.
func (c *Client) UpdatePipeline(ctx context.Context, acc Account, id int, in PipelineInput) (Pipeline, error) {
	return sendOne[Pipeline](ctx, c, acc, http.MethodPatch, "pipelines.update", fmt.Sprintf("/api/v4/leads/pipelines/%d", id), in)
}

// UpsertStatuses — POST /api/v4/leads/pipelines/{pipeline_id}/statuses (create/update).
func (c *Client) UpsertStatuses(ctx context.Context, acc Account, pipelineID int, in []StatusInput) ([]Status, error) {
	path := fmt.Sprintf("/api/v4/leads/pipelines/%d/statuses", pipelineID)
	return sendBatch[StatusInput, Status](ctx, c, acc, http.MethodPost, "statuses.upsert", path, "statuses", in)
}

// --- Custom fields + groups (entity: leads|contacts|companies|customers|catalogs/{id}) ---

// CustomField — дополнительное поле.
type CustomField struct {
	ID               int               `json:"id"`
	Name             string            `json:"name"`
	Code             string            `json:"code"`
	Sort             int               `json:"sort"`
	Type             string            `json:"type"`
	EntityType       string            `json:"entity_type"`
	IsPredefined     bool              `json:"is_predefined"`
	IsAPIOnly        bool              `json:"is_api_only"`
	GroupID          string            `json:"group_id"`
	Enums            []CustomFieldEnum `json:"enums"`
	RequiredStatuses []map[string]any  `json:"required_statuses"`
}

// CustomFieldEnum — вариант значения select/multiselect-поля.
type CustomFieldEnum struct {
	ID    int    `json:"id,omitempty"`
	Value string `json:"value"`
	Sort  int    `json:"sort,omitempty"`
	Code  string `json:"code,omitempty"`
}

// CustomFieldInput — тело POST/PATCH /{entity}/custom_fields.
type CustomFieldInput struct {
	ID        int               `json:"id,omitempty"`
	Name      string            `json:"name,omitempty"`
	Type      string            `json:"type,omitempty"`
	Code      string            `json:"code,omitempty"`
	Sort      int               `json:"sort,omitempty"`
	GroupID   string            `json:"group_id,omitempty"`
	Enums     []CustomFieldEnum `json:"enums,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// CustomFieldGroup — группа дополнительных полей.
type CustomFieldGroup struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	IsPredefined bool   `json:"is_predefined"`
	Type         string `json:"type"`
	EntityType   string `json:"entity_type"`
	Sort         int    `json:"sort"`
}

// CustomFieldGroupInput — тело POST/PATCH групп.
type CustomFieldGroupInput struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Sort      int    `json:"sort,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// ListCustomFields — GET /api/v4/{entity}/custom_fields.
func (c *Client) ListCustomFields(ctx context.Context, acc Account, entity string, p ListParams) (Page[CustomField], error) {
	return listEntities[CustomField](ctx, c, acc, "custom_fields.list", fmt.Sprintf("/api/v4/%s/custom_fields", entity), "custom_fields", p.values())
}

// CreateCustomFields — POST /api/v4/{entity}/custom_fields (batch).
func (c *Client) CreateCustomFields(ctx context.Context, acc Account, entity string, in []CustomFieldInput) ([]CustomField, error) {
	return sendBatch[CustomFieldInput, CustomField](ctx, c, acc, http.MethodPost, "custom_fields.create", fmt.Sprintf("/api/v4/%s/custom_fields", entity), "custom_fields", in)
}

// UpdateCustomFields — PATCH /api/v4/{entity}/custom_fields (batch).
func (c *Client) UpdateCustomFields(ctx context.Context, acc Account, entity string, in []CustomFieldInput) ([]CustomField, error) {
	return sendBatch[CustomFieldInput, CustomField](ctx, c, acc, http.MethodPatch, "custom_fields.update", fmt.Sprintf("/api/v4/%s/custom_fields", entity), "custom_fields", in)
}

// DeleteCustomField — DELETE /api/v4/{entity}/custom_fields/{id}.
func (c *Client) DeleteCustomField(ctx context.Context, acc Account, entity string, id int) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "custom_fields.delete", fmt.Sprintf("/api/v4/%s/custom_fields/%d", entity, id), nil)
}

// ListCustomFieldGroups — GET /api/v4/{entity}/custom_fields/groups.
func (c *Client) ListCustomFieldGroups(ctx context.Context, acc Account, entity string) (Page[CustomFieldGroup], error) {
	return listEntities[CustomFieldGroup](ctx, c, acc, "custom_field_groups.list", fmt.Sprintf("/api/v4/%s/custom_fields/groups", entity), "custom_field_groups", nil)
}

// CreateCustomFieldGroups — POST /api/v4/{entity}/custom_fields/groups (batch).
func (c *Client) CreateCustomFieldGroups(ctx context.Context, acc Account, entity string, in []CustomFieldGroupInput) ([]CustomFieldGroup, error) {
	return sendBatch[CustomFieldGroupInput, CustomFieldGroup](ctx, c, acc, http.MethodPost, "custom_field_groups.create", fmt.Sprintf("/api/v4/%s/custom_fields/groups", entity), "custom_field_groups", in)
}

// UpdateCustomFieldGroup — PATCH /api/v4/{entity}/custom_fields/groups/{id}.
func (c *Client) UpdateCustomFieldGroup(ctx context.Context, acc Account, entity, id string, in CustomFieldGroupInput) (CustomFieldGroup, error) {
	return sendOne[CustomFieldGroup](ctx, c, acc, http.MethodPatch, "custom_field_groups.update", fmt.Sprintf("/api/v4/%s/custom_fields/groups/%s", entity, id), in)
}

// DeleteCustomFieldGroup — DELETE /api/v4/{entity}/custom_fields/groups/{id}.
func (c *Client) DeleteCustomFieldGroup(ctx context.Context, acc Account, entity, id string) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "custom_field_groups.delete", fmt.Sprintf("/api/v4/%s/custom_fields/groups/%s", entity, id), nil)
}

// --- Tags (entity: leads|contacts|companies|customers) ---

// TagCreateInput — тело POST/PATCH /{entity}/tags.
type TagCreateInput struct {
	ID        int    `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Color     string `json:"color,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// ListTags — GET /api/v4/{entity}/tags.
func (c *Client) ListTags(ctx context.Context, acc Account, entity string, p ListParams) (Page[Tag], error) {
	return listEntities[Tag](ctx, c, acc, "tags.list", fmt.Sprintf("/api/v4/%s/tags", entity), "tags", p.values())
}

// CreateTags — POST /api/v4/{entity}/tags (batch до 50).
func (c *Client) CreateTags(ctx context.Context, acc Account, entity string, in []TagCreateInput) ([]Tag, error) {
	return sendBatch[TagCreateInput, Tag](ctx, c, acc, http.MethodPost, "tags.create", fmt.Sprintf("/api/v4/%s/tags", entity), "tags", in)
}

// UpdateTags — PATCH /api/v4/{entity}/tags (batch).
func (c *Client) UpdateTags(ctx context.Context, acc Account, entity string, in []TagCreateInput) ([]Tag, error) {
	return sendBatch[TagCreateInput, Tag](ctx, c, acc, http.MethodPatch, "tags.update", fmt.Sprintf("/api/v4/%s/tags", entity), "tags", in)
}

// --- Loss reasons (read-only) ---

// LossReason — причина отказа.
type LossReason struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Sort      int    `json:"sort"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// ListLossReasons — GET /api/v4/leads/loss_reasons.
func (c *Client) ListLossReasons(ctx context.Context, acc Account, p ListParams) (Page[LossReason], error) {
	return listEntities[LossReason](ctx, c, acc, "loss_reasons.list", "/api/v4/leads/loss_reasons", "loss_reasons", p.values())
}

// --- Sources + website buttons + online chat ---

// Source — источник.
type Source struct {
	ID         int            `json:"id"`
	Name       string         `json:"name"`
	PipelineID int            `json:"pipeline_id"`
	ExternalID any            `json:"external_id"`
	Default    bool           `json:"default"`
	OriginCode string         `json:"origin_code"`
	Services   []any          `json:"services"`
	Embedded   map[string]any `json:"_embedded,omitempty"`
}

// SourceInput — тело POST/PATCH /sources.
type SourceInput struct {
	ID         int    `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	PipelineID int    `json:"pipeline_id,omitempty"`
	ExternalID any    `json:"external_id,omitempty"`
	Default    *bool  `json:"default,omitempty"`
	OriginCode string `json:"origin_code,omitempty"`
	Services   []any  `json:"services,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
}

// idRef — элемент тела batch-DELETE по id.
type idRef struct {
	ID int `json:"id"`
}

// ListSources — GET /api/v4/sources.
func (c *Client) ListSources(ctx context.Context, acc Account, p ListParams) (Page[Source], error) {
	return listEntities[Source](ctx, c, acc, "sources.list", "/api/v4/sources", "sources", p.values())
}

// CreateSources — POST /api/v4/sources (batch до 50).
func (c *Client) CreateSources(ctx context.Context, acc Account, in []SourceInput) ([]Source, error) {
	return sendBatch[SourceInput, Source](ctx, c, acc, http.MethodPost, "sources.create", "/api/v4/sources", "sources", in)
}

// UpdateSources — PATCH /api/v4/sources (batch до 50).
func (c *Client) UpdateSources(ctx context.Context, acc Account, in []SourceInput) ([]Source, error) {
	return sendBatch[SourceInput, Source](ctx, c, acc, http.MethodPatch, "sources.update", "/api/v4/sources", "sources", in)
}

// DeleteSources — DELETE /api/v4/sources (batch по id в теле).
func (c *Client) DeleteSources(ctx context.Context, acc Account, ids []int) error {
	body := make([]idRef, len(ids))
	for i, id := range ids {
		body[i] = idRef{ID: id}
	}
	return c.sendNoContent(ctx, acc, http.MethodDelete, "sources.delete", "/api/v4/sources", body)
}

// WebsiteButton — конфиг кнопки онлайн-чата на сайте.
type WebsiteButton struct {
	Source         map[string]any `json:"source,omitempty"`
	TrustedDomains []string       `json:"trusted_domains,omitempty"`
	OriginCode     string         `json:"origin_code,omitempty"`
}

// ListWebsiteButtons — GET /api/v4/sources/web_chat_button.
func (c *Client) ListWebsiteButtons(ctx context.Context, acc Account) (WebsiteButton, error) {
	return getOne[WebsiteButton](ctx, c, acc, "website_buttons.list", "/api/v4/sources/web_chat_button", nil)
}

// CreateWebsiteButton — POST /api/v4/sources/web_chat_button.
func (c *Client) CreateWebsiteButton(ctx context.Context, acc Account, in WebsiteButton) (WebsiteButton, error) {
	return sendOne[WebsiteButton](ctx, c, acc, http.MethodPost, "website_buttons.create", "/api/v4/sources/web_chat_button", in)
}

// UpdateWebsiteButton — PATCH /api/v4/sources/web_chat_button.
func (c *Client) UpdateWebsiteButton(ctx context.Context, acc Account, in WebsiteButton) (WebsiteButton, error) {
	return sendOne[WebsiteButton](ctx, c, acc, http.MethodPatch, "website_buttons.update", "/api/v4/sources/web_chat_button", in)
}

// ConnectOnlineChat — POST /api/v4/sources/{id}/online_chat.
func (c *Client) ConnectOnlineChat(ctx context.Context, acc Account, sourceID int, in map[string]any) (map[string]any, error) {
	path := fmt.Sprintf("/api/v4/sources/%d/online_chat", sourceID)
	return sendOne[map[string]any](ctx, c, acc, http.MethodPost, "online_chat.connect", path, in)
}

// --- Users + roles ---

// User — пользователь аккаунта.
type User struct {
	ID     int            `json:"id"`
	Name   string         `json:"name"`
	Email  string         `json:"email"`
	Lang   string         `json:"lang"`
	Rights map[string]any `json:"rights"`
}

// UserInput — тело POST /users.
type UserInput struct {
	Name      string         `json:"name,omitempty"`
	Email     string         `json:"email,omitempty"`
	Password  string         `json:"password,omitempty"`
	Lang      string         `json:"lang,omitempty"`
	Rights    map[string]any `json:"rights,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

// Role — роль доступа.
type Role struct {
	ID     int            `json:"id"`
	Name   string         `json:"name"`
	Rights map[string]any `json:"rights"`
}

// RoleInput — тело POST/PATCH /roles.
type RoleInput struct {
	ID        int            `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Rights    map[string]any `json:"rights,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

// activeFlag — тело PATCH /users/{id} для активации/деактивации.
type activeFlag struct {
	IsActive bool `json:"is_active"`
}

// ListUsers — GET /api/v4/users.
func (c *Client) ListUsers(ctx context.Context, acc Account, p ListParams) (Page[User], error) {
	return listEntities[User](ctx, c, acc, "users.list", "/api/v4/users", "users", p.values())
}

// CreateUsers — POST /api/v4/users (batch до 10).
func (c *Client) CreateUsers(ctx context.Context, acc Account, in []UserInput) ([]User, error) {
	return sendBatch[UserInput, User](ctx, c, acc, http.MethodPost, "users.create", "/api/v4/users", "users", in)
}

// ActivateUser — PATCH /api/v4/users/{id} (is_active=true).
func (c *Client) ActivateUser(ctx context.Context, acc Account, id int) (User, error) {
	return sendOne[User](ctx, c, acc, http.MethodPatch, "users.activate", fmt.Sprintf("/api/v4/users/%d", id), activeFlag{IsActive: true})
}

// DeactivateUser — PATCH /api/v4/users/{id} (is_active=false).
func (c *Client) DeactivateUser(ctx context.Context, acc Account, id int) (User, error) {
	return sendOne[User](ctx, c, acc, http.MethodPatch, "users.deactivate", fmt.Sprintf("/api/v4/users/%d", id), activeFlag{IsActive: false})
}

// ListRoles — GET /api/v4/roles.
func (c *Client) ListRoles(ctx context.Context, acc Account, p ListParams) (Page[Role], error) {
	return listEntities[Role](ctx, c, acc, "roles.list", "/api/v4/roles", "roles", p.values())
}

// CreateRoles — POST /api/v4/roles (batch).
func (c *Client) CreateRoles(ctx context.Context, acc Account, in []RoleInput) ([]Role, error) {
	return sendBatch[RoleInput, Role](ctx, c, acc, http.MethodPost, "roles.create", "/api/v4/roles", "roles", in)
}

// UpdateRole — PATCH /api/v4/roles/{id}.
func (c *Client) UpdateRole(ctx context.Context, acc Account, id int, in RoleInput) (Role, error) {
	return sendOne[Role](ctx, c, acc, http.MethodPatch, "roles.update", fmt.Sprintf("/api/v4/roles/%d", id), in)
}

// DeleteRole — DELETE /api/v4/roles/{id}.
func (c *Client) DeleteRole(ctx context.Context, acc Account, id int) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "roles.delete", fmt.Sprintf("/api/v4/roles/%d", id), nil)
}

// --- Customer statuses + segments ---

// CustomerStatus — этап покупателя.
type CustomerStatus struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Sort       int    `json:"sort"`
	IsDefault  bool   `json:"is_default"`
	Color      string `json:"color"`
	Type       int    `json:"type"`
	Conditions []any  `json:"conditions"`
	AccountID  int    `json:"account_id"`
}

// CustomerStatusInput — тело POST/PATCH /customers/statuses.
type CustomerStatusInput struct {
	ID        int    `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Sort      int    `json:"sort,omitempty"`
	Color     string `json:"color,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// Segment — сегмент покупателей.
type Segment struct {
	ID                          int                `json:"id"`
	Name                        string             `json:"name"`
	Color                       string             `json:"color"`
	CustomersCount              int                `json:"customers_count"`
	CustomFieldsValues          []CustomFieldValue `json:"custom_fields_values"`
	AvailableProductsPriceTypes []any              `json:"available_products_price_types"`
	CreatedAt                   int64              `json:"created_at"`
	UpdatedAt                   int64              `json:"updated_at"`
	AccountID                   int                `json:"account_id"`
}

// SegmentInput — тело POST/PATCH /customers/segments.
type SegmentInput struct {
	ID                 int                `json:"id,omitempty"`
	Name               string             `json:"name,omitempty"`
	Color              string             `json:"color,omitempty"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
}

// ListCustomerStatuses — GET /api/v4/customers/statuses.
func (c *Client) ListCustomerStatuses(ctx context.Context, acc Account) (Page[CustomerStatus], error) {
	return listEntities[CustomerStatus](ctx, c, acc, "customer_statuses.list", "/api/v4/customers/statuses", "statuses", nil)
}

// CreateCustomerStatuses — POST /api/v4/customers/statuses (batch до 30).
func (c *Client) CreateCustomerStatuses(ctx context.Context, acc Account, in []CustomerStatusInput) ([]CustomerStatus, error) {
	return sendBatch[CustomerStatusInput, CustomerStatus](ctx, c, acc, http.MethodPost, "customer_statuses.create", "/api/v4/customers/statuses", "statuses", in)
}

// UpdateCustomerStatus — PATCH /api/v4/customers/statuses/{id}.
func (c *Client) UpdateCustomerStatus(ctx context.Context, acc Account, id int, in CustomerStatusInput) (CustomerStatus, error) {
	return sendOne[CustomerStatus](ctx, c, acc, http.MethodPatch, "customer_statuses.update", fmt.Sprintf("/api/v4/customers/statuses/%d", id), in)
}

// DeleteCustomerStatus — DELETE /api/v4/customers/statuses/{id}.
func (c *Client) DeleteCustomerStatus(ctx context.Context, acc Account, id int) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "customer_statuses.delete", fmt.Sprintf("/api/v4/customers/statuses/%d", id), nil)
}

// ListSegments — GET /api/v4/customers/segments.
func (c *Client) ListSegments(ctx context.Context, acc Account, p ListParams) (Page[Segment], error) {
	return listEntities[Segment](ctx, c, acc, "segments.list", "/api/v4/customers/segments", "segments", p.values())
}

// CreateSegments — POST /api/v4/customers/segments (batch до 100).
func (c *Client) CreateSegments(ctx context.Context, acc Account, in []SegmentInput) ([]Segment, error) {
	return sendBatch[SegmentInput, Segment](ctx, c, acc, http.MethodPost, "segments.create", "/api/v4/customers/segments", "segments", in)
}

// UpdateSegment — PATCH /api/v4/customers/segments/{id}.
func (c *Client) UpdateSegment(ctx context.Context, acc Account, id int, in SegmentInput) (Segment, error) {
	return sendOne[Segment](ctx, c, acc, http.MethodPatch, "segments.update", fmt.Sprintf("/api/v4/customers/segments/%d", id), in)
}

// DeleteSegment — DELETE /api/v4/customers/segments/{id}.
func (c *Client) DeleteSegment(ctx context.Context, acc Account, id int) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "segments.delete", fmt.Sprintf("/api/v4/customers/segments/%d", id), nil)
}
