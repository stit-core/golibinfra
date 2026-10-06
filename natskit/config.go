package natskit

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
	"git.stit.tech/stit-core/golibinfra/internal/redact"
)

// DefaultInstance — имя дефолтного (безымянного) инстанса в метриках и спанах.
const DefaultInstance = "default"

// ErrConfig — невалидный конфиг инстанса: возвращается Validate, NewConfig,
// NewInstanceConfig и New, чтобы сервис падал на старте.
var ErrConfig = errors.New("natskit: invalid config")

// minAckWait — ниже этого порога +WPI не успевает продлить ожидание сервера.
const minAckWait = time.Second

// BackpressureConfig — потолок одновременно обрабатываемых Core-сообщений.
type BackpressureConfig struct {
	MaxInFlight int `envconfig:"NATS_MAX_IN_FLIGHT" default:"100"`
}

// JetStreamConfig — включение JetStream, декларация стримов и poison-обработка.
type JetStreamConfig struct {
	Enabled       bool          `envconfig:"NATS_JETSTREAM_ENABLED" default:"false"`
	StreamNames   []string      `envconfig:"NATS_JS_STREAM_NAMES" default:""`
	StreamManaged bool          `envconfig:"NATS_JS_STREAM_MANAGED" default:"true"`
	Retention     string        `envconfig:"NATS_JS_RETENTION" default:"limits"`
	MaxAge        time.Duration `envconfig:"NATS_JS_MAX_AGE" default:"168h"`
	Duplicates    time.Duration `envconfig:"NATS_JS_DUPLICATES" default:"2m"`
	MaxInFlight   int           `envconfig:"NATS_JS_MAX_IN_FLIGHT" default:"100"`
	AckWait       time.Duration `envconfig:"NATS_JS_ACK_WAIT" default:"30s"`
	// HandlerTimeout — deadline ctx одной попытки обработчика; 0 — равен AckWait.
	HandlerTimeout      time.Duration `envconfig:"NATS_JS_HANDLER_TIMEOUT" default:"0"`
	PublishAsyncTimeout time.Duration `envconfig:"NATS_JS_PUBLISH_ASYNC_TIMEOUT" default:"5s"`
	// MaxDeliver, NakBackoff и NakMaxBackoff — бюджет ретраев: задержка NAK после n-й
	// доставки — NakBackoff·2^(n−1), не больше NakMaxBackoff; дефолты держат сообщение
	// 15 минут сбоя зависимости.
	MaxDeliver    int           `envconfig:"NATS_JS_MAX_DELIVER" default:"21"`
	NakBackoff    time.Duration `envconfig:"NATS_JS_NAK_BACKOFF" default:"1s"`
	NakMaxBackoff time.Duration `envconfig:"NATS_JS_NAK_MAX_BACKOFF" default:"1m"`
	DLQSubject    string        `envconfig:"NATS_JS_DLQ_SUBJECT" default:""`
}

// Config — публичный env-контракт инстанса шины.
type Config struct {
	URL            string        `envconfig:"NATS_URL" default:"nats://localhost:4222"`
	ConnectTimeout time.Duration `envconfig:"NATS_CONNECT_TIMEOUT" default:"5s"`
	ReconnectWait  time.Duration `envconfig:"NATS_RECONNECT_WAIT" default:"2s"`
	MaxReconnects  int           `envconfig:"NATS_MAX_RECONNECTS" default:"-1"`
	DrainTimeout   time.Duration `envconfig:"NATS_DRAIN_TIMEOUT" default:"10s"`
	// HandlerTimeout — deadline ctx Core-обработчика; 0 — без ограничения.
	HandlerTimeout time.Duration `envconfig:"NATS_HANDLER_TIMEOUT" default:"30s"`
	CredsFile      string        `envconfig:"NATS_CREDS_FILE" default:""`
	NKeySeedFile   string        `envconfig:"NATS_NKEY_SEED_FILE" default:""`
	TLSCAFile      string        `envconfig:"NATS_TLS_CA_FILE" default:""`
	Backpressure   BackpressureConfig
	JetStream      JetStreamConfig

	// Instance — имя инстанса шины: едет атрибутом в метрики, спаны и логи, чтобы
	// сигналы двух шин в одном процессе не сливались. Из ENV не читается,
	// проставляется New/Module.
	Instance string `ignored:"true"`
}

