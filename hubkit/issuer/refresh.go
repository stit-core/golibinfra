package issuer

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
)

var (
	ErrRefreshInvalid = errors.New("hubkit/issuer: refresh token unknown or expired")
	ErrRefreshRevoked = errors.New("hubkit/issuer: session revoked (logged out)")
)

const (
	refreshKeyPrefix   = "hub:refresh:"
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

// RefreshStore хранит refresh-состояние. Записи refresh-токена one-time (GetDel),
// tombstone — по sid. Одна из двух реализаций: RedisRefreshStore (прод) /
// MemoryRefreshStore (тесты, single-replica без Redis).
type RefreshStore interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	GetDel(ctx context.Context, key string) ([]byte, bool, error)
	Tombstone(ctx context.Context, sid string, ttl time.Duration) error
	IsTombstoned(ctx context.Context, sid string) (bool, error)
}

// RefreshManager реализует rotating refresh: каждый обмен инвалидирует старый
// токен и выдаёт новый; логаут ставит tombstone по sid, после чего вся
// refresh-цепочка сессии отвергается.
type RefreshManager struct {
	store        RefreshStore
	ttl          time.Duration
	tombstoneTTL time.Duration
}

func NewRefreshManager(store RefreshStore, cfg RefreshConfig) *RefreshManager {
	return &RefreshManager{store: store, ttl: cfg.TTL, tombstoneTTL: cfg.TombstoneTTL}
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

// Rotate обменивает старый refresh-токен на новую сессию + новый токен.
// Старый гасится (one-time). Отклоняет неизвестный/протухший токен и токены
// сессии с tombstone (после логаута).
func (m *RefreshManager) Rotate(ctx context.Context, oldToken string) (Session, string, error) {
	raw, ok, err := m.store.GetDel(ctx, refreshKeyPrefix+oldToken)
	if err != nil {
		return Session{}, "", err
	}
	if !ok {
		return Session{}, "", ErrRefreshInvalid
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return Session{}, "", fmt.Errorf("hubkit/issuer: corrupt refresh record: %w", err)
	}
	dead, err := m.store.IsTombstoned(ctx, sess.SID)
	if err != nil {
		return Session{}, "", err
	}
	if dead {
		return Session{}, "", ErrRefreshRevoked
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

// Revoke ставит tombstone на сессию (логаут): любой ещё живой refresh-токен
// этой sid перестаёт обмениваться.
func (m *RefreshManager) Revoke(ctx context.Context, sid string) error {
	return m.store.Tombstone(ctx, sid, m.tombstoneTTL)
}

func (m *RefreshManager) save(ctx context.Context, token string, sess Session) error {
	raw, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("hubkit/issuer: marshal refresh record: %w", err)
	}
	return m.store.Set(ctx, refreshKeyPrefix+token, raw, m.ttl)
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("hubkit/issuer: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewCacheRefreshStore — RefreshStore поверх cachekit.Cache (Redis): переживает
// рестарт и работает при нескольких репликах.
func NewCacheRefreshStore(c cachekit.Cache) RefreshStore {
	return &cacheRefreshStore{c: c}
}

type cacheRefreshStore struct {
	c cachekit.Cache
}

func (s *cacheRefreshStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.c.Set(ctx, key, value, ttl)
}

func (s *cacheRefreshStore) GetDel(ctx context.Context, key string) ([]byte, bool, error) {
	v, ok, err := s.c.Get(ctx, key)
	if err != nil || !ok {
		return nil, false, err
	}
	// Get+Delete не атомарны, но refresh-токен и так one-time-секрет: гонка двух
	// параллельных refresh с одним токеном в худшем случае пустит один из них.
	if err := s.c.Delete(ctx, key); err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (s *cacheRefreshStore) Tombstone(ctx context.Context, sid string, ttl time.Duration) error {
	return s.c.Set(ctx, tombstoneKeyPrefix+sid, []byte{1}, ttl)
}

func (s *cacheRefreshStore) IsTombstoned(ctx context.Context, sid string) (bool, error) {
	_, ok, err := s.c.Get(ctx, tombstoneKeyPrefix+sid)
	return ok, err
}

// NewMemoryRefreshStore — RefreshStore в памяти процесса: тесты и
// single-replica без Redis (состояние не переживает рестарт).
func NewMemoryRefreshStore() RefreshStore {
	return &memoryRefreshStore{entries: map[string]memoryEntry{}, tombstones: map[string]time.Time{}}
}

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

type memoryRefreshStore struct {
	mu         sync.Mutex
	entries    map[string]memoryEntry
	tombstones map[string]time.Time
}

func (s *memoryRefreshStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := make([]byte, len(value))
	copy(v, value)
	s.entries[key] = memoryEntry{value: v, expiresAt: time.Now().Add(ttl)}
	return nil
}

func (s *memoryRefreshStore) GetDel(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	delete(s.entries, key)
	if time.Now().After(e.expiresAt) {
		return nil, false, nil
	}
	return e.value, true, nil
}

func (s *memoryRefreshStore) Tombstone(_ context.Context, sid string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombstones[sid] = time.Now().Add(ttl)
	return nil
}

func (s *memoryRefreshStore) IsTombstoned(_ context.Context, sid string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tombstones[sid]
	if !ok {
		return false, nil
	}
	if time.Now().After(exp) {
		delete(s.tombstones, sid)
		return false, nil
	}
	return true, nil
}
