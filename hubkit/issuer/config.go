package issuer

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// KeyConfig — источник ES256-ключей подписи. Ключ задаётся inline-PEM
// (HUB_SESSION_PRIVATE_KEY) либо путём к файлу (HUB_SESSION_PRIVATE_KEY_FILE);
// файл имеет приоритет. Next-ключ публикуется в JWKS вместе с активным на период
// ротации; для него достаточно публичного PEM (`openssl ec -in next.pem -pubout`).
type KeyConfig struct {
	ActivePEM  string `envconfig:"HUB_SESSION_PRIVATE_KEY"`
	ActiveFile string `envconfig:"HUB_SESSION_PRIVATE_KEY_FILE"`
	NextPEM    string `envconfig:"HUB_SESSION_NEXT_PRIVATE_KEY"`
	NextFile   string `envconfig:"HUB_SESSION_NEXT_PRIVATE_KEY_FILE"`
	Issuer     string `default:"https://id.stit.tech" envconfig:"HUB_SESSION_ISSUER"`
	// Audience — aud выпускаемых токенов; обязателен: sso-verifier проверяет aud всегда.
	Audience string `envconfig:"HUB_SESSION_AUDIENCE"`
}

func (c KeyConfig) activePEM() (string, error) { return readPEM(c.ActiveFile, c.ActivePEM) }
func (c KeyConfig) nextPEM() (string, error)   { return readPEM(c.NextFile, c.NextPEM) }

func readPEM(file, inline string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file) //nolint:gosec // path is operator-provided config, not user input
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
	Name   string        `default:"stit_session" envconfig:"HUB_SESSION_COOKIE"`
	Domain string        `default:".stit.tech"   envconfig:"HUB_SESSION_COOKIE_DOMAIN"`
	Secure bool          `default:"true"         envconfig:"HUB_SESSION_COOKIE_SECURE"`
	TTL    time.Duration `default:"15m"          envconfig:"HUB_SESSION_TTL"`
	// RefreshName — имя refresh-cookie.
	RefreshName string `default:"stit_refresh" envconfig:"HUB_SESSION_REFRESH_COOKIE"`
	// RefreshPath — Path refresh-cookie: браузер шлёт её только на
	// refresh/logout-роуты issuer'а, продуктовые сабдомены её не видят.
	RefreshPath string `default:"/auth" envconfig:"HUB_SESSION_REFRESH_COOKIE_PATH"`
}

// RefreshConfig — параметры rotating-refresh контура. NewRefreshManager
// требует TTL > 0 и TombstoneTTL >= TTL.
type RefreshConfig struct {
	// TTL — срок жизни refresh-токена (окно бездействия до повторного логина).
	TTL time.Duration `default:"720h" envconfig:"HUB_REFRESH_TTL"`
	// TombstoneTTL — сколько держим метку отзыва сессии после логаута (>= TTL,
	// чтобы утёкший refresh-токен не пережил tombstone).
	TombstoneTTL time.Duration `default:"720h" envconfig:"HUB_REFRESH_TOMBSTONE_TTL"`
}
