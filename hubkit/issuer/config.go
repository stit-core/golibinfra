package issuer

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Default* — значения envconfig-дефолтов, продублированные константами для
// конфигов, собранных литералом мимо NewCookieConfig.
const (
	DefaultCookieName        = "stit_session"
	DefaultCookieDomain      = ".stit.tech"
	DefaultRefreshCookieName = "stit_refresh"
	DefaultRefreshCookiePath = "/auth"
	DefaultSessionTTL        = 15 * time.Minute
	DefaultJWKSMaxAge        = 5 * time.Minute
	DefaultReuseGrace        = 10 * time.Second
)

// minReuseGrace — нижняя граница окна параллельного обмена. Нулевое окно
// вернуло бы отзыв семейства на каждую гонку двух вкладок, поэтому конфиг с
// ним не стартует, а не ломается на первом параллельном refresh'е.
const minReuseGrace = time.Second

// KeyConfig — источник ES256-ключей подписи. Ключ задаётся inline-PEM
// (HUB_SESSION_PRIVATE_KEY) либо путём к файлу (HUB_SESSION_PRIVATE_KEY_FILE);
// файл имеет приоритет. Слоты next и previous публикуются в JWKS вместе с
// активным; для них достаточно публичного PEM (`openssl ec -in k.pem -pubout`).
type KeyConfig struct {
	// ActivePEM — активный приватный ключ P-256 (PEM inline).
	ActivePEM string `envconfig:"HUB_SESSION_PRIVATE_KEY"`
	// ActiveFile — путь к файлу активного ключа; приоритетнее inline.
	ActiveFile string `envconfig:"HUB_SESSION_PRIVATE_KEY_FILE"`
	// NextPEM — следующий ключ ротации: публикуется в JWKS до того, как станет активным.
	NextPEM string `envconfig:"HUB_SESSION_NEXT_PRIVATE_KEY"`
	// NextFile — путь к файлу следующего ключа.
	NextFile string `envconfig:"HUB_SESSION_NEXT_PRIVATE_KEY_FILE"`
	// PreviousPEM — предыдущий ключ ротации: остаётся в JWKS, пока живы
	// подписанные им access-токены (>= HUB_SESSION_TTL после переключения).
	PreviousPEM string `envconfig:"HUB_SESSION_PREVIOUS_PUBLIC_KEY"`
	// PreviousFile — путь к файлу предыдущего ключа.
	PreviousFile string `envconfig:"HUB_SESSION_PREVIOUS_PUBLIC_KEY_FILE"`
	// Issuer — iss выпускаемых токенов.
	Issuer string `envconfig:"HUB_SESSION_ISSUER" default:"https://id.stit.tech"`
	// Audience — aud выпускаемых токенов; обязателен: sso-verifier проверяет aud всегда.
	Audience string `envconfig:"HUB_SESSION_AUDIENCE"`
	// JWKSMaxAge — Cache-Control: max-age ответа JWKS. Окно, за которое
	// верификаторы узнают о новом kid; не больше времени публикации next-ключа.
	JWKSMaxAge time.Duration `envconfig:"HUB_SESSION_JWKS_MAX_AGE" default:"5m"`
}

// Validate проверяет источник ключей: невалидный ключ должен ронять старт
// issuer'а, а не первый логин.
func (c KeyConfig) Validate() error {
	if c.Audience == "" {
		return fmt.Errorf("%w: HUB_SESSION_AUDIENCE is required", ErrConfig)
	}
	if c.Issuer == "" {
		return fmt.Errorf("%w: HUB_SESSION_ISSUER is required", ErrConfig)
	}
	u, err := url.Parse(c.Issuer)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: HUB_SESSION_ISSUER must be an absolute http(s) URL", ErrConfig)
	}
	if c.JWKSMaxAge < 0 {
		return fmt.Errorf("%w: HUB_SESSION_JWKS_MAX_AGE must be >= 0", ErrConfig)
	}
	pem, err := c.activePEM()
	if err != nil {
		return err
	}
	if pem == "" {
		return ErrNoActiveKey
	}
	return nil
}

func (c KeyConfig) activePEM() (string, error) { return readPEM(c.ActiveFile, c.ActivePEM) }
func (c KeyConfig) nextPEM() (string, error)   { return readPEM(c.NextFile, c.NextPEM) }
func (c KeyConfig) previousPEM() (string, error) {
	return readPEM(c.PreviousFile, c.PreviousPEM)
}

