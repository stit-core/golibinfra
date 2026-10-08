// Package logtest — боевой логгер logkit, который пишет записи в память вместо stdout: тест
// проверяет уровень, сообщение и поля лога такими, какими их получит оператор.
package logtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Entry — одна запись лога: JSON-строка боевого логгера, разобранная обратно. KV — её поля, кроме
// time, level и msg: атрибуты вызова и With, ctx-атрибуты logkit (trace_id, request_id) — после
// масок. Типы — JSON: строка, json.Number, bool, nil, группа и структура — map[string]any,
// срез — []any.
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

// Recorder — logkit.Logger с цепочкой logkit.New (разбор kv, маски, ctx-атрибуты), записи
// которого копятся в памяти. Безопасен для параллельных вызовов; With отдаёт логгер поверх того
// же хранилища.
type Recorder struct {
	log   logkit.Logger
	store *store
}

// Capture возвращает Recorder, принимающий записи всех уровней.
func Capture() *Recorder { return CaptureAt(slog.LevelDebug) }

// CaptureAt возвращает Recorder, который принимает записи от уровня min и
// отвечает Enabled(min-1) = false — для тестов кита, пропускающего дорогую
// подготовку атрибутов по Enabled.
func CaptureAt(min slog.Level) *Recorder {
	s := &store{}
	log, err := logkit.New(logkit.Config{Level: min.String()}, logkit.WithWriter(s))
	if err != nil {
		panic("logtest: " + err.Error())
	}
	return &Recorder{log: log, store: s}
}

// Debug записывает запись уровня Debug.
func (r *Recorder) Debug(ctx context.Context, msg string, kv ...any) { r.log.Debug(ctx, msg, kv...) }

// Info записывает запись уровня Info.
func (r *Recorder) Info(ctx context.Context, msg string, kv ...any) { r.log.Info(ctx, msg, kv...) }

// Warn записывает запись уровня Warn.
func (r *Recorder) Warn(ctx context.Context, msg string, kv ...any) { r.log.Warn(ctx, msg, kv...) }

// Error записывает запись уровня Error.
func (r *Recorder) Error(ctx context.Context, msg string, kv ...any) { r.log.Error(ctx, msg, kv...) }

// With возвращает логгер с постоянными kv-парами поверх того же хранилища:
// записи дочернего логгера видны в Entries родителя.
func (r *Recorder) With(kv ...any) logkit.Logger { return r.log.With(kv...) }

// Enabled сообщает, принимает ли Recorder записи уровня level.
func (r *Recorder) Enabled(ctx context.Context, level slog.Level) bool {
	return r.log.Enabled(ctx, level)
}

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

// store — локальный sink логгера: slog.JSONHandler отдаёт запись одним вызовом Write.
type store struct {
	mu      sync.Mutex
	entries []Entry
}

func (s *store) Write(line []byte) (int, error) {
	e, err := parse(line)
	if err != nil {
		panic(fmt.Sprintf("logtest: logkit wrote a record that is not a JSON object: %v: %q", err, line))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return len(line), nil
}

// parse разбирает строку slog.JSONHandler: первые time, level и msg — поля самой записи,
// одноимённый атрибут вызова идёт в KV.
func parse(line []byte) (Entry, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	e := Entry{KV: map[string]any{}}
	if _, err := dec.Token(); err != nil {
		return e, err
	}
	own := map[string]bool{slog.TimeKey: true, slog.LevelKey: true, slog.MessageKey: true}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return e, err
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			return e, err
		}
		name, _ := key.(string)
		if !own[name] {
			e.KV[name] = v
			continue
		}
		delete(own, name)
		text, _ := v.(string)
		switch name {
		case slog.LevelKey:
			if err := e.Level.UnmarshalText([]byte(text)); err != nil {
				return e, err
			}
		case slog.MessageKey:
			e.Msg = text
		}
	}
	return e, nil
}

// Recorder реализует logkit.Logger целиком, включая Enabled.
var _ logkit.Logger = (*Recorder)(nil)
