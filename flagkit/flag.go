// Package flagkit — feature flags из одной переменной окружения:
// `FEATURE_FLAGS=foo=true,bar=baz` разбирается в Store, который отдаёт
// Bool/String. Снимок на старте, без remote-источников и refresh.
package flagkit

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ErrConfig — невалидное значение FEATURE_FLAGS; детали записи оборачивают его.
var ErrConfig = errors.New("flagkit: invalid config")

// Config — конфиг кита; заполняется из окружения через NewConfig.
type Config struct {
	Definitions string `envconfig:"FEATURE_FLAGS"`
}

// Validate отвергает записи без `=` и с пустым именем флага; ошибки всех
// невалидных записей объединяются errors.Join.
func (c Config) Validate() error {
	_, err := parse(c.Definitions)
	return err
}

// Store — доступ к снятым на старте флагам по имени.
type Store interface {
	// Bool — флаг как переключатель; отсутствие флага даёт false.
	Bool(name string) bool
	// String — сырое значение флага; отсутствие флага даёт "".
	String(name string) string
	// Names — имена заданных флагов в лексикографическом порядке.
	Names() []string
}

// EnvStore — реализация Store поверх разобранного Config.Definitions.
// После конструирования не меняется, поэтому безопасен для конкурентного чтения.
type EnvStore struct {
	flags map[string]string
}

// NewEnvStore разбирает Config.Definitions; невалидная запись — ошибка
// (errors.Is(err, ErrConfig)), а не молча пропущенный флаг.
func NewEnvStore(cfg Config) (*EnvStore, error) {
	flags, err := parse(cfg.Definitions)
	if err != nil {
		return nil, err
	}
	return &EnvStore{flags: flags}, nil
}

func parse(definitions string) (map[string]string, error) {
	flags := make(map[string]string)
	var errs []error
	for entry := range strings.SplitSeq(definitions, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, value, found := strings.Cut(entry, "=")
		if !found {
			errs = append(errs, fmt.Errorf("%w: FEATURE_FLAGS entry %q: want name=value", ErrConfig, entry))
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			errs = append(errs, fmt.Errorf("%w: FEATURE_FLAGS entry %q: empty flag name", ErrConfig, entry))
			continue
		}
		flags[name] = strings.TrimSpace(value)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return flags, nil
}

// Bool возвращает true, если флаг задан значением из списка strconv.ParseBool
// (1, t, T, TRUE, true, True); отсутствие флага и любое другое значение — false.
func (s *EnvStore) Bool(name string) bool {
	v, err := strconv.ParseBool(s.flags[name])
	return err == nil && v
}

// String возвращает сырое значение флага либо "" при его отсутствии.
func (s *EnvStore) String(name string) string {
	return s.flags[name]
}

// Names возвращает имена заданных флагов в лексикографическом порядке —
// для лога активной конфигурации на старте.
func (s *EnvStore) Names() []string {
	return slices.Sorted(maps.Keys(s.flags))
}
