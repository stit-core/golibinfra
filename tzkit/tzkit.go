// Package tzkit — работа с календарным временем в зоне приложения: резолв
// IANA-зоны (Zone), типы «календарный день» (Day) и «время суток» (WallTime)
// с каноничной сериализацией, и границы суток, посчитанные в этой зоне.
//
// Канон времени, который обслуживает kit:
//
//	момент      — timestamptz в БД, RFC3339 в UTC на проводе, time.Time в Go
//	день        — DATE в БД, "2006-01-02" на проводе, tzkit.Day в Go
//	время суток — TIME в БД, "15:04" на проводе, tzkit.WallTime в Go
//
// Всё, что зависит от границ суток («сегодня», «просрочено», «за период»),
// считается через Day и Zone. Прямой now.UTC().Truncate(24*time.Hour) даёт
// сутки, начинающиеся в 00:00 UTC: для любой зоны кроме UTC это не сутки.
package tzkit

import (
	"errors"
	"fmt"
	"time"

	// tzdata вшивается в бинарь: рантайм-образы сервисов — alpine без пакета
	// tzdata, а CGO_ENABLED=0 отключает системный резолв зон. Без этого импорта
	// LoadLocation знает только UTC и валит старт на "Europe/Moscow".
	_ "time/tzdata"
)

// ErrLocalZone — зона "Local" резолвится в TZ процесса, то есть возвращает ту
// самую зависимость от окружения контейнера, ради устранения которой существует
// этот kit.
var ErrLocalZone = errors.New(`tzkit: зона "Local" запрещена — зона приложения не должна зависеть от TZ процесса`)

// ErrEmptyZone — пустое имя зоны: молчаливый фолбэк на UTC сдвинул бы границы
// суток на часы, поэтому это ошибка старта, а не дефолт.
var ErrEmptyZone = errors.New("tzkit: пустое имя зоны")

// Zone — резолвленная IANA-зона приложения. Нулевое значение ведёт себя как
// UTC, чтобы zero value был безопасен в тестах; в приложении зона приходит из
// конфига через NewZone.
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
		return Zone{}, fmt.Errorf("tzkit: зона %q: %w", name, err)
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

func (z Zone) IsZero() bool { return z.loc == nil }

func (z Zone) Name() string {
	if z.loc == nil {
		return "UTC"
	}
	return z.name
}

func (z Zone) Location() *time.Location {
	if z.loc == nil {
		return time.UTC
	}
	return z.loc
}

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
	TZ string `default:"Europe/Moscow" envconfig:"APP_TZ"`
}

// NewZone — fx-фабрика Zone из Config. Ошибка резолва валит старт приложения.
func NewZone(cfg Config) (Zone, error) { return Load(cfg.TZ) }
