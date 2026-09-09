package logkit

import (
	"log/slog"

	"go.opentelemetry.io/otel/log"
	"go.uber.org/fx"
)

// Module не provide'ит конфиги — caller регистрирует их сам (fx.Provide(
// logkit.NewConfig) и опц. NewRedactConfig). OTel-sink включается, если в
// графе есть log.LoggerProvider (его отдаёт telemetry.Module).
var Module = fx.Module(
	"logkit",
	fx.Provide(
		newSlogLogger,
		FromSlog,
	),
)

type params struct {
	fx.In
	Config         Config
	Redact         RedactConfig       `optional:"true"`
	LoggerProvider log.LoggerProvider `optional:"true"`
}

func newSlogLogger(p params) (*slog.Logger, error) {
	return NewSlog(p.Config, WithRedact(p.Redact), WithOTel(p.LoggerProvider))
}
