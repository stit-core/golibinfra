package webpushkit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys: %v", err)
	}
	return Config{
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
		Subject:         "mailto:ops@stit.tech",
		DefaultTTL:      24 * time.Hour,
	}
}

// pubKeyFromB64 восстанавливает *ecdsa.PublicKey из k= (uncompressed P-256).
func pubKeyFromB64(t *testing.T, b64 string) *ecdsa.PublicKey {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode pub: %v", err)
	}
	if len(raw) != 65 || raw[0] != 0x04 {
		t.Fatalf("pub not uncompressed P-256: len=%d", len(raw))
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(raw[1:33]),
		Y:     new(big.Int).SetBytes(raw[33:65]),
	}
}

func TestVAPIDAuthHeader_ParseAndVerify(t *testing.T) {
	cfg := testConfig(t)
	signer, err := newVAPIDSigner(cfg)
	if err != nil {
		t.Fatalf("newVAPIDSigner: %v", err)
	}

	now := time.Now()
	header, err := signer.authHeader("https://fcm.googleapis.com/fcm/send/abc123", now)
	if err != nil {
		t.Fatalf("authHeader: %v", err)
	}

	tokenStr, k, ok := parseVapidHeader(header)
	if !ok {
		t.Fatalf("malformed Authorization: %q", header)
	}
	if k != cfg.VAPIDPublicKey {
		t.Fatalf("k=%q, want %q", k, cfg.VAPIDPublicKey)
	}

	// Проверяем подпись собственным public-ключом из k=.
	pub := pubKeyFromB64(t, k)
	claims := jwt.MapClaims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, errors.New("unexpected alg")
		}
		return pub, nil
	})
	if err != nil {
		t.Fatalf("verify jwt: %v", err)
	}
	if !tok.Valid {
		t.Fatal("jwt not valid")
	}
	if aud, _ := claims["aud"].(string); aud != "https://fcm.googleapis.com" {
		t.Fatalf("aud=%v, want origin of endpoint", claims["aud"])
	}
	if sub, _ := claims["sub"].(string); sub != cfg.Subject {
		t.Fatalf("sub=%v, want %q", claims["sub"], cfg.Subject)
	}
	exp, _ := claims["exp"].(float64)
	if exp <= float64(now.Unix()) || exp > float64(now.Add(24*time.Hour).Unix()) {
		t.Fatalf("exp=%v out of (now, now+24h]", exp)
	}
}

func TestVAPIDAuthHeader_ForeignKeyRejected(t *testing.T) {
	signer, err := newVAPIDSigner(testConfig(t))
	if err != nil {
		t.Fatalf("newVAPIDSigner: %v", err)
	}
	header, err := signer.authHeader("https://push.example/ep", time.Now())
	if err != nil {
		t.Fatalf("authHeader: %v", err)
	}
	tokenStr, _, _ := parseVapidHeader(header)

	// Чужой ключ не должен верифицировать подпись.
	otherPub, _, _ := GenerateVAPIDKeys()
	_, err = jwt.Parse(tokenStr, func(*jwt.Token) (any, error) {
		return pubKeyFromB64(t, otherPub), nil
	})
	if err == nil {
		t.Fatal("подпись прошла проверку чужим ключом")
	}
}

func TestNewVAPIDSigner_Config(t *testing.T) {
	valid := testConfig(t)

	cases := map[string]func(Config) Config{
		"empty private": func(c Config) Config { c.VAPIDPrivateKey = ""; return c },
		"bad subject":   func(c Config) Config { c.Subject = "ops@stit.tech"; return c },
		"empty subject": func(c Config) Config { c.Subject = ""; return c },
		"garbage private key": func(c Config) Config {
			c.VAPIDPrivateKey = "not-base64-@@@"
			return c
		},
		"mismatched public": func(c Config) Config {
			other, _, _ := GenerateVAPIDKeys()
			c.VAPIDPublicKey = other
			return c
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newVAPIDSigner(mutate(valid)); !errors.Is(err, ErrConfig) {
				t.Fatalf("ожидали ErrConfig, получили %v", err)
			}
		})
	}
}

func TestNewVAPIDSigner_HTTPSSubject(t *testing.T) {
	cfg := testConfig(t)
	cfg.Subject = "https://stit.tech/contact"
	if _, err := newVAPIDSigner(cfg); err != nil {
		t.Fatalf("https subject должен приниматься: %v", err)
	}
}

func parseVapidHeader(h string) (token, k string, ok bool) {
	rest, found := strings.CutPrefix(h, "vapid ")
	if !found {
		return "", "", false
	}
	for _, part := range strings.Split(rest, ", ") {
		switch {
		case strings.HasPrefix(part, "t="):
			token = strings.TrimPrefix(part, "t=")
		case strings.HasPrefix(part, "k="):
			k = strings.TrimPrefix(part, "k=")
		}
	}
	return token, k, token != "" && k != ""
}
