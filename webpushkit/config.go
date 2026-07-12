package webpushkit

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config — параметры VAPID-идентичности отправителя. Ключи — raw ES256 (P-256)
// в base64url, как их отдаёт генератор VAPID-пары (см. GenerateVAPIDKeys); тот
// же public-ключ браузер передаёт в PushManager.subscribe как applicationServerKey.
//
// Имена env финализируются при первом потребителе (нужен, notification-домен) —
// envconfig-теги здесь часть публичного API kit'а.
type Config struct {
	VAPIDPublicKey  string        `envconfig:"WEBPUSH_VAPID_PUBLIC_KEY"`
	VAPIDPrivateKey string        `envconfig:"WEBPUSH_VAPID_PRIVATE_KEY"`
	Subject         string        `envconfig:"WEBPUSH_SUBJECT"`
	DefaultTTL      time.Duration `envconfig:"WEBPUSH_DEFAULT_TTL" default:"24h"`
}

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
