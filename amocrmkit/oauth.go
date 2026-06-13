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

// Tokens — пара токенов + серверное время выдачи. TTL считается по ServerTime,
// а не по локальным часам — это устраняет clock-skew между gateway и amoCRM.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int   // секунд
	ServerTime   int64 // unix, серверное время amoCRM
}

// ExpiresAt — абсолютный момент истечения access-токена (ServerTime + ExpiresIn).
func (t Tokens) ExpiresAt() time.Time {
	return time.Unix(t.ServerTime+int64(t.ExpiresIn), 0).UTC()
}

// TokenSource — gateway реализует его поверх token-store; kit зовёт для
// авто-подстановки актуального access-токена аккаунта. Превентивный refresh
// (порог 5 мин) и хранение — у gateway, не у kit'а.
type TokenSource interface {
	Token(ctx context.Context, accountSubdomain string) (string, error)
}

// ExchangeCode обменивает authorization code на пару токенов (grant authorization_code).
func (c *Client) ExchangeCode(ctx context.Context, subdomain, tld, clientID, clientSecret, code, redirectURI string) (Tokens, error) {
	return c.oauthCall(ctx, subdomain, tld, "oauth.exchange", oauthReq{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		GrantType:    "authorization_code",
		Code:         code,
		RedirectURI:  redirectURI,
	})
}

// RefreshToken обновляет пару токенов по refresh-токену (grant refresh_token).
// redirect_uri обязателен в теле и для refresh (подтверждено wire-докой amoCRM),
// хотя в индикативной сигнатуре spec он опущен.
func (c *Client) RefreshToken(ctx context.Context, subdomain, tld, clientID, clientSecret, refreshToken, redirectURI string) (Tokens, error) {
	return c.oauthCall(ctx, subdomain, tld, "oauth.refresh", oauthReq{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
		RedirectURI:  redirectURI,
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

func (c *Client) oauthCall(ctx context.Context, subdomain, tld, endpoint string, reqBody oauthReq) (Tokens, error) {
	base := "https://" + subdomain + ".amocrm." + tld
	u := c.resolveURL(base, "/oauth2/access_token", nil)

	b, err := json.Marshal(reqBody) //nolint:gosec // G117: OAuth-тело обязано нести client_secret (wire-контракт amoCRM), не утечка
	if err != nil {
		return Tokens{}, fmt.Errorf("amocrmkit: %s: marshal: %w", endpoint, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return Tokens{}, fmt.Errorf("amocrmkit: %s: new request: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	var resp oauthResp
	err = c.execute(ctx, req, endpoint, subdomain, &resp)

	status := "success"
	if err != nil {
		status = "error"
	}
	c.instr.oauthRefresh.Add(ctx, 1, metric.WithAttributes(attribute.String("status", status)))

	if err != nil {
		return Tokens{}, err
	}
	return Tokens{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		ExpiresIn:    resp.ExpiresIn,
		ServerTime:   resp.ServerTime,
	}, nil
}
