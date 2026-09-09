package amocrmkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// OAuthConfig — параметры интеграции amoCRM (одни на аккаунт, из настроек
// интеграции). Отдельный тип вместо позиционных строк: перестановка
// client_id/client_secret компилятором не ловится.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string // обязателен в теле обоих grant'ов (wire-контракт amoCRM)
}

// TokenPair — пара токенов + серверное время выдачи. TTL считается по ServerTime,
// а не по локальным часам — это устраняет clock-skew между потребителем и amoCRM.
//
// RefreshToken одноразовый: после обмена старый недействителен. Сохраняйте новую
// пару до того, как ей воспользуетесь, и сериализуйте refresh на аккаунт
// (RefreshCoordinator) — иначе гонка теряет интеграцию.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int   // секунд
	ServerTime   int64 // unix, серверное время amoCRM (кит подставляет свои часы, если поле не пришло)
}

// ExpiresAt — абсолютный момент истечения access-токена (ServerTime + ExpiresIn).
func (t TokenPair) ExpiresAt() time.Time {
	return time.Unix(t.ServerTime+int64(t.ExpiresIn), 0).UTC()
}

// ExchangeCode обменивает authorization code на пару токенов
// (grant authorization_code). Account.Token не используется.
func (c *Client) ExchangeCode(ctx context.Context, acc Account, app OAuthConfig, code string) (TokenPair, error) {
	return c.oauthCall(ctx, acc, "oauth.exchange", oauthReq{
		ClientID:     app.ClientID,
		ClientSecret: app.ClientSecret,
		GrantType:    "authorization_code",
		Code:         code,
		RedirectURI:  app.RedirectURI,
	})
}

// RefreshToken обменивает refresh-токен на новую пару (grant refresh_token).
// Старый refresh-токен после успешного обмена недействителен.
func (c *Client) RefreshToken(ctx context.Context, acc Account, app OAuthConfig, refreshToken string) (TokenPair, error) {
	return c.oauthCall(ctx, acc, "oauth.refresh", oauthReq{
		ClientID:     app.ClientID,
		ClientSecret: app.ClientSecret,
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
		RedirectURI:  app.RedirectURI,
	})
}

// oauthReq — тело POST /oauth2/access_token (одно для обоих grant'ов; пустые
// поля omitempty не ставим — amoCRM игнорит неактуальные для grant поля).
type oauthReq struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	GrantType    string `json:"grant_type"`
	Code         string `json:"code,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	RedirectURI  string `json:"redirect_uri"`
}

type oauthResp struct {
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	ServerTime   int64  `json:"server_time"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (c *Client) oauthCall(ctx context.Context, acc Account, endpoint string, reqBody oauthReq) (TokenPair, error) {
	if err := acc.validate(); err != nil {
		return TokenPair{}, fmt.Errorf("amocrmkit: %s: %w", endpoint, err)
	}
	u, err := c.resolveURL(acc.restBase(), "/oauth2/access_token", nil)
	if err != nil {
		return TokenPair{}, fmt.Errorf("amocrmkit: %s: %w", endpoint, err)
	}

	b, err := json.Marshal(reqBody) //nolint:gosec // G117: OAuth-тело обязано нести client_secret (wire-контракт amoCRM), не утечка
	if err != nil {
		return TokenPair{}, fmt.Errorf("amocrmkit: %s: %w: marshal: %w", endpoint, ErrInvalidRequest, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return TokenPair{}, fmt.Errorf("amocrmkit: %s: %w: new request: %w", endpoint, ErrInvalidRequest, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	var resp oauthResp
	err = c.execute(ctx, req, call{endpoint: endpoint, acc: acc.ref(), single: true}, &resp)

	status := "success"
	if err != nil {
		status = "error"
	}
	c.instr.oauthRefresh.Add(ctx, 1, metric.WithAttributes(
		attribute.String("grant", reqBody.GrantType),
		attribute.String("status", status),
	))

	if err != nil {
		c.log.Warn(ctx, "amocrmkit: oauth failed", "grant", reqBody.GrantType, "account", acc.Subdomain, "error", err.Error())
		return TokenPair{}, err
	}
	if resp.ServerTime == 0 {
		// Без server_time TTL считался бы от 1970 — потребитель рефрешил бы по кругу.
		resp.ServerTime = c.clock.Now().Unix()
	}
	return TokenPair{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		ExpiresIn:    resp.ExpiresIn,
		ServerTime:   resp.ServerTime,
	}, nil
}
