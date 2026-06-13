package amocrmkit

import (
	"context"
	"net/http"
)

// contacts + companies. Реестр: create_contact, update_contact, list_contacts,
// list_contact_chats, link_contact_chat, create_company, update_company, list_companies.

// Contact — объект контакта (GET-ответ).
type Contact struct {
	ID                 int                `json:"id"`
	Name               string             `json:"name"`
	FirstName          string             `json:"first_name"`
	LastName           string             `json:"last_name"`
	ResponsibleUserID  int                `json:"responsible_user_id"`
	GroupID            int                `json:"group_id"`
	CreatedBy          int                `json:"created_by"`
	UpdatedBy          int                `json:"updated_by"`
	CreatedAt          int64              `json:"created_at"`
	UpdatedAt          int64              `json:"updated_at"`
	ClosestTaskAt      *int64             `json:"closest_task_at"`
	IsDeleted          bool               `json:"is_deleted"`
	AccountID          int                `json:"account_id"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values"`
	Embedded           *ContactEmbedded   `json:"_embedded,omitempty"`
}

// ContactEmbedded — _embedded контакта.
type ContactEmbedded struct {
	Tags      []Tag        `json:"tags,omitempty"`
	Companies []CompanyRef `json:"companies,omitempty"`
}

// ContactInput — тело POST/PATCH /contacts.
type ContactInput struct {
	ID                 int                `json:"id,omitempty"`
	Name               string             `json:"name,omitempty"`
	FirstName          string             `json:"first_name,omitempty"`
	LastName           string             `json:"last_name,omitempty"`
	ResponsibleUserID  int                `json:"responsible_user_id,omitempty"`
	CreatedBy          *int               `json:"created_by,omitempty"`
	UpdatedBy          *int               `json:"updated_by,omitempty"`
	CreatedAt          int64              `json:"created_at,omitempty"`
	UpdatedAt          int64              `json:"updated_at,omitempty"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values,omitempty"`
	TagsToAdd          []TagRef           `json:"tags_to_add,omitempty"`
	TagsToDelete       []TagRef           `json:"tags_to_delete,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
}

// Company — объект компании (GET-ответ).
type Company struct {
	ID                 int                `json:"id"`
	Name               string             `json:"name"`
	ResponsibleUserID  int                `json:"responsible_user_id"`
	GroupID            int                `json:"group_id"`
	CreatedBy          int                `json:"created_by"`
	UpdatedBy          int                `json:"updated_by"`
	CreatedAt          int64              `json:"created_at"`
	UpdatedAt          int64              `json:"updated_at"`
	ClosestTaskAt      *int64             `json:"closest_task_at"`
	IsDeleted          bool               `json:"is_deleted"`
	AccountID          int                `json:"account_id"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values"`
	Embedded           *CompanyEmbedded   `json:"_embedded,omitempty"`
}

// CompanyEmbedded — _embedded компании.
type CompanyEmbedded struct {
	Tags []Tag `json:"tags,omitempty"`
}

// CompanyInput — тело POST/PATCH /companies.
type CompanyInput struct {
	ID                 int                `json:"id,omitempty"`
	Name               string             `json:"name,omitempty"`
	ResponsibleUserID  int                `json:"responsible_user_id,omitempty"`
	CreatedBy          *int               `json:"created_by,omitempty"`
	UpdatedBy          *int               `json:"updated_by,omitempty"`
	CreatedAt          int64              `json:"created_at,omitempty"`
	UpdatedAt          int64              `json:"updated_at,omitempty"`
	CustomFieldsValues []CustomFieldValue `json:"custom_fields_values,omitempty"`
	TagsToAdd          []TagRef           `json:"tags_to_add,omitempty"`
	TagsToDelete       []TagRef           `json:"tags_to_delete,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
}

// ContactChat — связь контакта с чатом (_embedded.chats).
type ContactChat struct {
	ContactID int    `json:"contact_id,omitempty"`
	ChatID    string `json:"chat_id"`
}

// ListContacts — GET /api/v4/contacts.
func (c *Client) ListContacts(ctx context.Context, acc Account, p ListParams) (Page[Contact], error) {
	return listEntities[Contact](ctx, c, acc, "contacts.list", "/api/v4/contacts", "contacts", p.values())
}

// CreateContacts — POST /api/v4/contacts (batch до 250).
func (c *Client) CreateContacts(ctx context.Context, acc Account, in []ContactInput) ([]CreatedEntity, error) {
	return sendBatch[ContactInput, CreatedEntity](ctx, c, acc, http.MethodPost, "contacts.create", "/api/v4/contacts", "contacts", in)
}

// UpdateContacts — PATCH /api/v4/contacts (batch до 250).
func (c *Client) UpdateContacts(ctx context.Context, acc Account, in []ContactInput) ([]CreatedEntity, error) {
	return sendBatch[ContactInput, CreatedEntity](ctx, c, acc, http.MethodPatch, "contacts.update", "/api/v4/contacts", "contacts", in)
}

// ListContactChats — GET /api/v4/contacts/chats.
func (c *Client) ListContactChats(ctx context.Context, acc Account, p ListParams) (Page[ContactChat], error) {
	return listEntities[ContactChat](ctx, c, acc, "contacts.chats.list", "/api/v4/contacts/chats", "chats", p.values())
}

// LinkContactChat — POST /api/v4/contacts/chats (batch до 250).
func (c *Client) LinkContactChat(ctx context.Context, acc Account, in []ContactChat) ([]ContactChat, error) {
	return sendBatch[ContactChat, ContactChat](ctx, c, acc, http.MethodPost, "contacts.chats.link", "/api/v4/contacts/chats", "chats", in)
}

// ListCompanies — GET /api/v4/companies.
func (c *Client) ListCompanies(ctx context.Context, acc Account, p ListParams) (Page[Company], error) {
	return listEntities[Company](ctx, c, acc, "companies.list", "/api/v4/companies", "companies", p.values())
}

// CreateCompanies — POST /api/v4/companies (batch до 250).
func (c *Client) CreateCompanies(ctx context.Context, acc Account, in []CompanyInput) ([]CreatedEntity, error) {
	return sendBatch[CompanyInput, CreatedEntity](ctx, c, acc, http.MethodPost, "companies.create", "/api/v4/companies", "companies", in)
}

// UpdateCompanies — PATCH /api/v4/companies (batch до 250).
func (c *Client) UpdateCompanies(ctx context.Context, acc Account, in []CompanyInput) ([]CreatedEntity, error) {
	return sendBatch[CompanyInput, CreatedEntity](ctx, c, acc, http.MethodPatch, "companies.update", "/api/v4/companies", "companies", in)
}
