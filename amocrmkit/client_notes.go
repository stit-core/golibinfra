package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
)

// notes + calls (communications). Реестр: add_note, add_call.

// Note — объект примечания (GET-ответ).
type Note struct {
	ID                int            `json:"id"`
	EntityID          int            `json:"entity_id"`
	CreatedBy         int            `json:"created_by"`
	UpdatedBy         int            `json:"updated_by"`
	CreatedAt         int64          `json:"created_at"`
	UpdatedAt         int64          `json:"updated_at"`
	ResponsibleUserID int            `json:"responsible_user_id"`
	GroupID           int            `json:"group_id"`
	NoteType          string         `json:"note_type"`
	Params            map[string]any `json:"params"`
	AccountID         int            `json:"account_id"`
	IsPinned          *bool          `json:"is_pinned"`
}

// NoteInput — тело POST /{entity_type}/notes.
type NoteInput struct {
	EntityID  int            `json:"entity_id,omitempty"`
	NoteType  string         `json:"note_type"`
	Params    map[string]any `json:"params"`
	RequestID string         `json:"request_id,omitempty"`
}

// CallInput — тело POST /calls (звонок как примечание call_in/call_out).
type CallInput struct {
	Direction       string `json:"direction"` // inbound|outbound
	Phone           string `json:"phone"`
	Duration        int    `json:"duration,omitempty"`
	Source          string `json:"source,omitempty"`
	Uniq            string `json:"uniq,omitempty"`
	Link            string `json:"link,omitempty"`
	CallStatus      int    `json:"call_status,omitempty"`
	CallResult      string `json:"call_result,omitempty"`
	CallResponsible any    `json:"call_responsible,omitempty"`
	ResponsibleUser int    `json:"responsible_user_id,omitempty"`
	CreatedBy       int    `json:"created_by,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
}

// AddNotes — POST /api/v4/{entity_type}/notes (entityType: leads|contacts|companies|customers; batch до 250).
func (c *Client) AddNotes(ctx context.Context, acc Account, entityType string, in []NoteInput) ([]CreatedEntity, error) {
	path := fmt.Sprintf("/api/v4/%s/notes", entityType)
	return sendBatch[NoteInput, CreatedEntity](ctx, c, acc, http.MethodPost, "notes.add", path, "notes", in)
}

// AddCalls — POST /api/v4/calls (batch до 250).
func (c *Client) AddCalls(ctx context.Context, acc Account, in []CallInput) ([]CreatedEntity, error) {
	return sendBatch[CallInput, CreatedEntity](ctx, c, acc, http.MethodPost, "calls.add", "/api/v4/calls", "calls", in)
}
