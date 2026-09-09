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
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/hubclaims"
)

var (
	// ErrRefreshInvalid — токен неизвестен или истёк.
	ErrRefreshInvalid = errors.New("hubkit/issuer: refresh token unknown or expired")
	// ErrRefreshRevoked — сессия отозвана (логаут или reuse-detection).
	ErrRefreshRevoked = errors.New("hubkit/issuer: session revoked (logged out)")
	// ErrRefreshReused — токен предъявлен повторно за пределами grace-окна:
	// признак утечки, семейство отозвано.
	ErrRefreshReused = errors.New("hubkit/issuer: refresh token reused, session revoked")
	// ErrRefreshInFlight — тот же токен обменивается прямо сейчас (параллельные
	// вкладки, ретрай после обрыва). Семейство не трогаем: клиент уже получил
	// Set-Cookie победителя, повтор запроса пройдёт с новым токеном.
	ErrRefreshInFlight = errors.New("hubkit/issuer: refresh token exchange already in flight")
	// ErrNoSession — Session без sid или sub: tombstone логаута адресуется по
	// sid, а claims — по sub.
	ErrNoSession = errors.New("hubkit/issuer: session requires sid and sub")
	// ErrNoCompany — customer-сессия без company_id: видимость заказчика
	// определяется его компанией, и продукт такой токен всё равно отвергнет
	// (sso.ErrNoCompany), поэтому issuer его не выпускает.
	ErrNoCompany = errors.New("hubkit/issuer: customer session requires company_id")
)

const (
	refreshKeyPrefix   = "hub:refresh:"
	usedKeyPrefix      = "hub:refresh:used:"
	tombstoneKeyPrefix = "hub:revoked:"

	// detachedWriteTimeout — дедлайн записи, которая переживает ctx запроса
	// (снятие резерва, отзыв семейства при детекте утечки).
	detachedWriteTimeout = 5 * time.Second
)

// Session — identity refresh-цепочки: то, из чего issuer соберёт claims нового
// access-токена. SID связывает цепочку с tombstone логаута. Заполнены поля
// своего контура: employee_id/username/role у сотрудника,
// company_id/account_role/products у заказчика.
type Session struct {
	// SID — идентификатор сессии (адрес tombstone'а).
	SID string `json:"sid"`
	// Subject — sub токена: Authentik subject или customer_account_id.
	Subject string `json:"sub"`
	// Kind — контур сессии (hubclaims.KindEmployee | hubclaims.KindCustomer);
	// пусто = сотрудник.
	Kind string `json:"kind,omitempty"`
	// EmployeeID — hub-UUID сотрудника.
	EmployeeID string `json:"employee_id,omitempty"`
	// Username — логин сотрудника.
	Username string `json:"username,omitempty"`
	// Name — ФИО принципала.
	Name string `json:"name,omitempty"`
	// Email — почта принципала.
	Email string `json:"email,omitempty"`
	// Role — роль сотрудника в экосистеме.
	Role string `json:"role,omitempty"`
	// CompanyID — hub-UUID компании заказчика.
	CompanyID string `json:"company_id,omitempty"`
	// AccountRole — роль заказчика внутри своей компании (member|admin).
	AccountRole string `json:"account_role,omitempty"`
	// Products — продукты, куда пущен заказчик.
	Products []string `json:"products,omitempty"`
}

// Claims — набор claim'ов сессии для Signer.Sign по контракту hub-session JWT
// (константы hubkit/hubclaims). Пустые поля не попадают в токен;
// зарезервированные issuer'ом claims метод не выставляет.
func (s Session) Claims() map[string]any {
	claims := map[string]any{"sub": s.Subject}
	put := func(name, value string) {
		if value != "" {
			claims[name] = value
		}
	}
	put(hubclaims.SID, s.SID)
	put(hubclaims.Kind, s.Kind)
	put(hubclaims.EmployeeID, s.EmployeeID)
	put(hubclaims.Username, s.Username)
	put(hubclaims.Name, s.Name)
	put(hubclaims.Email, s.Email)
	put(hubclaims.Role, s.Role)
	put(hubclaims.CompanyID, s.CompanyID)
	put(hubclaims.AccountRole, s.AccountRole)
	if len(s.Products) > 0 {
		claims[hubclaims.Products] = s.Products
	}
	return claims
}

func (s Session) validate() error {
	if s.SID == "" || s.Subject == "" {
		return ErrNoSession
	}
	if s.Kind == hubclaims.KindCustomer && s.CompanyID == "" {
		return ErrNoCompany
	}
	return nil
}

// usedRecord — маркер обмена токена: кто обменял (sid) и когда (для grace-окна).
type usedRecord struct {
	SID string `json:"sid,omitempty"`
	At  int64  `json:"at"`
}

// RefreshManager реализует rotating refresh поверх cachekit.Cache: обмен
// резервирует маркер токена (SetNX), гасит запись (GetDel) и выдаёт новый
// токен. В кэше лежат только sha256-хэши токенов.
type RefreshManager struct {
	cache        cachekit.Cache
	ttl          time.Duration
	tombstoneTTL time.Duration
	reuseGrace   time.Duration
	clock        clockkit.Clock
}

