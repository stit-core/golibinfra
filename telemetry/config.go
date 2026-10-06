package telemetry

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrConfig — sentinel невалидной конфигурации телеметрии.
var ErrConfig = errors.New("telemetry: invalid config")

// defaultExportTimeout — бюджет одного шага Shutdown, когда Providers собраны
// вручную и ExportTimeout неизвестен.
const defaultExportTimeout = 10 * time.Second

// Headers — заголовки OTLP-запросов из OTEL_EXPORTER_OTLP_HEADERS.
type Headers map[string]string

// Decode разбирает OTEL_EXPORTER_OTLP_HEADERS в формате спецификации OTel: `k1=v1,k2=v2`,
// значения в percent-encoding.
func (h *Headers) Decode(value string) error {
	out := Headers{}
	for pair := range strings.SplitSeq(value, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		k, v, found := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !found || k == "" {
			return fmt.Errorf("%q: want key=value pairs separated by comma", pair)
		}
		decoded, err := url.PathUnescape(v)
		if err != nil {
			return fmt.Errorf("%q: %w", pair, err)
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
}

// Validate проверяет конфиг на старте: невалидные значения должны валить
// процесс, а не первый экспорт.
func (c Config) Validate() error {
	if c.Disabled {
		return nil
	}
	switch {
	case c.ServiceName == "":
		return fmt.Errorf("%w: OTEL_SERVICE_NAME is required (or set OTEL_SDK_DISABLED=true)", ErrConfig)
	case c.SampleRatio < 0 || c.SampleRatio > 1:
		return fmt.Errorf("%w: OTEL_TRACES_SAMPLER_ARG %v is out of [0, 1]", ErrConfig, c.SampleRatio)
	case c.ExportTimeout <= 0 || c.TraceBatchDelay <= 0 || c.MetricInterval <= 0 || c.LogBatchDelay <= 0:
		return fmt.Errorf("%w: OTEL_EXPORT_TIMEOUT, OTEL_TRACES_BATCH_DELAY, OTEL_METRICS_INTERVAL and OTEL_LOGS_BATCH_DELAY must be > 0", ErrConfig)
	}
	for k := range c.OTLPHeaders {
		if !validHeaderKey(k) {
			return fmt.Errorf("%w: OTEL_EXPORTER_OTLP_HEADERS: %q is not a valid header name", ErrConfig, k)
		}
	}
	_, err := signalURL(c.OTLPEndpoint, "v1/traces")
	return err
}

// signalURL — <endpoint>/<signalPath> по OTel-спецификации: путь базового URL
// сохраняется, per-signal путь дописывается к нему.
func signalURL(endpoint, signalPath string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("%w: OTEL_EXPORTER_OTLP_ENDPOINT %q: %w", ErrConfig, endpoint, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%w: OTEL_EXPORTER_OTLP_ENDPOINT %q: want a full URL like http(s)://host:port", ErrConfig, endpoint)
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
