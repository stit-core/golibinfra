package sberaikit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// refreshSkew — обновляем токен заранее, не дожидаясь точного expires_at, чтобы
// устранить гонку «токен протух между проверкой и запросом» (research §2.4).
const refreshSkew = 60 * time.Second

// rqUIDHeader — корреляционный заголовок Sber: свежий UUIDv4 на КАЖДЫЙ запрос
// (и OAuth, и бизнес-вызовы). Логируется для саппорта.
const rqUIDHeader = "RqUID"

// tokenResponse — тело 200 OAuth-эндпоинта. expires_at — абсолютный Unix epoch в
// МИЛЛИСЕКУНДАХ (не TTL), время жизни ~30 мин.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   int64  `json:"expires_at"`
}

// tokenManager — кэш access-токена с single-flight рефрешем. Часы — clockkit
// (тестируемость TTL). Один токен переиспользуется всеми вызовами до скоса.
type tokenManager struct {
	authKey  string
	scope    string
	oauthURL string
	hc       *http.Client
	clock    clockkit.Clock

	mu          sync.Mutex
	sf          singleflightToken
	accessToken string
	expireAt    time.Time
}

func newTokenManager(cfg Config, hc *http.Client, clock clockkit.Clock) *tokenManager {
	return &tokenManager{
		authKey:  cfg.AuthKey,
		scope:    cfg.Scope,
		oauthURL: cfg.OAuthURL,
		hc:       hc,
		clock:    clock,
	}
}

// token возвращает валидный access-токен, минтя новый при истечении (со скосом).
// Single-flight: параллельные вызовы в момент рефреша ждут один POST, а не штурмуют
// OAuth-эндпоинт пачкой.
func (m *tokenManager) token(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.accessToken != "" && m.clock.Now().Before(m.expireAt.Add(-refreshSkew)) {
		tok := m.accessToken
		m.mu.Unlock()
		return tok, nil
	}
	m.mu.Unlock()

	return m.sf.do(func() (string, error) {
		// Повторная проверка под флайтом: другой вызов мог уже обновить токен.
		m.mu.Lock()
		if m.accessToken != "" && m.clock.Now().Before(m.expireAt.Add(-refreshSkew)) {
			tok := m.accessToken
			m.mu.Unlock()
			return tok, nil
		}
		m.mu.Unlock()

		tok, exp, err := m.mint(ctx)
		if err != nil {
			return "", err
		}
		m.mu.Lock()
		m.accessToken, m.expireAt = tok, exp
		m.mu.Unlock()
		return tok, nil
	})
}

// invalidate сбрасывает кэш — после 401 рефрешим один раз и ретраим (research §10).
func (m *tokenManager) invalidate() {
	m.mu.Lock()
	m.accessToken, m.expireAt = "", time.Time{}
	m.mu.Unlock()
}

// mint выполняет client-credentials POST: Authorization: Basic <authKey> + RqUID +
// scope (form-urlencoded). Тот же Минцифры-CA на хосте 9443 (общий hc).
func (m *tokenManager) mint(ctx context.Context) (string, time.Time, error) {
	body := strings.NewReader("scope=" + m.scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.oauthURL, body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: oauth new request: %w", ErrAuth, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+m.authKey)
	req.Header.Set(rqUIDHeader, uuid.NewString())

	resp, err := m.hc.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: oauth call: %w", ErrAuth, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, classify(parseAPIError(resp.StatusCode, resp.Body))
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", time.Time{}, fmt.Errorf("%w: decode oauth response: %w", ErrAuth, err)
	}
	if tr.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("%w: empty access_token", ErrAuth)
	}
	// expires_at — epoch МИЛЛИСЕКУНДЫ (абсолютное), не TTL.
	return tr.AccessToken, time.UnixMilli(tr.ExpiresAt).UTC(), nil
}

// singleflightToken — минимальный single-flight на один ключ (рефреш токена).
// Не тянем golang.org/x/sync ради одного inflight: токен один на клиент.
type singleflightToken struct {
	mu      sync.Mutex
	inFlt   bool
	waiters []chan sfResult
}

type sfResult struct {
	tok string
	err error
}

func (s *singleflightToken) do(fn func() (string, error)) (string, error) {
	s.mu.Lock()
	if s.inFlt {
		ch := make(chan sfResult, 1)
		s.waiters = append(s.waiters, ch)
		s.mu.Unlock()
		r := <-ch
		return r.tok, r.err
	}
	s.inFlt = true
	s.mu.Unlock()

	tok, err := fn()

	s.mu.Lock()
	waiters := s.waiters
	s.waiters = nil
	s.inFlt = false
	s.mu.Unlock()

	for _, ch := range waiters {
		ch <- sfResult{tok: tok, err: err}
	}
	return tok, err
}

// parseAPIError декодит тело ошибки в обеих формах (OpenAI {error:{message}} и
// Sber {status,message}); raw сохраняем. Никогда не nil — даже на пустом теле.
func parseAPIError(status int, body io.Reader) *APIError {
	e := &APIError{StatusCode: status}
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil || len(raw) == 0 {
		return e
	}
	e.Raw = string(raw)

	var b struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &b) == nil {
		switch {
		case b.Error.Message != "":
			e.Message = b.Error.Message
		case b.Message != "":
			e.Message = b.Message
		}
	}
	return e
}
