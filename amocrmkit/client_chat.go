package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// chat templates (REST v4; amoJo-контур — в amojo.go).

// ChatTemplate — шаблон сообщения чата.
type ChatTemplate struct {
	ID           int            `json:"id"`
	AccountID    int            `json:"account_id"`
	Name         string         `json:"name"`
	Content      string         `json:"content"`
	CreatedAt    int64          `json:"created_at"`
	UpdatedAt    int64          `json:"updated_at"`
	Buttons      []any          `json:"buttons"`
	Attachment   map[string]any `json:"attachment"`
	IsEditable   bool           `json:"is_editable"`
	ExternalID   string         `json:"external_id"`
	Type         string         `json:"type"`
	ReviewStatus string         `json:"review_status"`
	IsOnReview   *bool          `json:"is_on_review"`
	WABACategory string         `json:"waba_category,omitempty"`
	WABALanguage string         `json:"waba_language,omitempty"`
}

// ChatTemplateInput — тело POST/PATCH /chats/templates.
type ChatTemplateInput struct {
	ID           int            `json:"id,omitempty"`
	Name         string         `json:"name,omitempty"`
	Content      string         `json:"content,omitempty"`
	Buttons      []any          `json:"buttons,omitempty"`
	Attachment   map[string]any `json:"attachment,omitempty"`
	ExternalID   string         `json:"external_id,omitempty"`
	WABACategory string         `json:"waba_category,omitempty"`
	WABALanguage string         `json:"waba_language,omitempty"`
	RequestID    string         `json:"request_id,omitempty"`
}

// ListChatTemplates — GET /api/v4/chats/templates.
func (c *Client) ListChatTemplates(ctx context.Context, acc Account, p ListParams) (Page[ChatTemplate], error) {
	return listEntities[ChatTemplate](ctx, c, acc, "chat_templates.list", "/api/v4/chats/templates", "chat_templates", p.values())
}

// CreateChatTemplates — POST /api/v4/chats/templates (batch до 50).
func (c *Client) CreateChatTemplates(ctx context.Context, acc Account, in []ChatTemplateInput) ([]ChatTemplate, error) {
	return sendBatch[ChatTemplateInput, ChatTemplate](ctx, c, acc, http.MethodPost, "chat_templates.create", "/api/v4/chats/templates", "chat_templates", in)
}

// UpdateChatTemplate — PATCH /api/v4/chats/templates/{id}.
func (c *Client) UpdateChatTemplate(ctx context.Context, acc Account, id int, in ChatTemplateInput) (ChatTemplate, error) {
	return sendOne[ChatTemplate](ctx, c, acc, http.MethodPatch, "chat_templates.update", fmt.Sprintf("/api/v4/chats/templates/%d", id), in)
}

// DeleteChatTemplates — DELETE /api/v4/chats/templates (batch по id в теле).
func (c *Client) DeleteChatTemplates(ctx context.Context, acc Account, ids []int) error {
	body := make([]idRef, len(ids))
	for i, id := range ids {
		body[i] = idRef{ID: id}
	}
	return c.sendNoContent(ctx, acc, http.MethodDelete, "chat_templates.delete", "/api/v4/chats/templates", body)
}

// TemplateReview — статус ревью шаблона (WABA-модерация).
type TemplateReview struct {
	ID         int    `json:"id"`
	TemplateID int    `json:"template_id"`
	Status     string `json:"status"`
}

// SubmitTemplateReview — POST /api/v4/chats/templates/{id}/review.
func (c *Client) SubmitTemplateReview(ctx context.Context, acc Account, templateID int, in map[string]any) (TemplateReview, error) {
	path := fmt.Sprintf("/api/v4/chats/templates/%d/review", templateID)
	return sendOne[TemplateReview](ctx, c, acc, http.MethodPost, "chat_templates.review.submit", path, in)
}

// UpdateTemplateReview — POST /api/v4/chats/templates/{id}/review/{review_id}.
func (c *Client) UpdateTemplateReview(ctx context.Context, acc Account, templateID, reviewID int, in map[string]any) (TemplateReview, error) {
	path := fmt.Sprintf("/api/v4/chats/templates/%d/review/%d", templateID, reviewID)
	return sendOne[TemplateReview](ctx, c, acc, http.MethodPost, "chat_templates.review.update", path, in)
}
