package logkit

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

// FxLogger — опция fx.New: события fx пишутся логгером из Config и RedactConfig графа (маски — как
// у Module) в локальный sink, без OTel — ошибки сборки и старта уровнем ERROR, рутина — DEBUG.
// Отказ провайдера, который тянет граф logkit (telemetry), доходит до лога: логгер событий от него
// не зависит. При Config.Silent события ERROR идут в stderr: причина отказа старта не теряется ни
// при каком конфиге логов. Ставится на верхний уровень fx.New: внутри fx.Module она ловит только
// события модуля.
var FxLogger = fx.WithLogger(newFxLogger)

type fxLoggerParams struct {
	fx.In
	Config Config
	Redact RedactConfig `optional:"true"`
}

func newFxLogger(p fxLoggerParams) (fxevent.Logger, error) {
	l, err := NewSlog(p.Config, WithRedact(p.Redact), WithStderrFallback())
	if err != nil {
		return nil, err
	}
	fl := &fxevent.SlogLogger{Logger: l}
	fl.UseLogLevel(slog.LevelDebug)
	return fl, nil
}
