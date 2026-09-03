package telemetry

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var ErrConfig = errors.New("telemetry: invalid config")

type Config struct {
	// Disabled — провайдеры без экспортёров: сигналы дропаются, сети нет,
	// propagator и глобальные провайдеры всё равно выставляются.
	Disabled bool `envconfig:"OTEL_DISABLED" default:"false"`

	// ServiceName обязателен при включённой телеметрии.
	ServiceName    string `envconfig:"OTEL_SERVICE_NAME"`
	ServiceVersion string `envconfig:"OTEL_SERVICE_VERSION" default:"dev"`
	Environment    string `envconfig:"APP_ENV" default:"dev"`

	// OTLPEndpoint — базовый URL коллектора по OTel-спецификации: схема задаёт
	// TLS, per-signal пути /v1/traces, /v1/metrics, /v1/logs добавляются к нему.
	OTLPEndpoint string  `envconfig:"OTEL_EXPORTER_OTLP_ENDPOINT" default:"http://localhost:4318"`
	SampleRatio  float64 `envconfig:"OTEL_TRACES_SAMPLER_ARG" default:"1.0"`

	// ExportTimeout — таймаут одного OTLP-запроса у всех трёх экспортёров.
	ExportTimeout time.Duration `envconfig:"OTEL_EXPORT_TIMEOUT" default:"10s"`
	// TraceBatchDelay — как часто batch-processor трейсов сбрасывает очередь.
	TraceBatchDelay time.Duration `envconfig:"OTEL_TRACES_BATCH_DELAY" default:"5s"`
	// MetricInterval — период экспорта метрик (он же min-интервал чтения runtime memstats).
	MetricInterval time.Duration `envconfig:"OTEL_METRICS_INTERVAL" default:"10s"`
	// LogBatchDelay — как часто batch-processor логов сбрасывает очередь.
	LogBatchDelay time.Duration `envconfig:"OTEL_LOGS_BATCH_DELAY" default:"1s"`

	// Tenant — resource-атрибуты service.namespace и tenant для local-dev;
	// в проде их ставит коллектор. Пусто — атрибуты не выставляются.
	Tenant string `envconfig:"OTEL_TENANT"`

	// OTLPBearerToken, если задан, — `Authorization: Bearer ...` в каждом
	// запросе экспортёров.
	OTLPBearerToken string `envconfig:"OTEL_TOKEN"`
}

func (c Config) Validate() error {
	if c.Disabled {
		return nil
	}
	switch {
	case c.ServiceName == "":
		return fmt.Errorf("%w: OTEL_SERVICE_NAME обязателен (либо OTEL_DISABLED=true)", ErrConfig)
	case c.SampleRatio < 0 || c.SampleRatio > 1:
		return fmt.Errorf("%w: OTEL_TRACES_SAMPLER_ARG %v вне [0, 1]", ErrConfig, c.SampleRatio)
	case c.ExportTimeout <= 0 || c.TraceBatchDelay <= 0 || c.MetricInterval <= 0 || c.LogBatchDelay <= 0:
		return fmt.Errorf("%w: OTEL_EXPORT_TIMEOUT, OTEL_TRACES_BATCH_DELAY, OTEL_METRICS_INTERVAL, OTEL_LOGS_BATCH_DELAY должны быть > 0", ErrConfig)
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
		return "", fmt.Errorf("%w: OTEL_EXPORTER_OTLP_ENDPOINT %q: нужен полный URL вида http(s)://host:port", ErrConfig, endpoint)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + signalPath
	return u.String(), nil
}
