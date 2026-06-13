package amocrmkit

import (
	"context"
	"net/url"
)

// account/meta. Реестр: get_amojo_id, get_drive_url, get_account_task_types, get_event_types.
// (Webhook-subscription management в реестре 109 отсутствует — добавляется при появлении use-case.)

// AccountInfo — объект GET /api/v4/account (с нужными with=).
type AccountInfo struct {
	ID                      int              `json:"id"`
	Name                    string           `json:"name"`
	Subdomain               string           `json:"subdomain"`
	CurrentUserID           int              `json:"current_user_id"`
	Country                 string           `json:"country"`
	CustomersMode           string           `json:"customers_mode"`
	IsUnsortedOn            bool             `json:"is_unsorted_on"`
	IsLossReasonEnabled     bool             `json:"is_loss_reason_enabled"`
	IsTechnicalAccount      bool             `json:"is_technical_account"`
	ContactNameDisplayOrder int              `json:"contact_name_display_order"`
	UUID                    string           `json:"uuid"`
	Version                 int              `json:"version"`
	AmojoID                 string           `json:"amojo_id"`
	DriveURL                string           `json:"drive_url"`
	Embedded                *AccountEmbedded `json:"_embedded,omitempty"`
}

// AccountEmbedded — _embedded аккаунта (по with=task_types и др.).
type AccountEmbedded struct {
	TaskTypes []TaskType `json:"task_types,omitempty"`
}

// TaskType — тип задачи аккаунта.
type TaskType struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Color  string `json:"color"`
	IconID int    `json:"icon_id"`
	Code   string `json:"code"`
}

// EventType — тип события ленты (GET /events/types; права/язык-зависимый).
type EventType struct {
	Type string `json:"type"`
	Key  string `json:"key"`
	Lang string `json:"lang"`
}

// getAccount — GET /api/v4/account?with=... (общий для get_amojo_id/drive_url/task_types).
func (c *Client) getAccount(ctx context.Context, acc Account, with string) (AccountInfo, error) {
	q := url.Values{}
	if with != "" {
		q.Set("with", with)
	}
	return getOne[AccountInfo](ctx, c, acc, "account.get", "/api/v4/account", q)
}

// GetAmojoID — GET /api/v4/account?with=amojo_id → amojo_id аккаунта.
func (c *Client) GetAmojoID(ctx context.Context, acc Account) (string, error) {
	info, err := c.getAccount(ctx, acc, "amojo_id")
	return info.AmojoID, err
}

// GetDriveURL — GET /api/v4/account?with=drive_url → динамический drive_url аккаунта.
func (c *Client) GetDriveURL(ctx context.Context, acc Account) (string, error) {
	info, err := c.getAccount(ctx, acc, "drive_url")
	return info.DriveURL, err
}

// GetAccountTaskTypes — GET /api/v4/account?with=task_types → типы задач аккаунта.
func (c *Client) GetAccountTaskTypes(ctx context.Context, acc Account) ([]TaskType, error) {
	info, err := c.getAccount(ctx, acc, "task_types")
	if err != nil || info.Embedded == nil {
		return nil, err
	}
	return info.Embedded.TaskTypes, nil
}

// GetEventTypes — GET /api/v4/events/types (read-through: права/язык аккаунта).
func (c *Client) GetEventTypes(ctx context.Context, acc Account) ([]EventType, error) {
	page, err := listEntities[EventType](ctx, c, acc, "events.types", "/api/v4/events/types", "events_types", nil)
	return page.Items, err
}
