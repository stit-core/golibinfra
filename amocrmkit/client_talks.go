package amocrmkit

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// talks. Реестр: get_talk, list_talks, close_talk.
// get_talk здесь ЕСТЬ (в отличие от прочих сущностей): talks — единственная сущность
// без надёжного webhook, poller/webhook-комплемент гидратируют беседу через get_talk.

// Talk — беседа (talk).
type Talk struct {
	TalkID     int    `json:"talk_id"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	Rate       int    `json:"rate"`
	ContactID  int    `json:"contact_id"`
	ChatID     string `json:"chat_id"`
	EntityID   int    `json:"entity_id"`
	EntityType string `json:"entity_type"`
	IsInWork   bool   `json:"is_in_work"`
	IsRead     bool   `json:"is_read"`
	Origin     string `json:"origin"`
	AccountID  int    `json:"account_id"`
	Status     string `json:"status"`
	SourceID   *int   `json:"source_id"`
}

// GetTalk — GET /api/v4/talks/{id}.
func (c *Client) GetTalk(ctx context.Context, acc Account, talkID int) (Talk, error) {
	return getOne[Talk](ctx, c, acc, "talks.get", fmt.Sprintf("/api/v4/talks/%d", talkID), nil)
}

// ListTalks — GET /api/v4/talks?filter[contact_id]={id}. Прочие server-фильтры/сортировка
// амо игнорит → клиентская фильтрация по updated_at у gateway.
func (c *Client) ListTalks(ctx context.Context, acc Account, contactID int, p ListParams) (Page[Talk], error) {
	q := p.values()
	if q == nil {
		q = url.Values{}
	}
	q.Set("filter[contact_id]", strconv.Itoa(contactID))
	return listEntities[Talk](ctx, c, acc, "talks.list", "/api/v4/talks", "talks", q)
}

// CloseTalk — POST /api/v4/talks/{id}/close (202, без тела ответа).
func (c *Client) CloseTalk(ctx context.Context, acc Account, talkID int, force bool) error {
	body := map[string]bool{"force_close": force}
	return c.sendNoContent(ctx, acc, http.MethodPost, "talks.close", fmt.Sprintf("/api/v4/talks/%d/close", talkID), body)
}
