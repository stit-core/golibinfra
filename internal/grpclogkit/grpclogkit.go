// Package grpclogkit — мост внутреннего логгера grpc-go (grpclog) в logkit:
// сообщения транспорта (GOAWAY, keepalive, резолвер) идут структурной записью
// в общий sink, а не plain-text в stderr.
package grpclogkit

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/grpclog"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

var global = &bridge{}

// grpclog.SetLoggerV2 не потокобезопасен и должен вызываться до первого
// обращения к grpc; init — единственный момент, когда это гарантировано.
// Дальше меняется только цель моста, атомарно.
func init() { grpclog.SetLoggerV2(global) }

// fallback — логгер grpc-go по умолчанию. Пока мост не получил настоящий
// logkit.Logger, записи уходят туда же, куда ушли бы без моста (ERROR в stderr;
// severity и verbosity — из GRPC_GO_LOG_SEVERITY_LEVEL/GRPC_GO_LOG_VERBOSITY_LEVEL),
// иначе импорт кита молча съедал бы ошибки транспорта.
var fallback = sync.OnceValue(newFallback)

func newFallback() grpclog.LoggerV2 {
	infoW, warningW, errorW := fallbackWriters(os.Getenv("GRPC_GO_LOG_SEVERITY_LEVEL"))
	v, _ := strconv.Atoi(os.Getenv("GRPC_GO_LOG_VERBOSITY_LEVEL"))
	return grpclog.NewLoggerV2WithVerbosity(infoW, warningW, errorW, v)
}

// fallbackWriters повторяет раскладку писателей grpc-go: логгер пишет в поток
// своего уровня и во все более подробные, поэтому уровень задаётся тем, какому
// писателю достался stderr.
func fallbackWriters(severity string) (infoW, warningW, errorW io.Writer) {
	infoW, warningW, errorW = io.Discard, io.Discard, io.Discard
	switch severity {
	case "WARNING", "warning":
		warningW = os.Stderr
	case "INFO", "info":
		infoW = os.Stderr
	default:
		errorW = os.Stderr
	}
	return infoW, warningW, errorW
}

// Install направляет grpclog в log. Nop-логгер и nil мост не занимают: их
// ставят конструкторы, которым логгер не передали, и после них настоящий логгер
// графа всё ещё должен получить записи транспорта. Из настоящих логгеров
// выигрывает последний — grpckit.New и grpcclient.Dial зовут Install оба,
// а логгер у процесса один.
func Install(log logkit.Logger) {
	if log == nil || log == logkit.Nop() {
		return
	}
	global.set(log)
}

// New собирает отдельный адаптер grpclog.LoggerV2 поверх logkit.Logger (тесты и
// нестандартная проводка). Уровни: Info и verbose → Debug, Warning → Warn,
// Error и Fatal → Error. nil означает «логгер не задан» — записи идут дефолтным
// логгером grpc-go.
func New(log logkit.Logger) grpclog.LoggerV2 {
	b := &bridge{}
	if log != nil {
		b.set(log)
	}
	return b
}

type bridge struct{ target atomic.Pointer[logkit.Logger] }

func (b *bridge) set(log logkit.Logger) { b.target.Store(&log) }

func (b *bridge) logger() logkit.Logger {
	if p := b.target.Load(); p != nil {
		return *p
	}
	return nil
}

// grpc-go передаёт готовое сообщение, поэтому у записи одно поле: msg. Ключ
// "grpc" отделяет транспортный шум от access-log кита.
func (b *bridge) write(level slog.Level, msg string) {
	log := b.logger()
	if log == nil {
		writeFallback(level, msg)
		return
	}
	ctx := context.Background()
	switch level {
	case slog.LevelDebug:
		log.Debug(ctx, "grpc", "msg", msg)
	case slog.LevelWarn:
		log.Warn(ctx, "grpc", "msg", msg)
	default:
		log.Error(ctx, "grpc", "msg", msg)
	}
}

func writeFallback(level slog.Level, msg string) {
	lg := fallback()
	switch level {
	case slog.LevelDebug:
		lg.Info(msg)
	case slog.LevelWarn:
		lg.Warning(msg)
	default:
		lg.Error(msg)
	}
}

func (b *bridge) Info(args ...any)   { b.write(slog.LevelDebug, fmt.Sprint(args...)) }
func (b *bridge) Infoln(args ...any) { b.write(slog.LevelDebug, fmt.Sprintln(args...)) }
func (b *bridge) Infof(format string, args ...any) {
	b.write(slog.LevelDebug, fmt.Sprintf(format, args...))
}

func (b *bridge) Warning(args ...any)   { b.write(slog.LevelWarn, fmt.Sprint(args...)) }
func (b *bridge) Warningln(args ...any) { b.write(slog.LevelWarn, fmt.Sprintln(args...)) }
func (b *bridge) Warningf(format string, args ...any) {
	b.write(slog.LevelWarn, fmt.Sprintf(format, args...))
}

func (b *bridge) Error(args ...any)   { b.write(slog.LevelError, fmt.Sprint(args...)) }
func (b *bridge) Errorln(args ...any) { b.write(slog.LevelError, fmt.Sprintln(args...)) }
func (b *bridge) Errorf(format string, args ...any) {
	b.write(slog.LevelError, fmt.Sprintf(format, args...))
}

// Fatal* завершают процесс: контракт grpclog.LoggerV2 обещает вызывающему
// os.Exit(1), и grpc-go зовёт их только на неисправимой ошибке.
func (b *bridge) Fatal(args ...any) {
	b.write(slog.LevelError, fmt.Sprint(args...))
	os.Exit(1)
}

func (b *bridge) Fatalln(args ...any) {
	b.write(slog.LevelError, fmt.Sprintln(args...))
	os.Exit(1)
}

func (b *bridge) Fatalf(format string, args ...any) {
	b.write(slog.LevelError, fmt.Sprintf(format, args...))
	os.Exit(1)
}

// V сообщает grpc-go, писать ли verbose-сообщение: они уходят на Debug, значит
// решает уровень logkit, а не отдельная переменная verbosity grpc-go. Без
// установленного логгера решает verbosity дефолтного логгера grpc-go.
func (b *bridge) V(l int) bool {
	log := b.logger()
	if log == nil {
		return fallback().V(l)
	}
	return log.Enabled(context.Background(), slog.LevelDebug)
}
