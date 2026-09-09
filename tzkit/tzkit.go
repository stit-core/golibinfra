// Package tzkit — календарное время в зоне приложения: резолв IANA-зоны (Zone),
// типы «календарный день» (Day) и «время суток» (WallTime) с каноничной
// сериализацией и границы суток, посчитанные в этой зоне (канон — README.md).
package tzkit

import (
	"errors"
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"

	// tzdata вшивается в бинарь: рантайм-образы сервисов — alpine без пакета
	// tzdata, а CGO_ENABLED=0 отключает системный резолв зон. Без этого импорта
	// LoadLocation знает только UTC и валит старт на "Europe/Moscow".
	_ "time/tzdata"
)

// ErrLocalZone — зона "Local" резолвится в TZ процесса, то есть возвращает ту
// самую зависимость от окружения контейнера, ради устранения которой существует
// этот kit.
var ErrLocalZone = errors.New(`tzkit: zone "Local" is not allowed: the application zone must not depend on the process TZ`)

// ErrEmptyZone — пустое имя зоны: молчаливый фолбэк на UTC сдвинул бы границы
// суток на часы, поэтому это ошибка старта, а не дефолт.
var ErrEmptyZone = errors.New("tzkit: empty zone name")

// ErrConfig — невалидный Config; возвращается из NewConfig и Validate.
var ErrConfig = errors.New("tzkit: invalid config")

// Zone — резолвленная IANA-зона приложения. Нулевое значение считает в UTC:
// детерминированная зона, не зависящая от окружения (в отличие от "Local"),
// поэтому zero value безопасен в тестах; в приложении зона приходит из конфига
// через NewZone.
type Zone struct {
	name string
	loc  *time.Location
}

// UTC — зона UTC явным значением.
func UTC() Zone { return Zone{name: "UTC", loc: time.UTC} }

// Load резолвит IANA-имя зоны ("Europe/Moscow"). Неизвестная зона — ошибка,
// которую вызывающий обязан превратить в отказ старта.
func Load(name string) (Zone, error) {
	switch name {
	case "":
		return Zone{}, ErrEmptyZone
	case "Local":
		return Zone{}, ErrLocalZone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return Zone{}, fmt.Errorf("tzkit: zone %q: %w", name, err)
	}
	return Zone{name: name, loc: loc}, nil
}

// MustLoad — Load с паникой; для тестов и package-level переменных.
func MustLoad(name string) Zone {
	z, err := Load(name)
	if err != nil {
		panic(err)
	}
	return z
}

// IsZero — зона не резолвлена; считает в UTC.
func (z Zone) IsZero() bool { return z.loc == nil }

// Name — IANA-имя зоны; нулевая зона — "UTC".
func (z Zone) Name() string {
	if z.loc == nil {
		return "UTC"
	}
	return z.name
}

// Location — *time.Location зоны; нулевая зона — time.UTC.
func (z Zone) Location() *time.Location {
	if z.loc == nil {
		return time.UTC
	}
	return z.loc
}

// String — IANA-имя зоны.
func (z Zone) String() string { return z.Name() }

// In переводит момент в зону — представление меняется, сам момент нет.
func (z Zone) In(t time.Time) time.Time { return t.In(z.Location()) }

// OffsetAt — смещение зоны от UTC в конкретный момент (в зоне с переходом на
// летнее время смещение зависит от даты).
func (z Zone) OffsetAt(t time.Time) time.Duration {
	_, off := t.In(z.Location()).Zone()
	return time.Duration(off) * time.Second
}

// Config — зона приложения одна на всю организацию. Kit её не provide'ит:
// Config регистрирует caller.
type Config struct {
	// TZ — IANA-имя зоны организации; обязательно, дефолта нет.
	TZ string `envconfig:"APP_TZ"`
}

// NewConfig читает Config из окружения и валидирует его.
func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// Validate требует заданной и резолвящейся зоны: дефолта у APP_TZ нет, потому
// что любая подставленная зона — это чужие границы суток без единого сигнала.
func (c Config) Validate() error {
	if _, err := Load(c.TZ); err != nil {
		return fmt.Errorf("%w: APP_TZ: %w", ErrConfig, err)
	}
	return nil
}

// NewZone — fx-фабрика Zone из Config. Ошибка резолва валит старт приложения.
func NewZone(cfg Config) (Zone, error) { return Load(cfg.TZ) }
