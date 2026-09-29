package logkit

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kelseyhightower/envconfig"
)

// ErrConfig — невалидный уровень, формат, regex или строка-замена redact'а.
var ErrConfig = errors.New("logkit: invalid config")

// Config — уровень, формат и назначение локального sink'а. Zero-value валиден:
// info-уровень, JSON, stdout.
type Config struct {
	Level  string `envconfig:"LOG_LEVEL" default:"info"`
	Format string `envconfig:"LOG_FORMAT" default:"json"`
	Source bool   `envconfig:"LOG_SOURCE"`
	Silent bool   `envconfig:"LOG_SILENT"`
}

// Validate проверяет уровень и формат; вызывается конструкторами логгера.
func (c Config) Validate() error {
	if _, err := parseLevel(c.Level); err != nil {
		return err
	}
	_, err := parseFormat(c.Format)
	return err
}

// Patterns — regex-фильтры redact'а. В ENV разделитель «;», потому что запятая
// встречается внутри квантификаторов ({10,15}); литеральная «;» — как «\;».
type Patterns []string

// Decode реализует envconfig.Decoder: режет значение по неэкранированным «;».
func (p *Patterns) Decode(value string) error {
	var (
		out Patterns
		cur strings.Builder
	)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c == '\\' && i+1 < len(value):
			// «\;» остаётся в паттерне: RE2 читает это как литеральную «;».
			cur.WriteByte(c)
			i++
			cur.WriteByte(value[i])
		case c == ';':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	*p = out
	return nil
}

// RedactConfig — маскирование PII и секретов в msg и значениях атрибутов.
// Нулевое значение маскирует дефолтным набором; выключает только Disabled.
type RedactConfig struct {
	Disabled    bool     `envconfig:"LOG_REDACT_DISABLED"`
	Patterns    Patterns `envconfig:"LOG_REDACT_PATTERNS"`
	ReplaceWith string   `envconfig:"LOG_REDACT_REPLACE_WITH" default:"[REDACTED]"`
}

// Validate компилирует паттерны и проверяет, что ReplaceWith не ломает JSON.
// Паттерн, совпадающий с пустой строкой, отвергается: он маскирует каждую
// позицию записи.
func (c RedactConfig) Validate() error {
	_, err := compileRedact(c)
	return err
}

// ServiceConfig — идентичность сервиса для WithService: статические поля
// service/env/version в каждой записи, когда сборщик логов не получает
// OTel-resource (файловый sink без коллектора).
type ServiceConfig struct {
	Service string `envconfig:"OTEL_SERVICE_NAME"`
	Env     string `envconfig:"APP_ENV" default:"dev"`
	Version string `envconfig:"OTEL_SERVICE_VERSION" default:"dev"`
}

// NewConfig читает LOG_* из окружения и валидирует результат.
func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// NewRedactConfig читает LOG_REDACT_* из окружения и валидирует результат.
func NewRedactConfig() (RedactConfig, error) {
	var c RedactConfig
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// NewServiceConfig читает идентичность сервиса из окружения.
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

type logFormat int

const (
	formatJSON logFormat = iota
	formatText
)

func parseFormat(s string) (logFormat, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "json":
		return formatJSON, nil
	case "text":
		return formatText, nil
	default:
		return 0, fmt.Errorf("%w: LOG_FORMAT %q: want \"json\" or \"text\"", ErrConfig, s)
	}
}
