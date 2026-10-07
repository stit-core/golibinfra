package telemetry

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"git.stit.tech/stit-core/golibinfra/internal/redact"
)

// ErrConfig — sentinel невалидной конфигурации телеметрии.
var ErrConfig = errors.New("telemetry: invalid config")

// defaultExportTimeout — бюджет одного шага Shutdown, когда Providers собраны
// вручную и ExportTimeout неизвестен.
const defaultExportTimeout = 10 * time.Second

// Headers — заголовки OTLP-запросов из OTEL_EXPORTER_OTLP_HEADERS.
type Headers map[string]string

// Decode разбирает OTEL_EXPORTER_OTLP_HEADERS в формате спецификации OTel: `k1=v1,k2=v2`,
// значения в percent-encoding. Ошибка называет ключ, но не значение: в значениях — токены.
func (h *Headers) Decode(value string) error {
	out := Headers{}
	for pair := range strings.SplitSeq(value, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		k, v, found := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !found || k == "" {
			return errors.New("want key=value pairs separated by comma")
		}
		decoded, err := url.PathUnescape(v)
		if err != nil {
			return fmt.Errorf("header %q: invalid percent-escape in the value", k)
		}
		out[k] = strings.TrimSpace(decoded)
	}
	*h = out
	return nil
}

// Config — конфигурация OTel-bootstrap'а; поля и envconfig-теги — public API.
type Config struct {
	// Propagator ставится и при Disabled: сквозной trace-контекст между сервисами
	// не зависит от того, экспортирует ли этот сервис сигналы.
	Disabled bool `envconfig:"OTEL_SDK_DISABLED" default:"false"`

	ServiceName    string `envconfig:"OTEL_SERVICE_NAME"`
	ServiceVersion string `envconfig:"OTEL_SERVICE_VERSION" default:"dev"`
	Environment    string `envconfig:"APP_ENV" default:"dev"`

	// Путь базового URL сохраняется, per-signal `/v1/<signal>` дописывается к нему.
	OTLPEndpoint string  `envconfig:"OTEL_EXPORTER_OTLP_ENDPOINT" default:"http://localhost:4318"`
	SampleRatio  float64 `envconfig:"OTEL_TRACES_SAMPLER_ARG" default:"1.0"`

	ExportTimeout   time.Duration `envconfig:"OTEL_EXPORT_TIMEOUT" default:"10s"`
	TraceBatchDelay time.Duration `envconfig:"OTEL_TRACES_BATCH_DELAY" default:"5s"`
	MetricInterval  time.Duration `envconfig:"OTEL_METRICS_INTERVAL" default:"10s"`
	LogBatchDelay   time.Duration `envconfig:"OTEL_LOGS_BATCH_DELAY" default:"1s"`

	// Tenant — непустой выставляет resource-атрибуты service.namespace и tenant.
	Tenant string `envconfig:"OTEL_TENANT"`

	OTLPHeaders Headers `envconfig:"OTEL_EXPORTER_OTLP_HEADERS"`

	// OTLPBearerToken переопределяет Authorization из OTLPHeaders.
	OTLPBearerToken string `envconfig:"OTEL_TOKEN"`
	// AllowInsecureToken разрешает OTLPBearerToken и Authorization по http:// не на loopback:
	// без него такой конфиг — ошибка старта, токен ушёл бы в сеть открытым.
	AllowInsecureToken bool `envconfig:"OTEL_ALLOW_INSECURE_TOKEN" default:"false"`
}

