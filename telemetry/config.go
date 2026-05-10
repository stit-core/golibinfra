package telemetry

import "time"

type Config struct {
	ServiceName    string        `envconfig:"OTEL_SERVICE_NAME" required:"true"`
	ServiceVersion string        `envconfig:"OTEL_SERVICE_VERSION" default:"dev"`
	Environment    string        `envconfig:"APP_ENV" default:"dev"`
	OTLPEndpoint   string        `envconfig:"OTEL_EXPORTER_OTLP_ENDPOINT" default:"localhost:4318"`
	OTLPInsecure   bool          `envconfig:"OTEL_EXPORTER_OTLP_INSECURE" default:"true"`
	SampleRatio    float64       `envconfig:"OTEL_TRACES_SAMPLER_ARG" default:"1.0"`
	ExportTimeout  time.Duration `envconfig:"OTEL_EXPORT_TIMEOUT" default:"10s"`

	// Tenant identifies the project against the corp shared observability
	// stack (otel.stit.tech). When set, it is exported as the
	// service.namespace resource attribute and as a free-form `tenant`
	// attribute that the corp-side otel-collector uses to route signals
	// into per-tenant Loki/Tempo/VM streams. Leave empty for local dev
	// or when shipping to an unauthenticated collector.
	Tenant string `envconfig:"OTEL_TENANT"`

	// OTLPBearerToken, when set, is sent as `Authorization: Bearer ...`
	// on every OTLP exporter request. Required for shipping to the corp
	// otel ingest endpoint, ignored locally.
	OTLPBearerToken string `envconfig:"OTEL_TOKEN"`

	StdoutOnly bool `envconfig:"OTEL_STDOUT_ONLY" default:"false"`
}
