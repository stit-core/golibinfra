// Package logtest — реализация logkit.Logger, которая копит записи в памяти:
// тест кита проверяет уровень, сообщение и поля лога, не разбирая stdout.
package logtest

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Entry — одна перехваченная запись лога. KV содержит kv-пары вызова плюс
// накопленные через With; ctx-атрибуты logkit (trace_id, request_id) сюда не
// попадают — их добавляет handler боевого логгера, а не Logger.
type Entry struct {
	Level slog.Level
	Msg   string
	KV    map[string]any
}

// Value возвращает значение поля key и признак его наличия.
func (e Entry) Value(key string) (any, bool) {
	v, ok := e.KV[key]
	return v, ok
}

// Str возвращает значение поля key в строковом виде ("" — поля нет).
func (e Entry) Str(key string) string {
	v, ok := e.KV[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// Recorder — logkit.Logger, пишущий записи в память. Безопасен для
// параллельных вызовов; With отдаёт логгер поверх того же хранилища.
type Recorder struct {
	store *store
	attrs []any
	min   slog.Level
}

type store struct {
	mu      sync.Mutex
	entries []Entry
}

// Capture возвращает Recorder, принимающий записи всех уровней.
func Capture() *Recorder { return CaptureAt(slog.LevelDebug) }

// CaptureAt возвращает Recorder, который принимает записи от уровня min и
// отвечает Enabled(min-1) = false — для тестов кита, пропускающего дорогую
// подготовку атрибутов по Enabled.
func CaptureAt(min slog.Level) *Recorder {
	return &Recorder{store: &store{}, min: min}
}

// Debug записывает запись уровня Debug.
func (r *Recorder) Debug(_ context.Context, msg string, kv ...any) { r.add(slog.LevelDebug, msg, kv) }

// Info записывает запись уровня Info.
func (r *Recorder) Info(_ context.Context, msg string, kv ...any) { r.add(slog.LevelInfo, msg, kv) }

// Warn записывает запись уровня Warn.
func (r *Recorder) Warn(_ context.Context, msg string, kv ...any) { r.add(slog.LevelWarn, msg, kv) }

// Error записывает запись уровня Error.
func (r *Recorder) Error(_ context.Context, msg string, kv ...any) { r.add(slog.LevelError, msg, kv) }

// With возвращает логгер с постоянными kv-парами поверх того же хранилища:
// записи дочернего логгера видны в Entries родителя.
func (r *Recorder) With(kv ...any) logkit.Logger {
	attrs := make([]any, 0, len(r.attrs)+len(kv))
	attrs = append(attrs, r.attrs...)
	attrs = append(attrs, kv...)
	return &Recorder{store: r.store, attrs: attrs, min: r.min}
}

// Enabled сообщает, принимает ли Recorder записи уровня level.
func (r *Recorder) Enabled(_ context.Context, level slog.Level) bool { return level >= r.min }

// Entries возвращает копию перехваченных записей в порядке появления.
func (r *Recorder) Entries() []Entry {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	return append([]Entry(nil), r.store.entries...)
}

// Find возвращает последнюю запись с таким уровнем и сообщением.
func (r *Recorder) Find(level slog.Level, msg string) (Entry, bool) {
	return r.FindFunc(func(e Entry) bool { return e.Level == level && e.Msg == msg })
}

// FindFunc возвращает последнюю запись, удовлетворяющую match.
func (r *Recorder) FindFunc(match func(Entry) bool) (Entry, bool) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	for _, v := range slices.Backward(r.store.entries) {
		if match(v) {
			return v, true
		}
	}
	return Entry{}, false
}

// Has сообщает, есть ли запись с таким уровнем и сообщением.
func (r *Recorder) Has(level slog.Level, msg string) bool {
	_, ok := r.Find(level, msg)
	return ok
}

// Reset очищает хранилище (общее с логгерами из With).
func (r *Recorder) Reset() {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	r.store.entries = nil
}

func (r *Recorder) add(level slog.Level, msg string, kv []any) {
	if level < r.min {
		return
	}
	e := Entry{Level: level, Msg: msg, KV: map[string]any{}}
	putKV(e.KV, r.attrs)
	putKV(e.KV, kv)

	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	r.store.entries = append(r.store.entries, e)
}

// putKV раскладывает slog-стиль kv-пар в map; нечётный хвост и не-строковый
// ключ отбрасываются так же, как их отбросил бы боевой handler.
func putKV(dst map[string]any, kv []any) {
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			dst[k] = kv[i+1]
		}
	}
}

// Recorder реализует logkit.Logger целиком, включая Enabled.
var _ logkit.Logger = (*Recorder)(nil)