// NewConfig читает и валидирует конфиг дефолтного инстанса из `NATS_*`.
func NewConfig() (Config, error) {
	return NewInstanceConfig("")
}

// NewInstanceConfig читает и валидирует конфиг инстанса name из `<NAME>_NATS_*`. Голые
// `NATS_*` ему не достаются — набор переменных инстанса изолирован; пустое имя — NewConfig.
func NewInstanceConfig(name string) (Config, error) {
	if name != "" && !validInstanceName(name) {
		return Config{}, fmt.Errorf("%w: instance %q: allowed characters are letters, digits, '_', '-', '.'", ErrConfig, name)
	}
	var c Config
	if err := envconf.LoadPrefixed(&c, envPrefix(name), false); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	c.Instance = name
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate проверяет конфиг целиком: невалидное значение валит старт сервиса, а не первую
// публикацию. Ноль — значение, а не дефолт кита: таймаут подключения, пауза reconnect'а,
// бюджет drain и потолок обработчиков обязаны быть > 0. Формат JetStream-полей проверяется
// всегда, обязательные значения — при Enabled.
func (c Config) Validate() error {
	if err := validateURL(c.URL); err != nil {
		return err
	}
	if c.Instance != "" && !validInstanceName(c.Instance) {
		return fmt.Errorf("%w: instance %q: allowed characters are letters, digits, '_', '-', '.'", ErrConfig, c.Instance)
	}
	for _, d := range []struct {
		name string
		v    time.Duration
	}{
		{"NATS_CONNECT_TIMEOUT", c.ConnectTimeout},
		{"NATS_RECONNECT_WAIT", c.ReconnectWait},
		{"NATS_DRAIN_TIMEOUT", c.DrainTimeout},
	} {
		if d.v <= 0 {
			return fmt.Errorf("%w: %s must be > 0, got %s", ErrConfig, d.name, d.v)
		}
	}
	if c.MaxReconnects < -1 {
		return fmt.Errorf("%w: NATS_MAX_RECONNECTS must be >= -1, got %d", ErrConfig, c.MaxReconnects)
	}
	if err := c.validateBus(); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		path string
	}{
		{"NATS_CREDS_FILE", c.CredsFile},
		{"NATS_NKEY_SEED_FILE", c.NKeySeedFile},
		{"NATS_TLS_CA_FILE", c.TLSCAFile},
	} {
		if f.path == "" {
			continue
		}
		if _, err := os.Stat(f.path); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrConfig, f.name, err)
		}
	}
	return c.JetStream.validate()
}

// validateBus — поля Core-шины: их проверяет и NewBus поверх готового соединения.
func (c Config) validateBus() error {
	if c.HandlerTimeout < 0 {
		return fmt.Errorf("%w: NATS_HANDLER_TIMEOUT must be >= 0, got %s", ErrConfig, c.HandlerTimeout)
	}
	if c.Backpressure.MaxInFlight < 1 {
		return fmt.Errorf("%w: NATS_MAX_IN_FLIGHT must be >= 1, got %d", ErrConfig, c.Backpressure.MaxInFlight)
	}
	return nil
}

