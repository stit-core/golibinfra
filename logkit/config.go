package logkit

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kelseyhightower/envconfig"
)

var ErrConfig = errors.New("logkit: invalid config")

type Config struct {
	// Level: debug/info/warn/error (регистр не важен); пустая строка = info.
	Level string `envconfig:"LOG_LEVEL" default:"info"`
	// Stdout по умолчанию через envconfig=true; в struct-литерале zero-value
	// false — задавайте явно. false — JSON-sink отбрасывает записи (io.Discard).
	Stdout bool `envconfig:"LOG_STDOUT" default:"true"`
}

func (c Config) Validate() error {
	_, err := parseLevel(c.Level)
	return err
}

// Patterns — regex-фильтры redact'а. В ENV разделитель «;», потому что запятая
// встречается внутри квантификаторов ({10,15}).
type Patterns []string

func (p *Patterns) Decode(value string) error {
	var out Patterns
	for _, s := range strings.Split(value, ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	*p = out
	return nil
}

type RedactConfig struct {
	Enabled     bool     `envconfig:"LOG_REDACT_ENABLED"`
	Patterns    Patterns `envconfig:"LOG_REDACT_PATTERNS"`
	ReplaceWith string   `envconfig:"LOG_REDACT_REPLACE_WITH" default:"[REDACTED]"`
}

// ServiceConfig — идентичность сервиса для WithService: статические поля
// service/env/version в каждой записи, когда сборщик логов не получает
// OTel-resource (файловый sink без коллектора).
type ServiceConfig struct {
	Service string `envconfig:"OTEL_SERVICE_NAME"`
	Env     string `envconfig:"APP_ENV" default:"dev"`
	Version string `envconfig:"OTEL_SERVICE_VERSION" default:"dev"`
}

func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func NewRedactConfig() (RedactConfig, error) {
	var c RedactConfig
	return c, envconfig.Process("", &c)
}

func NewServiceConfig() (ServiceConfig, error) {
	var c ServiceConfig
	return c, envconfig.Process("", &c)
}

func parseLevel(s string) (slog.Level, error) {
	if s == "" {
		return slog.LevelInfo, nil
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("%w: LOG_LEVEL %q: %w", ErrConfig, s, err)
	}
	return lvl, nil
}
