package amocrmkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// amoJo Chat API — подписанные (HMAC-SHA1) запросы на https://amojo.amocrm.{tld}.
//
// Тело маршалится ОДИН раз: Content-MD5 и реальное тело считаются с тех же байтов
// (иначе MD5 разойдётся). Маппинг типов медиа (audio→voice и пр.) — у потребителя,
// кит принимает amoJo-тип как есть.

// AmojoTarget — координаты amoJo-вызова. AccountKey должен совпадать с
// Account.Subdomain того же аккаунта: rate-budget общий с REST.
type AmojoTarget struct {
	TLD        string // "ru" | "com" (домены невзаимозаменяемы)
	AccountKey string // ключ rate-limiter (= Account.Subdomain аккаунта)
	Secret     string // channel secret для подписи
}

func (t AmojoTarget) validate() error {
	return validateAccount(t.AccountKey, t.TLD)
}

func (c *Client) amojoCall(ctx context.Context, t AmojoTarget, method, endpoint, path string, body, out any) error {
	if err := t.validate(); err != nil {
		return fmt.Errorf("amocrmkit: %s: %w", endpoint, err)
	}
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("amocrmkit: %s: marshal: %w", endpoint, err)
		}
	}
	u, err := c.resolveURL("https://amojo.amocrm."+t.TLD, path, nil)
	if err != nil {
		return fmt.Errorf("amocrmkit: %s: %w", endpoint, err)
	}
	sh := signRequest(c.clock.Now(), method, path, raw, t.Secret)

	var reader io.Reader
	if raw != nil {
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("amocrmkit: %s: new request: %w", endpoint, err)
	}
	req.Header.Set("Date", sh.Date)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-MD5", sh.ContentMD5)
	req.Header.Set("X-Signature", sh.Signature)

	return c.execute(ctx, req, endpoint, t.AccountKey, out)
}

// amojoEnvelope — конверт событий сообщений (new_message / edit_message).
type amojoEnvelope struct {
	EventType string `json:"event_type"`
	Payload   any    `json:"payload"`
}

