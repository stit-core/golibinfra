package yandexaikit

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
)

// authProvider — источник заголовков авторизации для одного исходящего вызова.
// Две реализации: apiKeyAuth (статичный Api-Key) и iamAuth (IAM-token через
// PS256-JWT с проактивным refresh по clockkit).
type authProvider interface {
	// apply ставит Authorization (+ x-folder-id для IAM) на req. Может сходить за
	// свежим IAM-токеном (потому ctx и error).
	apply(ctx context.Context, req *http.Request) error
}

// apiKeyAuth — статичная схема: Authorization: Api-Key <key>, без x-folder-id
// (API уже знает каталог по ключу — см. research §2.2).
type apiKeyAuth struct{ key string }

func (a apiKeyAuth) apply(_ context.Context, req *http.Request) error {
	req.Header.Set("Authorization", "Api-Key "+a.key)
	return nil
}

// iamRefreshSkew — за сколько до expiry обновляем IAM-токен проактивно.
const iamRefreshSkew = 5 * time.Minute

// jwtTTL — TTL подписываемого JWT для обмена на IAM-токен. Yandex требует
// exp - iat ≤ 3600s (research §2.4).
const jwtTTL = time.Hour

// iamAuth — Bearer-схема: PS256-JWT сервис-аккаунта обменивается на IAM-token
// (≤12h) на iam.api.cloud.yandex.net, кэшируется и проактивно обновляется.
// x-folder-id обязателен для IAM-схемы.
type iamAuth struct {
	hc       *httpclient.Client
	clock    clockkit.Clock
	iamURL   string
	folderID string

	saID  string
	keyID string
	pk    *rsa.PrivateKey

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newIAMAuth(cfg Config, hc *httpclient.Client, clock clockkit.Clock) (*iamAuth, error) {
	if cfg.FolderID == "" {
		return nil, fmt.Errorf("%w: YANDEX_FOLDER_ID is required for IAM auth", ErrConfig)
	}
	if cfg.ServiceAccountID == "" || cfg.ServiceAccountKeyID == "" || cfg.PrivateKey == "" {
		return nil, fmt.Errorf("%w: YANDEX_SERVICE_ACCOUNT_{ID,KEY_ID,PRIVATE_KEY} are required for IAM auth", ErrConfig)
	}
	pk, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(cfg.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("%w: parse service-account private key: %v", ErrConfig, err) //nolint:errorlint // wrap ErrConfig, причина — текст
	}
	return &iamAuth{
		hc:       hc,
		clock:    clock,
		iamURL:   cfg.IAMURL,
		folderID: cfg.FolderID,
		saID:     cfg.ServiceAccountID,
		keyID:    cfg.ServiceAccountKeyID,
		pk:       pk,
	}, nil
}

func (a *iamAuth) apply(ctx context.Context, req *http.Request) error {
	tok, err := a.iamToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("x-folder-id", a.folderID)
	return nil
}

// iamToken возвращает кэшированный IAM-токен, обновляя его за iamRefreshSkew до
// истечения. Refresh сериализован под mu (один обмен на гонке).
func (a *iamAuth) iamToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.clock.Now().Before(a.expires.Add(-iamRefreshSkew)) {
		return a.token, nil
	}
	tok, exp, err := a.exchange(ctx)
	if err != nil {
		return "", err
	}
	a.token, a.expires = tok, exp
	return tok, nil
}

// signedJWT собирает PS256-JWT авторизованного ключа сервис-аккаунта (research §2.4):
// header{typ:JWT,alg:PS256,kid}, claims{iss:saID, aud:<tokens-url>, iat, exp≤+1h}.
func (a *iamAuth) signedJWT() (string, error) {
	now := a.clock.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodPS256, jwt.MapClaims{
		"iss": a.saID,
		"aud": a.iamURL + "/iam/v1/tokens",
		"iat": now.Unix(),
		"exp": now.Add(jwtTTL).Unix(),
	})
	tok.Header["kid"] = a.keyID
	signed, err := tok.SignedString(a.pk)
	if err != nil {
		return "", fmt.Errorf("yandexaikit: sign IAM JWT: %w", err)
	}
	return signed, nil
}

// exchange обменивает подписанный JWT на IAM-токен (POST /iam/v1/tokens).
func (a *iamAuth) exchange(ctx context.Context) (string, time.Time, error) {
	jwtStr, err := a.signedJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	body, err := json.Marshal(map[string]string{"jwt": jwtStr})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("yandexaikit: marshal IAM request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.iamURL+"/iam/v1/tokens", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("yandexaikit: new IAM request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.hc.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("yandexaikit: IAM exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", time.Time{}, classify(resp.StatusCode, parseAPIError(resp))
	}
	var out struct {
		IAMToken  string `json:"iamToken"`
		ExpiresAt string `json:"expiresAt"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("yandexaikit: read IAM response: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("yandexaikit: decode IAM response: %w", err)
	}
	if out.IAMToken == "" {
		return "", time.Time{}, fmt.Errorf("%w: empty IAM token in response", ErrAuth)
	}
	exp, err := time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil {
		// Без expiresAt держим консервативный TTL в 1 час от текущих часов кита.
		exp = a.clock.Now().Add(time.Hour)
	}
	return out.IAMToken, exp, nil
}

// parseAPIError читает тело ошибочного ответа (google.rpc.Status). Никогда не
// возвращает nil: даже при нечитаемом/пустом теле отдаёт *APIError со статусом.
func parseAPIError(resp *http.Response) *APIError {
	e := &APIError{Status: resp.StatusCode}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(raw) == 0 {
		return e
	}
	var st rpcStatus
	if json.Unmarshal(raw, &st) == nil {
		e.Code, e.Message = st.Code, st.Message
	}
	return e
}

// newAuth выбирает схему по Config: Api-Key приоритетнее, иначе IAM-JWT.
func newAuth(cfg Config, hc *httpclient.Client, clock clockkit.Clock) (authProvider, error) {
	if cfg.usesAPIKey() {
		if cfg.FolderID != "" {
			// research §2.2: с Api-Key x-folder-id запрещён — каталог уже в ключе.
			return nil, fmt.Errorf("%w: YANDEX_FOLDER_ID must be empty with Api-Key auth", ErrConfig)
		}
		return apiKeyAuth{key: cfg.APIKey}, nil
	}
	return newIAMAuth(cfg, hc, clock)
}
