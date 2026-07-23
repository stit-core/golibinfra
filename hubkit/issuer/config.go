package issuer

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// KeyConfig — источник ES256-ключей подписи. Ключ можно задать либо inline-PEM
// (HUB_SESSION_PRIVATE_KEY), либо путём к файлу (HUB_SESSION_PRIVATE_KEY_FILE);
// файл имеет приоритет. NEXT-ключ — публикуется в JWKS вместе с активным на
// период ротации (сгенерировать: `openssl ecparam -genkey -name prime256v1 -noout`).
type KeyConfig struct {
	ActivePEM  string `envconfig:"HUB_SESSION_PRIVATE_KEY"`
	ActiveFile string `envconfig:"HUB_SESSION_PRIVATE_KEY_FILE"`
	NextPEM    string `envconfig:"HUB_SESSION_NEXT_PRIVATE_KEY"`
	NextFile   string `envconfig:"HUB_SESSION_NEXT_PRIVATE_KEY_FILE"`
	Issuer     string `default:"https://id.stit.tech" envconfig:"HUB_SESSION_ISSUER"`
	Audience   string `envconfig:"HUB_SESSION_AUDIENCE"`
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

// CookieConfig — параметры hub-session cookie. Дефолты соответствуют
// 00-architecture §3: Domain=.stit.tech, HttpOnly, Secure, SameSite=Lax, TTL 15м.
type CookieConfig struct {
	Name   string        `default:"stit_session" envconfig:"HUB_SESSION_COOKIE"`
	Domain string        `default:".stit.tech"   envconfig:"HUB_SESSION_COOKIE_DOMAIN"`
	Secure bool          `default:"true"         envconfig:"HUB_SESSION_COOKIE_SECURE"`
	TTL    time.Duration `default:"15m"          envconfig:"HUB_SESSION_TTL"`
}

// RefreshConfig — параметры rotating-refresh контура (Redis).
type RefreshConfig struct {
	// TTL — срок жизни refresh-токена (окно бездействия до повторного логина).
	TTL time.Duration `default:"720h" envconfig:"HUB_REFRESH_TTL"`
	// TombstoneTTL — сколько держим метку отзыва сессии после логаута (≥ TTL,
	// чтобы утёкший refresh-токен не пережил tombstone).
	TombstoneTTL time.Duration `default:"720h" envconfig:"HUB_REFRESH_TOMBSTONE_TTL"`
}
