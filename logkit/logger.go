// Package logkit — структурный logger поверх slog: JSON-записи в stdout
// (LOG_STDOUT) или в произвольный writer, опциональный второй sink в OTel
// LoggerProvider (OTLP), PII-redact на уровне атрибутов, trace_id/span_id из
// ctx, user-registered attrs (RegisterAttr) и метрика log_messages_total{level}.
package logkit

import "context"

type Logger interface {
	Debug(ctx context.Context, msg string, kv ...any)
	Info(ctx context.Context, msg string, kv ...any)
	Warn(ctx context.Context, msg string, kv ...any)
	Error(ctx context.Context, msg string, kv ...any)
	With(kv ...any) Logger
}

func Nop() Logger { return nopLogger{} }

type nopLogger struct{}

func (nopLogger) Debug(context.Context, string, ...any) {}
func (nopLogger) Info(context.Context, string, ...any)  {}
func (nopLogger) Warn(context.Context, string, ...any)  {}
func (nopLogger) Error(context.Context, string, ...any) {}
func (nopLogger) With(...any) Logger                    { return nopLogger{} }