// Validate проверяет конфиг на старте: невалидные значения должны валить процесс, а не первый
// экспорт. Формат значений проверяется и при Disabled, обязательные значения — только у
// включённого SDK.
func (c Config) Validate() error {
	switch {
	case c.SampleRatio < 0 || c.SampleRatio > 1:
		return fmt.Errorf("%w: OTEL_TRACES_SAMPLER_ARG %v is out of [0, 1]", ErrConfig, c.SampleRatio)
	case c.ExportTimeout < 0 || c.TraceBatchDelay < 0 || c.MetricInterval < 0 || c.LogBatchDelay < 0:
		return fmt.Errorf("%w: OTEL_EXPORT_TIMEOUT, OTEL_TRACES_BATCH_DELAY, OTEL_METRICS_INTERVAL and OTEL_LOGS_BATCH_DELAY must be >= 0", ErrConfig)
	}
	for k := range c.OTLPHeaders {
		if !validHeaderKey(k) {
			return fmt.Errorf("%w: OTEL_EXPORTER_OTLP_HEADERS: %q is not a valid header name", ErrConfig, k)
		}
	}
	if c.Disabled && c.OTLPEndpoint == "" {
		return nil
	}
	if _, err := signalURL(c.OTLPEndpoint, "v1/traces"); err != nil {
		return err
	}
	if c.credentials() && !c.AllowInsecureToken && plaintextRemote(c.OTLPEndpoint) {
		return fmt.Errorf("%w: OTEL_TOKEN or the Authorization header over plaintext %q to a non-loopback host: use https or set OTEL_ALLOW_INSECURE_TOKEN=true",
			ErrConfig, redact.URL(c.OTLPEndpoint))
	}
	switch {
	case c.Disabled:
		return nil
	case c.ServiceName == "":
		return fmt.Errorf("%w: OTEL_SERVICE_NAME is required (or set OTEL_SDK_DISABLED=true)", ErrConfig)
	case c.ExportTimeout == 0 || c.TraceBatchDelay == 0 || c.MetricInterval == 0 || c.LogBatchDelay == 0:
		return fmt.Errorf("%w: OTEL_EXPORT_TIMEOUT, OTEL_TRACES_BATCH_DELAY, OTEL_METRICS_INTERVAL and OTEL_LOGS_BATCH_DELAY must be > 0", ErrConfig)
	}
	return nil
}

// credentials — экспорт несёт секрет: OTEL_TOKEN или заголовок Authorization.
func (c Config) credentials() bool {
	if c.OTLPBearerToken != "" {
		return true
	}
	for k := range c.OTLPHeaders {
		if strings.EqualFold(k, "Authorization") {
			return true
		}
	}
	return false
}

// plaintextRemote — endpoint по http:// не на loopback (localhost, 127.0.0.0/8, ::1): трафик
// уходит из процесса в сеть открытым.
func plaintextRemote(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	addr, err := netip.ParseAddr(host)
	return err != nil || !addr.IsLoopback()
}

// signalURL — <endpoint>/<signalPath> по OTel-спецификации: путь базового URL
// сохраняется, per-signal путь дописывается к нему.
func signalURL(endpoint, signalPath string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("%w: OTEL_EXPORTER_OTLP_ENDPOINT %q: %w", ErrConfig, redact.URL(endpoint), redact.ParseError(err))
	}
	// `@` в пути — хвост пароля с `/`, который разбор принял за порт и путь: экспорт ушёл бы не туда.
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Contains(u.Path, "@") {
		return "", fmt.Errorf("%w: OTEL_EXPORTER_OTLP_ENDPOINT %q: want a full URL like http(s)://host:port", ErrConfig, redact.URL(endpoint))
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + signalPath
	return u.String(), nil
}

// otlpHeaders мержит OTEL_EXPORTER_OTLP_HEADERS с Bearer-токеном. Ключи
// канонизируются: иначе `authorization` из env и `Authorization` от токена
// уехали бы в запрос двумя записями map'а с недетерминированным порядком.
func otlpHeaders(cfg Config) map[string]string {
	if len(cfg.OTLPHeaders) == 0 && cfg.OTLPBearerToken == "" {
		return nil
	}
	h := make(map[string]string, len(cfg.OTLPHeaders)+1)
	for k, v := range cfg.OTLPHeaders {
		h[http.CanonicalHeaderKey(k)] = v
	}
	if cfg.OTLPBearerToken != "" {
		h["Authorization"] = "Bearer " + cfg.OTLPBearerToken
	}
	return h
}

func validHeaderKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune(`()<>@,;:\"/[]?={}`, r) {
			return false
		}
	}
	return true
}
