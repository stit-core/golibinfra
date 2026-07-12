package webpushkit

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Subscription — то, что отдаёт браузерный PushManager.subscribe (поля
// endpoint + keys.p256dh + keys.auth). Kit подписки не хранит — это зона
// потребителя (notification-домен); сюда они приходят как есть.
type Subscription struct {
	Endpoint string // URL push-сервиса, куда POST'ится зашифрованное сообщение
	P256dh   string // публичный ключ user agent'а (uncompressed P-256, base64url)
	Auth     string // authentication secret подписки (16 байт, base64url)
}

// decodeBase64 принимает base64 в любом из вариантов, которыми потребители
// сериализуют ключи PushManager: url/std, с паддингом и без.
func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return base64.RawURLEncoding.DecodeString(s)
}

// keys декодирует и валидирует p256dh (65-байтовый uncompressed-пойнт проверит
// ecdh.NewPublicKey уже при шифровании) и auth (ровно 16 байт по RFC 8291).
func (s Subscription) keys() (p256dh, auth []byte, err error) {
	p256dh, err = decodeBase64(s.P256dh)
	if err != nil {
		return nil, nil, fmt.Errorf("webpushkit: decode p256dh: %w", ErrSubscription)
	}
	auth, err = decodeBase64(s.Auth)
	if err != nil {
		return nil, nil, fmt.Errorf("webpushkit: decode auth: %w", ErrSubscription)
	}
	if len(auth) != authSecretLen {
		return nil, nil, fmt.Errorf("webpushkit: auth secret must be %d bytes, got %d: %w", authSecretLen, len(auth), ErrSubscription)
	}
	return p256dh, auth, nil
}
