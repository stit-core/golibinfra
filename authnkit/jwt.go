package authnkit

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type JWTConfig struct {
	Secret     string        `envconfig:"JWT_SECRET" vault:"jwt_secret" required:"true"`
	AccessTTL  time.Duration `envconfig:"JWT_ACCESS_TTL" default:"15m"`
	RefreshTTL time.Duration `envconfig:"JWT_REFRESH_TTL" default:"720h"`
}

type JWTSigner interface {
	Sign(claims map[string]any, ttl time.Duration) (string, error)
}

type JWTParser interface {
	Parse(token string) (map[string]any, error)
}

var (
	ErrTokenExpired   = errors.New("authnkit: token expired")
	ErrTokenMalformed = errors.New("authnkit: token malformed")
	ErrTokenSignature = errors.New("authnkit: token signature invalid")
)

type HS256JWT struct {
	secret []byte
}

func NewHS256JWT(cfg JWTConfig) *HS256JWT {
	return &HS256JWT{secret: []byte(cfg.Secret)}
}

func (h *HS256JWT) Sign(claims map[string]any, ttl time.Duration) (string, error) {
	mc := jwt.MapClaims{}
	for k, v := range claims {
		mc[k] = v
	}
	mc["exp"] = time.Now().Add(ttl).Unix()
	mc["iat"] = time.Now().Unix()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, mc)
	signed, err := tok.SignedString(h.secret)
	if err != nil {
		return "", fmt.Errorf("authnkit: sign: %w", err)
	}
	return signed, nil
}

func (h *HS256JWT) Parse(token string) (map[string]any, error) {
	tok, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("authnkit: unexpected signing method: %v", t.Header["alg"])
		}
		return h.secret, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			recordJWTResult("expired")
			return nil, ErrTokenExpired
		case errors.Is(err, jwt.ErrTokenSignatureInvalid):
			recordJWTResult("signature_invalid")
			return nil, ErrTokenSignature
		default:
			recordJWTResult("malformed")
			return nil, ErrTokenMalformed
		}
	}
	mc, ok := tok.Claims.(jwt.MapClaims)
	if !ok || !tok.Valid {
		recordJWTResult("malformed")
		return nil, ErrTokenMalformed
	}
	result := make(map[string]any, len(mc))
	for k, v := range mc {
		result[k] = v
	}
	recordJWTResult("ok")
	return result, nil
}
