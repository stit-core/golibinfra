// Package logkit — структурный логгер поверх log/slog: JSON или text в
// локальный sink, опциональный второй sink в OTel LoggerProvider, PII-redact,
// trace_id/span_id и ctx-атрибуты на верхнем уровне записи.
package logkit

import (
	"context"
	"log/slog"
)

// Logger — единственный способ логирования в сервисах: ctx первым аргументом,
// kv — пары ключ-значение в стиле slog.
type Logger interface {
	Debug(ctx context.Context, msg string, kv ...any)
	Info(ctx context.Context, msg string, kv ...any)
	Warn(ctx context.Context, msg string, kv ...any)
	Error(ctx context.Context, msg string, kv ...any)
	With(kv ...any) Logger
	// Enabled позволяет пропустить дорогую подготовку атрибутов.
	Enabled(ctx context.Context, level slog.Level) bool
}

// Nop возвращает Logger-заглушку: ничего не пишет и не паникует.
func Nop() Logger { return nopLogger{} }

type nopLogger struct{}

func (nopLogger) Debug(context.Context, string, ...any) {}
func (nopLogger) Info(context.Context, string, ...any)  {}
func (nopLogger) Warn(context.Context, string, ...any)  {}
func (nopLogger) Error(context.Context, string, ...any) {}
func (nopLogger) With(...any) Logger                    { return nopLogger{} }

// MaskText — text под дефолтными масками: правил у Nop нет, а текст ошибки в спане скрывают и они.
func (nopLogger) MaskText(text string) string { return defaultMasks().text(text) }

func (nopLogger) Enabled(context.Context, slog.Level) bool { return false }