// NewRefreshManager строит менеджер поверх кэша; конфиг валидируется (ErrConfig).
func NewRefreshManager(cache cachekit.Cache, cfg RefreshConfig, opts ...Option) (*RefreshManager, error) {
	if cache == nil {
		return nil, fmt.Errorf("%w: cache is required", ErrConfig)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	o := options{clock: clockkit.Real{}}
	for _, apply := range opts {
		apply(&o)
	}
	return &RefreshManager{
		cache:        cache,
		ttl:          cfg.TTL,
		tombstoneTTL: cfg.TombstoneTTL,
		reuseGrace:   cfg.ReuseGrace,
		clock:        o.clock,
	}, nil
}

// Issue создаёт новый refresh-токен для сессии (после логина). Возвращает
// непрозрачный токен для cookie/ответа.
func (m *RefreshManager) Issue(ctx context.Context, sess Session) (string, error) {
	if err := sess.validate(); err != nil {
		return "", err
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := m.save(ctx, token, sess); err != nil {
		return "", err
	}
	return token, nil
}

// Rotate обменивает refresh-токен на сессию + новый токен.
//
// Исходы: неизвестный/истёкший токен → ErrRefreshInvalid; сессия после логаута
// → ErrRefreshRevoked; тот же токен в параллельном обмене (внутри
// HUB_REFRESH_REUSE_GRACE) → ErrRefreshInFlight без отзыва; повторное
// предъявление позже grace → Revoke(sid) и ErrRefreshReused. Любая ошибка кэша
// — отказ (fail-closed).
func (m *RefreshManager) Rotate(ctx context.Context, oldToken string) (Session, string, error) {
	h := tokenHash(oldToken)
	usedKey := usedKeyPrefix + h
	now := m.clock.Now()
	mark, err := json.Marshal(usedRecord{At: now.UnixNano()})
	if err != nil {
		return Session{}, "", fmt.Errorf("hubkit/issuer: marshal refresh marker: %w", err)
	}

	// Маркер резервируется ДО гашения записи: иначе проигравший в гонке видел бы
	// «токена нет и маркера нет» и получал бы отзыв всего семейства.
	reserved, err := m.cache.SetNX(ctx, usedKey, mark, m.ttl)
	if err != nil {
		return Session{}, "", err
	}
	if !reserved {
		return Session{}, "", m.rejectReplay(ctx, usedKey, now)
	}

	raw, ok, err := m.cache.GetDel(ctx, refreshKeyPrefix+h)
	if err != nil {
		m.release(ctx, usedKey, mark)
		return Session{}, "", err
	}
	if !ok {
		// Токен не существовал: держать резерв под произвольную строку клиента
		// значило бы дать наполнить кэш чужим мусором.
		m.release(ctx, usedKey, mark)
		return Session{}, "", ErrRefreshInvalid
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

	// Маркер связывается с сессией: повтор за пределами grace отзовёт именно её.
	bound, err := json.Marshal(usedRecord{SID: sess.SID, At: now.UnixNano()})
	if err != nil {
		return Session{}, "", fmt.Errorf("hubkit/issuer: marshal refresh marker: %w", err)
	}
	if err := m.cache.Set(ctx, usedKey, bound, m.ttl); err != nil {
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

func (m *RefreshManager) rejectReplay(ctx context.Context, usedKey string, now time.Time) error {
	raw, ok, err := m.cache.Get(ctx, usedKey)
	if err != nil {
		return err
	}
	if !ok {
		// Маркер истёк между SetNX и Get: судить о повторе не по чему.
		return ErrRefreshInvalid
	}
	var rec usedRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("hubkit/issuer: corrupt refresh marker: %w", err)
	}
	if now.Sub(time.Unix(0, rec.At)) <= m.reuseGrace {
		return ErrRefreshInFlight
	}
	if rec.SID == "" {
		// Обмен не дошёл до выдачи: живого токена этой цепочки нет.
		return ErrRefreshInvalid
	}
	// Иначе обрыв соединения сразу после предъявления утёкшего токена подавил бы отзыв.
	revokeCtx, cancel := detached(ctx)
	defer cancel()
	if err := m.Revoke(revokeCtx, rec.SID); err != nil {
		return err
	}
	return fmt.Errorf("%w: sid %s", ErrRefreshReused, rec.SID)
}

// release снимает собственный резерв: CompareAndDelete не тронет маркер,
// который к этому моменту перезаписал победитель обмена.
func (m *RefreshManager) release(ctx context.Context, key string, mark []byte) {
	releaseCtx, cancel := detached(ctx)
	defer cancel()
	_, _ = m.cache.CompareAndDelete(releaseCtx, key, mark)
}

// detached отвязывает запись от отмены ctx запроса и держит собственный
// дедлайн: без него операция висела бы на недоступном кэше без границы.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), detachedWriteTimeout)
}

// Revoke ставит tombstone на сессию (логаут): любой ещё живой refresh-токен
// этой sid перестаёт обмениваться.
func (m *RefreshManager) Revoke(ctx context.Context, sid string) error {
	if sid == "" {
		return ErrNoSession
	}
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
