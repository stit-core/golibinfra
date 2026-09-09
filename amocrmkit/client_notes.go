package amocrmkit

import (
	"context"
	"net/http"
)

// Note — объект примечания (GET-ответ).
type Note struct {
	ID                int64          `json:"id"`
	EntityID          int64          `json:"entity_id"`
	CreatedBy         int64          `json:"created_by"`
	UpdatedBy         int64          `json:"updated_by"`
	CreatedAt         int64          `json:"created_at"`
	UpdatedAt         int64          `json:"updated_at"`
	ResponsibleUserID int64          `json:"responsible_user_id"`
	GroupID           int64          `json:"group_id"`
	NoteType          string         `json:"note_type"`
	Params            map[string]any `json:"params"`
	AccountID         int64          `json:"account_id"`
	IsPinned          *bool          `json:"is_pinned"`
}

// NoteInput — тело POST /{entity_type}/notes.
type NoteInput struct {
	EntityID  int64          `json:"entity_id,omitempty"`
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
	ResponsibleUser int64  `json:"responsible_user_id,omitempty"`
	CreatedBy       int64  `json:"created_by,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
}

// AddNotes — POST /api/v4/{entity_type}/notes (entityType ∈ leads|contacts|companies|customers; batch до 250).
func (c *Client) AddNotes(ctx context.Context, acc Account, entityType string, in []NoteInput) ([]CreatedEntity, error) {
	e, err := entitySegment(entityType)
	if err != nil {
		return nil, err
	}
	return sendBatch[NoteInput, CreatedEntity](ctx, c, acc, http.MethodPost, "notes.add", "/api/v4/"+e+"/notes", "notes", in)
}

// AddCalls — POST /api/v4/calls (batch до 250).
func (c *Client) AddCalls(ctx context.Context, acc Account, in []CallInput) ([]CreatedEntity, error) {
	return sendBatch[CallInput, CreatedEntity](ctx, c, acc, http.MethodPost, "calls.add", "/api/v4/calls", "calls", in)
}