// AmojoProfile — контактные данные участника.
type AmojoProfile struct {
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

// AmojoParticipant — участник чата (user/sender/receiver). RefID обязателен у
// менеджера-отправителя (UUID в API amoCRM).
type AmojoParticipant struct {
	ID          string        `json:"id"`
	RefID       string        `json:"ref_id,omitempty"`
	Name        string        `json:"name"`
	Avatar      string        `json:"avatar,omitempty"`
	Profile     *AmojoProfile `json:"profile,omitempty"`
	ProfileLink string        `json:"profile_link,omitempty"`
}

// ConnectInput — тело POST /v2/origin/custom/{channel_id}/connect.
type ConnectInput struct {
	AccountID            string `json:"account_id"` // amojo_id аккаунта (UUID)
	Title                string `json:"title"`
	HookAPIVersion       string `json:"hook_api_version"` // всегда "v2"
	IsTimeWindowDisabled bool   `json:"is_time_window_disabled,omitempty"`
}

// ConnectResult — ответ connect (scope_id = {channel_id}_{amojo_id}).
type ConnectResult struct {
	ScopeID string `json:"scope_id"`
}

// AmojoConnect — POST /v2/origin/custom/{channel_id}/connect.
func (c *Client) AmojoConnect(ctx context.Context, t AmojoTarget, channelID string, in ConnectInput) (ConnectResult, error) {
	if in.HookAPIVersion == "" {
		in.HookAPIVersion = "v2"
	}
	var out ConnectResult
	err := c.amojoCall(ctx, t, http.MethodPost, "amojo.connect", "/v2/origin/custom/"+channelID+"/connect", in, &out)
	return out, err
}

// AmojoDisconnect — DELETE /v2/origin/custom/{channel_id}/disconnect.
func (c *Client) AmojoDisconnect(ctx context.Context, t AmojoTarget, channelID, amojoAccountID string) error {
	body := map[string]string{"account_id": amojoAccountID}
	return c.amojoCall(ctx, t, http.MethodDelete, "amojo.disconnect", "/v2/origin/custom/"+channelID+"/disconnect", body, nil)
}

// CreateChatInput — тело POST /v2/origin/custom/{scope_id}/chats.
type CreateChatInput struct {
	ConversationID string           `json:"conversation_id"`
	Source         map[string]any   `json:"source,omitempty"`
	User           AmojoParticipant `json:"user"`
}

// CreateChatResult — ответ create chat (id = conversation_ref_id).
type CreateChatResult struct {
	ID   string `json:"id"`
	User struct {
		ID       string `json:"id"`
		ClientID string `json:"client_id"`
		Name     string `json:"name"`
		Avatar   string `json:"avatar"`
		Phone    string `json:"phone"`
		Email    string `json:"email"`
	} `json:"user"`
}

// AmojoCreateChat — POST /v2/origin/custom/{scope_id}/chats.
func (c *Client) AmojoCreateChat(ctx context.Context, t AmojoTarget, scopeID string, in CreateChatInput) (CreateChatResult, error) {
	var out CreateChatResult
	err := c.amojoCall(ctx, t, http.MethodPost, "amojo.create_chat", "/v2/origin/custom/"+scopeID+"/chats", in, &out)
	return out, err
}

// NewMessagePayload — payload события new_message. Message полиморфно (text/media/…),
// поэтому map: точное содержимое строит потребитель по message-types.
type NewMessagePayload struct {
	Timestamp         int64             `json:"timestamp"`
	MsecTimestamp     int64             `json:"msec_timestamp"`
	MsgID             string            `json:"msgid"`
	ConversationID    string            `json:"conversation_id,omitempty"`
	ConversationRefID string            `json:"conversation_ref_id,omitempty"`
	Silent            bool              `json:"silent"`
	Sender            AmojoParticipant  `json:"sender"`
	Receiver          *AmojoParticipant `json:"receiver,omitempty"`
	Message           map[string]any    `json:"message"`
	Source            map[string]any    `json:"source,omitempty"`
	ReplyTo           map[string]any    `json:"reply_to,omitempty"`
	Forwards          map[string]any    `json:"forwards,omitempty"`
}

// NewMessageResult — ответ new_message (ref_id'ы сущностей в API amoCRM).
type NewMessageResult struct {
	NewMessage struct {
		ConversationID string `json:"conversation_id"`
		SenderID       string `json:"sender_id"`
		ReceiverID     string `json:"receiver_id"`
		MsgID          string `json:"msgid"`
		RefID          string `json:"ref_id"`
	} `json:"new_message"`
}

// AmojoNewMessage — POST /v2/origin/custom/{scope_id} (event_type=new_message).
// silent в payload управляет режимом: false=send (создаёт неразобранное),
// true=import (тихо в историю).
func (c *Client) AmojoNewMessage(ctx context.Context, t AmojoTarget, scopeID string, payload NewMessagePayload) (NewMessageResult, error) {
	var out NewMessageResult
	body := amojoEnvelope{EventType: "new_message", Payload: payload}
	err := c.amojoCall(ctx, t, http.MethodPost, "amojo.new_message", "/v2/origin/custom/"+scopeID, body, &out)
	return out, err
}

// AmojoEditMessage — POST /v2/origin/custom/{scope_id} (event_type=edit_message;
// редактирование поверх send/import — отдельного суффикса /update амо не подтверждает).
func (c *Client) AmojoEditMessage(ctx context.Context, t AmojoTarget, scopeID string, payload NewMessagePayload) (NewMessageResult, error) {
	var out NewMessageResult
	body := amojoEnvelope{EventType: "edit_message", Payload: payload}
	err := c.amojoCall(ctx, t, http.MethodPost, "amojo.edit_message", "/v2/origin/custom/"+scopeID, body, &out)
	return out, err
}

// AmojoReact — POST /v2/origin/custom/{scope_id}/react. Тело строит потребитель
// (id/msgid цели, user, type, emoji) — кит передаёт как есть.
func (c *Client) AmojoReact(ctx context.Context, t AmojoTarget, scopeID string, body map[string]any) error {
	return c.amojoCall(ctx, t, http.MethodPost, "amojo.react", "/v2/origin/custom/"+scopeID+"/react", body, nil)
}

// AmojoTyping — POST /v2/origin/custom/{scope_id}/typing (ephemeral).
func (c *Client) AmojoTyping(ctx context.Context, t AmojoTarget, scopeID string, body map[string]any) error {
	return c.amojoCall(ctx, t, http.MethodPost, "amojo.typing", "/v2/origin/custom/"+scopeID+"/typing", body, nil)
}

// AmojoDeliveryStatus — POST /v2/origin/custom/{scope_id}/{msgid}/delivery_status.
func (c *Client) AmojoDeliveryStatus(ctx context.Context, t AmojoTarget, scopeID, msgID string, body map[string]any) error {
	path := "/v2/origin/custom/" + scopeID + "/" + msgID + "/delivery_status"
	return c.amojoCall(ctx, t, http.MethodPost, "amojo.delivery_status", path, body, nil)
}
