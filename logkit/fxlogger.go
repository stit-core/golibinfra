package logkit

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

// FxLogger — опция fx.New: события fx пишутся через *slog.Logger графа
// (Module) — ошибки сборки и старта уровнем ERROR, рутина — DEBUG. Ставится
// на верхний уровень fx.New: внутри fx.Module она ловит только события модуля.
var FxLogger = fx.WithLogger(newFxLogger)

func newFxLogger(l *slog.Logger) fxevent.Logger {
	fl := &fxevent.SlogLogger{Logger: l}
	fl.UseLogLevel(slog.LevelDebug)
	return fl
}
