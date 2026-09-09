package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// Pipeline — воронка продаж.
type Pipeline struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Sort         int    `json:"sort"`
	IsMain       bool   `json:"is_main"`
	IsUnsortedOn bool   `json:"is_unsorted_on"`
	IsArchive    bool   `json:"is_archive"`
	AccountID    int64  `json:"account_id"`
}

// PipelineInput — тело POST /leads/pipelines, PATCH /leads/pipelines/{id}.
type PipelineInput struct {
	ID           int64          `json:"id,omitempty"`
	Name         string         `json:"name,omitempty"`
	Sort         int            `json:"sort,omitempty"`
	IsMain       *bool          `json:"is_main,omitempty"`
	IsUnsortedOn *bool          `json:"is_unsorted_on,omitempty"`
	RequestID    string         `json:"request_id,omitempty"`
	Embedded     map[string]any `json:"_embedded,omitempty"` // statuses при создании
}

// Status — этап воронки.
type Status struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Sort       int    `json:"sort"`
	IsEditable bool   `json:"is_editable"`
	PipelineID int64  `json:"pipeline_id"`
	Color      string `json:"color"`
	Type       int    `json:"type"`
	AccountID  int64  `json:"account_id"`
}

// StatusInput — тело upsert статусов воронки.
type StatusInput struct {
	ID        int64  `json:"id,omitempty"`
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
func (c *Client) UpdatePipeline(ctx context.Context, acc Account, id int64, in PipelineInput) (Pipeline, error) {
	return sendOne[Pipeline](ctx, c, acc, http.MethodPatch, "pipelines.update", fmt.Sprintf("/api/v4/leads/pipelines/%d", id), in)
}

// UpsertStatuses — POST /api/v4/leads/pipelines/{pipeline_id}/statuses (create/update).
func (c *Client) UpsertStatuses(ctx context.Context, acc Account, pipelineID int64, in []StatusInput) ([]Status, error) {
	path := fmt.Sprintf("/api/v4/leads/pipelines/%d/statuses", pipelineID)
	return sendBatch[StatusInput, Status](ctx, c, acc, http.MethodPost, "statuses.upsert", path, "statuses", in)
}

// CustomField — дополнительное поле.
type CustomField struct {
	ID               int64             `json:"id"`
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
	ID    int64  `json:"id,omitempty"`
	Value string `json:"value"`
	Sort  int    `json:"sort,omitempty"`
	Code  string `json:"code,omitempty"`
}

// CustomFieldInput — тело POST/PATCH /{entity}/custom_fields.
type CustomFieldInput struct {
	ID        int64             `json:"id,omitempty"`
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

// ListCustomFields — GET /api/v4/{entity}/custom_fields
// (entity ∈ leads|contacts|companies|customers|catalogs/{id}).
func (c *Client) ListCustomFields(ctx context.Context, acc Account, entity string, p ListParams) (Page[CustomField], error) {
	e, err := customFieldsEntity(entity)
	if err != nil {
		return Page[CustomField]{}, err
	}
	return listEntities[CustomField](ctx, c, acc, "custom_fields.list", "/api/v4/"+e+"/custom_fields", "custom_fields", p.values())
}

// CreateCustomFields — POST /api/v4/{entity}/custom_fields (batch).
func (c *Client) CreateCustomFields(ctx context.Context, acc Account, entity string, in []CustomFieldInput) ([]CustomField, error) {
	e, err := customFieldsEntity(entity)
	if err != nil {
		return nil, err
	}
	return sendBatch[CustomFieldInput, CustomField](ctx, c, acc, http.MethodPost, "custom_fields.create", "/api/v4/"+e+"/custom_fields", "custom_fields", in)
}

// UpdateCustomFields — PATCH /api/v4/{entity}/custom_fields (batch).
func (c *Client) UpdateCustomFields(ctx context.Context, acc Account, entity string, in []CustomFieldInput) ([]CustomField, error) {
	e, err := customFieldsEntity(entity)
	if err != nil {
		return nil, err
	}
	return sendBatch[CustomFieldInput, CustomField](ctx, c, acc, http.MethodPatch, "custom_fields.update", "/api/v4/"+e+"/custom_fields", "custom_fields", in)
}

// DeleteCustomField — DELETE /api/v4/{entity}/custom_fields/{id}.
func (c *Client) DeleteCustomField(ctx context.Context, acc Account, entity string, id int64) error {
	e, err := customFieldsEntity(entity)
	if err != nil {
		return err
	}
	return c.sendNoContent(ctx, acc, http.MethodDelete, "custom_fields.delete", fmt.Sprintf("/api/v4/%s/custom_fields/%d", e, id), nil)
}

// ListCustomFieldGroups — GET /api/v4/{entity}/custom_fields/groups.
func (c *Client) ListCustomFieldGroups(ctx context.Context, acc Account, entity string) (Page[CustomFieldGroup], error) {
	e, err := customFieldsEntity(entity)
	if err != nil {
		return Page[CustomFieldGroup]{}, err
	}
	return listEntities[CustomFieldGroup](ctx, c, acc, "custom_field_groups.list", "/api/v4/"+e+"/custom_fields/groups", "custom_field_groups", nil)
}

// CreateCustomFieldGroups — POST /api/v4/{entity}/custom_fields/groups (batch).
func (c *Client) CreateCustomFieldGroups(ctx context.Context, acc Account, entity string, in []CustomFieldGroupInput) ([]CustomFieldGroup, error) {
	e, err := customFieldsEntity(entity)
	if err != nil {
		return nil, err
	}
	return sendBatch[CustomFieldGroupInput, CustomFieldGroup](ctx, c, acc, http.MethodPost, "custom_field_groups.create", "/api/v4/"+e+"/custom_fields/groups", "custom_field_groups", in)
}

// UpdateCustomFieldGroup — PATCH /api/v4/{entity}/custom_fields/groups/{id}.
func (c *Client) UpdateCustomFieldGroup(ctx context.Context, acc Account, entity, id string, in CustomFieldGroupInput) (CustomFieldGroup, error) {
	e, err := customFieldsEntity(entity)
	if err != nil {
		return CustomFieldGroup{}, err
	}
	return sendOne[CustomFieldGroup](ctx, c, acc, http.MethodPatch, "custom_field_groups.update", "/api/v4/"+e+"/custom_fields/groups/"+seg(id), in)
}

// DeleteCustomFieldGroup — DELETE /api/v4/{entity}/custom_fields/groups/{id}.
func (c *Client) DeleteCustomFieldGroup(ctx context.Context, acc Account, entity, id string) error {
	e, err := customFieldsEntity(entity)
	if err != nil {
		return err
	}
	return c.sendNoContent(ctx, acc, http.MethodDelete, "custom_field_groups.delete", "/api/v4/"+e+"/custom_fields/groups/"+seg(id), nil)
}

// TagCreateInput — тело POST/PATCH /{entity}/tags.
type TagCreateInput struct {
	ID        int64  `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Color     string `json:"color,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// ListTags — GET /api/v4/{entity}/tags (entity ∈ leads|contacts|companies|customers).
func (c *Client) ListTags(ctx context.Context, acc Account, entity string, p ListParams) (Page[Tag], error) {
	e, err := entitySegment(entity)
	if err != nil {
		return Page[Tag]{}, err
	}
	return listEntities[Tag](ctx, c, acc, "tags.list", "/api/v4/"+e+"/tags", "tags", p.values())
}

// CreateTags — POST /api/v4/{entity}/tags (batch до 50).
func (c *Client) CreateTags(ctx context.Context, acc Account, entity string, in []TagCreateInput) ([]Tag, error) {
	e, err := entitySegment(entity)
	if err != nil {
		return nil, err
	}
	return sendBatch[TagCreateInput, Tag](ctx, c, acc, http.MethodPost, "tags.create", "/api/v4/"+e+"/tags", "tags", in)
}

// UpdateTags — PATCH /api/v4/{entity}/tags (batch).
func (c *Client) UpdateTags(ctx context.Context, acc Account, entity string, in []TagCreateInput) ([]Tag, error) {
	e, err := entitySegment(entity)
	if err != nil {
		return nil, err
	}
	return sendBatch[TagCreateInput, Tag](ctx, c, acc, http.MethodPatch, "tags.update", "/api/v4/"+e+"/tags", "tags", in)
}

// LossReason — причина отказа.
type LossReason struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Sort      int    `json:"sort"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// ListLossReasons — GET /api/v4/leads/loss_reasons.
func (c *Client) ListLossReasons(ctx context.Context, acc Account, p ListParams) (Page[LossReason], error) {
	return listEntities[LossReason](ctx, c, acc, "loss_reasons.list", "/api/v4/leads/loss_reasons", "loss_reasons", p.values())
}

// Source — источник.
type Source struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	PipelineID int64          `json:"pipeline_id"`
	ExternalID any            `json:"external_id"`
	Default    bool           `json:"default"`
	OriginCode string         `json:"origin_code"`
	Services   []any          `json:"services"`
	Embedded   map[string]any `json:"_embedded,omitempty"`
}

// SourceInput — тело POST/PATCH /sources.
type SourceInput struct {
	ID         int64  `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	PipelineID int64  `json:"pipeline_id,omitempty"`
	ExternalID any    `json:"external_id,omitempty"`
	Default    *bool  `json:"default,omitempty"`
	OriginCode string `json:"origin_code,omitempty"`
	Services   []any  `json:"services,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
}

// idRef — элемент тела batch-запроса по id (delete, users activate/deactivate).
type idRef struct {
	ID int64 `json:"id"`
}

func idRefs(ids []int64) []idRef {
	out := make([]idRef, len(ids))
	for i, id := range ids {
		out[i] = idRef{ID: id}
	}
	return out
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
func (c *Client) DeleteSources(ctx context.Context, acc Account, ids []int64) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "sources.delete", "/api/v4/sources", idRefs(ids))
}

// WebsiteButton — CRM Plugin (кнопка на сайте). PipelineID и Script приходят
// null, пока асинхронное создание не завершилось (CreationStatus).
type WebsiteButton struct {
	AccountID                   int64    `json:"account_id,omitempty"`
	SourceID                    int64    `json:"source_id,omitempty"`
	ButtonID                    int64    `json:"button_id,omitempty"`
	IsDuplicationControlEnabled bool     `json:"is_duplication_control_enabled,omitempty"`
	Name                        string   `json:"name,omitempty"`
	CreationStatus              string   `json:"creation_status,omitempty"` // created | creation_pending
	PipelineID                  *int64   `json:"pipeline_id,omitempty"`
	Script                      *string  `json:"script,omitempty"`
	TrustedWebsites             []string `json:"trusted_websites,omitempty"`
}

// WebsiteButtonInput — тело POST /api/v4/website_buttons (одиночный объект).
// IsUsedInApp обязателен, если создаём кнопку без доверенных доменов.
type WebsiteButtonInput struct {
	PipelineID      int64    `json:"pipeline_id"`
	TrustedWebsites []string `json:"trusted_websites,omitempty"`
	IsUsedInApp     bool     `json:"is_used_in_app,omitempty"`
}

// WebsiteButtonUpdate — тело PATCH /api/v4/website_buttons/{source_id}.
type WebsiteButtonUpdate struct {
	TrustedWebsites *TrustedWebsitesEdit `json:"trusted_websites,omitempty"`
}

// TrustedWebsitesEdit — доверенные домены добавляются вложенным add (wire-контракт amoCRM).
type TrustedWebsitesEdit struct {
	Add []string `json:"add,omitempty"`
}

// ListWebsiteButtons — GET /api/v4/website_buttons (with=scripts отдаёт script).
func (c *Client) ListWebsiteButtons(ctx context.Context, acc Account, p ListParams) (Page[WebsiteButton], error) {
	return listEntities[WebsiteButton](ctx, c, acc, "website_buttons.list", "/api/v4/website_buttons", "website_buttons", p.values())
}

// GetWebsiteButton — GET /api/v4/website_buttons/{source_id}.
func (c *Client) GetWebsiteButton(ctx context.Context, acc Account, sourceID int64) (WebsiteButton, error) {
	return getOne[WebsiteButton](ctx, c, acc, "website_buttons.get", fmt.Sprintf("/api/v4/website_buttons/%d", sourceID), nil)
}

// CreateWebsiteButton — POST /api/v4/website_buttons (202: ответ несёт только
// source_id и trusted_websites, кнопка создаётся асинхронно).
func (c *Client) CreateWebsiteButton(ctx context.Context, acc Account, in WebsiteButtonInput) (WebsiteButton, error) {
	return sendOne[WebsiteButton](ctx, c, acc, http.MethodPost, "website_buttons.create", "/api/v4/website_buttons", in)
}

// UpdateWebsiteButton — PATCH /api/v4/website_buttons/{source_id}.
func (c *Client) UpdateWebsiteButton(ctx context.Context, acc Account, sourceID int64, in WebsiteButtonUpdate) (WebsiteButton, error) {
	path := fmt.Sprintf("/api/v4/website_buttons/%d", sourceID)
	return sendOne[WebsiteButton](ctx, c, acc, http.MethodPatch, "website_buttons.update", path, in)
}

// ConnectOnlineChat — POST /api/v4/website_buttons/{source_id}/online_chat
// (202, без тела ответа; один онлайн-чат на воронку).
func (c *Client) ConnectOnlineChat(ctx context.Context, acc Account, sourceID int64, in map[string]any) error {
	path := fmt.Sprintf("/api/v4/website_buttons/%d/online_chat", sourceID)
	return c.sendNoContent(ctx, acc, http.MethodPost, "online_chat.connect", path, in)
}

// User — пользователь аккаунта.
type User struct {
	ID     int64          `json:"id"`
	Name   string         `json:"name"`
	Email  string         `json:"email"`
	Lang   string         `json:"lang"`
	Rights map[string]any `json:"rights"`
}

// UserInput — тело POST /api/v4/users.
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
	ID     int64          `json:"id"`
	Name   string         `json:"name"`
	Rights map[string]any `json:"rights"`
}

// RoleInput — тело POST/PATCH /roles.
type RoleInput struct {
	ID        int64          `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Rights    map[string]any `json:"rights,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

// ListUsers — GET /api/v4/users.
func (c *Client) ListUsers(ctx context.Context, acc Account, p ListParams) (Page[User], error) {
	return listEntities[User](ctx, c, acc, "users.list", "/api/v4/users", "users", p.values())
}

// CreateUsers — POST /api/v4/users (batch до 10).
func (c *Client) CreateUsers(ctx context.Context, acc Account, in []UserInput) ([]User, error) {
	return sendBatch[UserInput, User](ctx, c, acc, http.MethodPost, "users.create", "/api/v4/users", "users", in)
}

// ActivateUsers — POST /api/v4/users/activate (batch по id в теле; 202 без тела).
func (c *Client) ActivateUsers(ctx context.Context, acc Account, ids []int64) error {
	return c.sendNoContent(ctx, acc, http.MethodPost, "users.activate", "/api/v4/users/activate", idRefs(ids))
}

// DeactivateUsers — POST /api/v4/users/deactivate (batch по id в теле; 204 без тела).
func (c *Client) DeactivateUsers(ctx context.Context, acc Account, ids []int64) error {
	return c.sendNoContent(ctx, acc, http.MethodPost, "users.deactivate", "/api/v4/users/deactivate", idRefs(ids))
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
func (c *Client) UpdateRole(ctx context.Context, acc Account, id int64, in RoleInput) (Role, error) {
	return sendOne[Role](ctx, c, acc, http.MethodPatch, "roles.update", fmt.Sprintf("/api/v4/roles/%d", id), in)
}

// DeleteRole — DELETE /api/v4/roles/{id}.
func (c *Client) DeleteRole(ctx context.Context, acc Account, id int64) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "roles.delete", fmt.Sprintf("/api/v4/roles/%d", id), nil)
}

// CustomerStatus — этап покупателя.
type CustomerStatus struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Sort       int    `json:"sort"`
	IsDefault  bool   `json:"is_default"`
	Color      string `json:"color"`
	Type       int    `json:"type"`
	Conditions []any  `json:"conditions"`
	AccountID  int64  `json:"account_id"`
}

// CustomerStatusInput — тело POST/PATCH /customers/statuses.
type CustomerStatusInput struct {
	ID        int64  `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Sort      int    `json:"sort,omitempty"`
	Color     string `json:"color,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// Segment — сегмент покупателей.
type Segment struct {
	ID                          int64              `json:"id"`
	Name                        string             `json:"name"`
	Color                       string             `json:"color"`
	CustomersCount              int                `json:"customers_count"`
	CustomFieldsValues          []CustomFieldValue `json:"custom_fields_values"`
	AvailableProductsPriceTypes []any              `json:"available_products_price_types"`
	CreatedAt                   int64              `json:"created_at"`
	UpdatedAt                   int64              `json:"updated_at"`
	AccountID                   int64              `json:"account_id"`
}

// SegmentInput — тело POST/PATCH /customers/segments.
type SegmentInput struct {
	ID                 int64              `json:"id,omitempty"`
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
func (c *Client) UpdateCustomerStatus(ctx context.Context, acc Account, id int64, in CustomerStatusInput) (CustomerStatus, error) {
	return sendOne[CustomerStatus](ctx, c, acc, http.MethodPatch, "customer_statuses.update", fmt.Sprintf("/api/v4/customers/statuses/%d", id), in)
}

// DeleteCustomerStatus — DELETE /api/v4/customers/statuses/{id}.
func (c *Client) DeleteCustomerStatus(ctx context.Context, acc Account, id int64) error {
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
func (c *Client) UpdateSegment(ctx context.Context, acc Account, id int64, in SegmentInput) (Segment, error) {
	return sendOne[Segment](ctx, c, acc, http.MethodPatch, "segments.update", fmt.Sprintf("/api/v4/customers/segments/%d", id), in)
}

// DeleteSegment — DELETE /api/v4/customers/segments/{id}.
func (c *Client) DeleteSegment(ctx context.Context, acc Account, id int64) error {
	return c.sendNoContent(ctx, acc, http.MethodDelete, "segments.delete", fmt.Sprintf("/api/v4/customers/segments/%d", id), nil)
}