func (j JetStreamConfig) validate() error {
	for _, d := range []struct {
		name string
		v    time.Duration
	}{
		{"NATS_JS_MAX_AGE", j.MaxAge},
		{"NATS_JS_DUPLICATES", j.Duplicates},
		{"NATS_JS_ACK_WAIT", j.AckWait},
		{"NATS_JS_HANDLER_TIMEOUT", j.HandlerTimeout},
		{"NATS_JS_PUBLISH_ASYNC_TIMEOUT", j.PublishAsyncTimeout},
		{"NATS_JS_NAK_BACKOFF", j.NakBackoff},
		{"NATS_JS_NAK_MAX_BACKOFF", j.NakMaxBackoff},
	} {
		if d.v < 0 {
			return fmt.Errorf("%w: %s must be >= 0, got %s", ErrConfig, d.name, d.v)
		}
	}
	if j.MaxInFlight < 0 {
		return fmt.Errorf("%w: NATS_JS_MAX_IN_FLIGHT must be >= 0, got %d", ErrConfig, j.MaxInFlight)
	}
	if j.MaxDeliver < 0 {
		return fmt.Errorf("%w: NATS_JS_MAX_DELIVER must be >= 0, got %d", ErrConfig, j.MaxDeliver)
	}
	if j.Retention != "" {
		if _, err := parseRetention(j.Retention); err != nil {
			return err
		}
	}
	for _, name := range j.StreamNames {
		if !validStreamName(name) {
			return fmt.Errorf("%w: NATS_JS_STREAM_NAMES: %q is not a valid stream name", ErrConfig, name)
		}
	}
	if j.DLQSubject != "" && !validPublishSubject(j.DLQSubject) {
		return fmt.Errorf("%w: NATS_JS_DLQ_SUBJECT: %q is not a valid publish subject", ErrConfig, j.DLQSubject)
	}
	if !j.Enabled {
		return nil
	}
	switch {
	case j.MaxAge == 0, j.Duplicates == 0, j.PublishAsyncTimeout == 0:
		return fmt.Errorf("%w: NATS_JS_MAX_AGE, NATS_JS_DUPLICATES and NATS_JS_PUBLISH_ASYNC_TIMEOUT must be > 0", ErrConfig)
	case j.AckWait < minAckWait:
		return fmt.Errorf("%w: NATS_JS_ACK_WAIT must be >= %s, got %s", ErrConfig, minAckWait, j.AckWait)
	case j.MaxInFlight < 1:
		return fmt.Errorf("%w: NATS_JS_MAX_IN_FLIGHT must be >= 1, got %d", ErrConfig, j.MaxInFlight)
	}
	_, err := parseRetention(j.Retention)
	return err
}

var allowedSchemes = map[string]bool{"nats": true, "tls": true, "ws": true, "wss": true}

func validateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%w: NATS_URL is empty", ErrConfig)
	}
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("%w: NATS_URL %q has an empty entry", ErrConfig, redactURL(raw))
		}
		// nats.go подставляет схему сама; без неё url.Parse принял бы host за схему.
		if !strings.Contains(part, "://") {
			part = "nats://" + part
		}
		u, err := url.Parse(part)
		if err != nil {
			return fmt.Errorf("%w: NATS_URL %q: %w", ErrConfig, redactURL(raw), redact.ParseError(err))
		}
		if !allowedSchemes[u.Scheme] {
			return fmt.Errorf("%w: NATS_URL %q: unsupported scheme %q (nats, tls, ws, wss)", ErrConfig, redactURL(raw), u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("%w: NATS_URL %q: host is empty", ErrConfig, redactURL(raw))
		}
	}
	return nil
}

func validInstanceName(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

func validStreamName(name string) bool {
	if name == "" {
		return false
	}
	return !strings.ContainsAny(name, ". \t*>/\\")
}

// validPublishSubject — subject, в который можно опубликовать: непустые токены,
// без пробелов и без wildcard'ов (`*`/`>` осмысленны только в подписке).
func validPublishSubject(subject string) bool {
	if subject == "" || strings.ContainsAny(subject, " \t\r\n*>") {
		return false
	}
	return !slices.Contains(strings.Split(subject, "."), "")
}

// envPrefix — префикс переменных окружения инстанса: `hub` → `HUB_NATS_URL`.
func envPrefix(instance string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(instance))
}
