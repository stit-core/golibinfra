// Package logkit — структурный logger поверх slog: каждый record пишется
// JSON'ом в stdout (при LOG_STDOUT, default; иначе отбрасывается) или в
// произвольный writer через NewWithWriter (file-sink); автоматически
// получает trace_id/span_id из ctx. Поддерживает PII-redact'инг
// (regex-фильтры через RedactConfig, LOG_REDACT_ENABLED) и user-registered
// attrs (RegisterAttr) для проброса полей вроде request_id из httpkit.
// Эмитит log_messages_total{level}.
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
