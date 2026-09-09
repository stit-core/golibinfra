package logkit

import (
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

var logMessagesCounter = otelkit.Must(otelkit.Meter("logkit").Int64Counter(
	"log_messages_total",
	metric.WithDescription("Emitted log records by level"),
))

// Наборы атрибутов собраны один раз: инкремент на горячем пути не аллоцирует.
var levelOpts = [4]metric.MeasurementOption{
	metric.WithAttributeSet(attribute.NewSet(attribute.String("level", "debug"))),
	metric.WithAttributeSet(attribute.NewSet(attribute.String("level", "info"))),
	metric.WithAttributeSet(attribute.NewSet(attribute.String("level", "warn"))),
	metric.WithAttributeSet(attribute.NewSet(attribute.String("level", "error"))),
}

// levelMeasurement держит множество label'ов конечным и на кастомных уровнях.
func levelMeasurement(l slog.Level) metric.MeasurementOption {
	switch {
	case l < slog.LevelInfo:
		return levelOpts[0]
	case l < slog.LevelWarn:
		return levelOpts[1]
	case l < slog.LevelError:
		return levelOpts[2]
	default:
		return levelOpts[3]
	}
}