func (c KeyConfig) jwksMaxAge() time.Duration {
	if c.JWKSMaxAge <= 0 {
		return DefaultJWKSMaxAge
	}
	return c.JWKSMaxAge
}

func readPEM(file, inline string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("hubkit/issuer: read key file %s: %w", file, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(inline), nil
}

// CookieConfig — параметры hub-session cookie: Domain=.stit.tech, HttpOnly,
// Secure, SameSite=Lax, TTL 15м.
//
// Все имена — конфигом, не константой: контуров сессии два (сотрудник и
// заказчик), и второй issuer поднимается со своими именами
// (stit_customer/stit_customer_refresh, Path=/auth/customer).
type CookieConfig struct {
	// Name — имя cookie access-сессии.
	Name string `envconfig:"HUB_SESSION_COOKIE" default:"stit_session"`
	// Domain — Domain cookie: общий для всех продуктов экосистемы.
	Domain string `envconfig:"HUB_SESSION_COOKIE_DOMAIN" default:".stit.tech"`
	// Secure — атрибут Secure; false только в dev-окружении без TLS.
	Secure bool `envconfig:"HUB_SESSION_COOKIE_SECURE" default:"true"`
	// TTL — срок жизни access-сессии (Max-Age cookie и exp токена).
	TTL time.Duration `envconfig:"HUB_SESSION_TTL" default:"15m"`
	// RefreshName — имя refresh-cookie.
	RefreshName string `envconfig:"HUB_SESSION_REFRESH_COOKIE" default:"stit_refresh"`
	// RefreshPath — Path refresh-cookie: браузер шлёт её только на
	// refresh/logout-роуты issuer'а, продуктовые сабдомены её не видят.
	RefreshPath string `envconfig:"HUB_SESSION_REFRESH_COOKIE_PATH" default:"/auth"`
}

// Validate проверяет cookie-слой: cookie без имени http.SetCookie молча не
// пишет, а нулевой TTL дал бы session-cookie без срока.
func (c CookieConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("%w: HUB_SESSION_COOKIE is required", ErrConfig)
	}
	if c.RefreshName == "" {
		return fmt.Errorf("%w: HUB_SESSION_REFRESH_COOKIE is required", ErrConfig)
	}
	if c.Name == c.RefreshName {
		return fmt.Errorf("%w: HUB_SESSION_REFRESH_COOKIE must differ from HUB_SESSION_COOKIE", ErrConfig)
	}
	if !strings.HasPrefix(c.RefreshPath, "/") {
		return fmt.Errorf("%w: HUB_SESSION_REFRESH_COOKIE_PATH must start with /", ErrConfig)
	}
	if c.TTL <= 0 {
		return fmt.Errorf("%w: HUB_SESSION_TTL must be > 0", ErrConfig)
	}
	return nil
}

// RefreshConfig — параметры rotating-refresh контура.
type RefreshConfig struct {
	// TTL — срок жизни refresh-токена (окно бездействия до повторного логина).
	TTL time.Duration `envconfig:"HUB_REFRESH_TTL" default:"720h"`
	// TombstoneTTL — сколько держим метку отзыва сессии после логаута (>= TTL,
	// чтобы утёкший refresh-токен не пережил tombstone).
	TombstoneTTL time.Duration `envconfig:"HUB_REFRESH_TOMBSTONE_TTL" default:"720h"`
	// ReuseGrace — окно, в котором повторное предъявление того же токена
	// считается параллельным обменом (две вкладки, ретрай после обрыва), а не
	// утечкой: клиент уже держит Set-Cookie победителя. Минимум — 1s.
	ReuseGrace time.Duration `envconfig:"HUB_REFRESH_REUSE_GRACE" default:"10s"`
}

// Validate проверяет параметры refresh-контура.
func (c RefreshConfig) Validate() error {
	if c.TTL <= 0 {
		return fmt.Errorf("%w: HUB_REFRESH_TTL must be > 0", ErrConfig)
	}
	if c.TombstoneTTL < c.TTL {
		return fmt.Errorf("%w: HUB_REFRESH_TOMBSTONE_TTL must be >= HUB_REFRESH_TTL", ErrConfig)
	}
	if c.ReuseGrace < minReuseGrace {
		return fmt.Errorf("%w: HUB_REFRESH_REUSE_GRACE must be >= %s (a zero window revokes the family on every parallel exchange)", ErrConfig, minReuseGrace)
	}
	if c.ReuseGrace >= c.TTL {
		return fmt.Errorf("%w: HUB_REFRESH_REUSE_GRACE must be < HUB_REFRESH_TTL", ErrConfig)
	}
	return nil
}
