package issuer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
)

var (
	ErrRefreshInvalid = errors.New("hubkit/issuer: refresh token unknown or expired")
	ErrRefreshRevoked = errors.New("hubkit/issuer: session revoked (logged out)")
	ErrRefreshReused  = errors.New("hubkit/issuer: refresh token reused, session revoked")
	ErrRefreshConfig  = errors.New("hubkit/issuer: invalid refresh config")
)

const (
	refreshKeyPrefix   = "hub:refresh:"
	usedKeyPrefix      = "hub:refresh:used:"
	tombstoneKeyPrefix = "hub:revoked:"
)

// Session — минтящаяся при refresh identity: то, из чего RefreshManager соберёт
// claims нового access-токена. SID связывает refresh-цепочку с tombstone логаута.
type Session struct {
	SID        string `json:"sid"`
	EmployeeID string `json:"employee_id"`
	Subject    string `json:"sub"`
	Username   string `json:"username,omitempty"`
	Name       string `json:"name,omitempty"`
	Email      string `json:"email,omitempty"`
	Role       string `json:"role,omitempty"`
}

// RefreshManager реализует rotating refresh поверх cachekit.Cache: каждый обмен
// гасит старый токен и выдаёт новый; повторное предъявление погашенного токена
// (утечка) отзывает всю сессию; логаут ставит tombstone по sid. В кэше лежат
// только sha256-хэши токенов.
type RefreshManager struct {
	cache        cachekit.Cache
	ttl          time.Duration
	tombstoneTTL time.Duration
}

// NewRefreshManager: TTL <= 0 или TombstoneTTL < TTL → ErrRefreshConfig.
func NewRefreshManager(cache cachekit.Cache, cfg RefreshConfig) (*RefreshManager, error) {
	if cfg.TTL <= 0 {
		return nil, fmt.Errorf("%w: TTL must be > 0", ErrRefreshConfig)
	}
	if cfg.TombstoneTTL < cfg.TTL {
		return nil, fmt.Errorf("%w: TombstoneTTL must be >= TTL", ErrRefreshConfig)
	}
	return &RefreshManager{cache: cache, ttl: cfg.TTL, tombstoneTTL: cfg.TombstoneTTL}, nil
}

// Issue создаёт новый refresh-токен для сессии (после логина). Возвращает
// непрозрачный токен для cookie/ответа.
func (m *RefreshManager) Issue(ctx context.Context, sess Session) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := m.save(ctx, token, sess); err != nil {
		return "", err
	}
	return token, nil
}

// Rotate обменивает refresh-токен на сессию + новый токен. Неизвестный/протухший
// → ErrRefreshInvalid; сессия после логаута → ErrRefreshRevoked; уже
// погашенный токен → Revoke(sid) и ErrRefreshReused.
func (m *RefreshManager) Rotate(ctx context.Context, oldToken string) (Session, string, error) {
	h := tokenHash(oldToken)
	raw, ok, err := m.cache.GetDel(ctx, refreshKeyPrefix+h)
	if err != nil {
		return Session{}, "", err
	}
	if !ok {
		return Session{}, "", m.rejectMissing(ctx, h)
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return Session{}, "", fmt.Errorf("hubkit/issuer: corrupt refresh record: %w", err)
	}
	dead, err := m.tombstoned(ctx, sess.SID)
	if err != nil {
		return Session{}, "", err
	}
	if dead {
		return Session{}, "", ErrRefreshRevoked
	}
	if err := m.cache.Set(ctx, usedKeyPrefix+h, []byte(sess.SID), m.ttl); err != nil {
		return Session{}, "", err
	}
	newToken, err := randomToken()
	if err != nil {
		return Session{}, "", err
	}
	if err := m.save(ctx, newToken, sess); err != nil {
		return Session{}, "", err
	}
	return sess, newToken, nil
}

func (m *RefreshManager) rejectMissing(ctx context.Context, h string) error {
	sid, used, err := m.cache.Get(ctx, usedKeyPrefix+h)
	if err != nil {
		return err
	}
	if !used {
		return ErrRefreshInvalid
	}
	if err := m.Revoke(ctx, string(sid)); err != nil {
		return err
	}
	return fmt.Errorf("%w: sid %s", ErrRefreshReused, sid)
}

// Revoke ставит tombstone на сессию (логаут): любой ещё живой refresh-токен
// этой sid перестаёт обмениваться.
func (m *RefreshManager) Revoke(ctx context.Context, sid string) error {
	return m.cache.Set(ctx, tombstoneKeyPrefix+sid, []byte{1}, m.tombstoneTTL)
}

func (m *RefreshManager) tombstoned(ctx context.Context, sid string) (bool, error) {
	_, ok, err := m.cache.Get(ctx, tombstoneKeyPrefix+sid)
	return ok, err
}

func (m *RefreshManager) save(ctx context.Context, token string, sess Session) error {
	raw, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("hubkit/issuer: marshal refresh record: %w", err)
	}
	return m.cache.Set(ctx, refreshKeyPrefix+tokenHash(token), raw, m.ttl)
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("hubkit/issuer: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
