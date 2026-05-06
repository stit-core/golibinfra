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

	StdoutOnly bool `envconfig:"OTEL_STDOUT_ONLY" default:"false"`
}
