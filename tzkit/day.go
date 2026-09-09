package tzkit

import (
	"encoding/json"
	"fmt"
	"time"
)

// DayLayout — единственный формат календарного дня на проводе.
const DayLayout = "2006-01-02"

// Day — календарный день без времени и без зоны: «8 августа 2026», а не момент.
// Момент из него получается только вместе с зоной (Start/End/At).
//
// Нулевое значение — «день не задан»: String() отдаёт "", MarshalJSON — null.
type Day struct {
	y int
	m time.Month
	d int
}

// NewDay нормализует переполнение так же, как time.Date: NewDay(2026, 4, 31)
// даёт 1 мая.
func NewDay(y int, m time.Month, d int) Day {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return Day{y: t.Year(), m: t.Month(), d: t.Day()}
}

// DayOf — день, на который приходится момент в зоне z. Это единственный
// правильный способ спросить «какое сегодня число».
func DayOf(t time.Time, z Zone) Day {
	l := t.In(z.Location())
	return Day{y: l.Year(), m: l.Month(), d: l.Day()}
}

// ParseDay разбирает строго "2006-01-02": ведущие нули обязательны, момент с
// офсетом в поле дня отвергается.
func ParseDay(s string) (Day, error) {
	t, err := time.Parse(DayLayout, s)
	if err != nil {
		return Day{}, fmt.Errorf("tzkit: day %q: %w", s, err)
	}
	return Day{y: t.Year(), m: t.Month(), d: t.Day()}, nil
}

// MustParseDay — ParseDay с паникой; для тестов и package-level переменных.
func MustParseDay(s string) Day {
	d, err := ParseDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

// IsZero — день не задан.
func (d Day) IsZero() bool { return d == Day{} }

// Date — год, месяц и число календарной даты.
func (d Day) Date() (int, time.Month, int) { return d.y, d.m, d.d }

// String — день в формате DayLayout; нулевой день — пустая строка.
func (d Day) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.y, int(d.m), d.d)
}

// Start — первый момент суток d в зоне z. В зоне, где полночи не существует
// (стрелки переведены вперёд через 00:00), это момент перехода; там, где
// полночь случается дважды, — первое вхождение. У календарного дня, которого в
// зоне не было вовсе (Pacific/Apia 2011-12-30), Start равен End — суток нет.
func (d Day) Start(z Zone) time.Time {
	start, _ := resolveCivil(d, 0, z.Location(), gapToTransition)
	return start
}

// End — начало следующего дня в зоне z; верхняя граница полуинтервала [Start, End).
func (d Day) End(z Zone) time.Time { return d.AddDays(1).Start(z) }

// At — момент времени суток w в этом дне и зоне z; результат всегда лежит в
// [Start(z), End(z)). Времени суток, которого в зоне нет, соответствует момент,
// сдвинутый вперёд на длину провала, а если провал упирается в полночь и сдвиг
// уходит за конец суток — последний момент суток; времени, которое случается
// дважды, — первое вхождение. У суток, которых в зоне не было (Start == End),
// любое время суток даёт этот момент. Паникует на w вне суток: такое значение
// получается только приведением int, все конструкторы WallTime его отвергают.
func (d Day) At(w WallTime, z Zone) time.Time {
	if !w.Valid() {
		panic(fmt.Sprintf("tzkit: Day.At: wall time %d is outside the day", int(w)))
	}
	at, gap := resolveCivil(d, w.Duration(), z.Location(), gapShiftForward)
	if !gap {
		return at
	}
	// Сдвиг вперёд из провала, доходящего до полуночи (America/Nuuk,
	// America/Scoresbysund), иначе уносит «23:30» в следующую дату.
	if end := d.End(z); !at.Before(end) {
		at = end.Add(-time.Nanosecond)
	}
	if start := d.Start(z); at.Before(start) {
		at = start
	}
	return at
}

// AddDays двигает календарную дату, а не прибавляет 24 часа: через переход на
// летнее время день остаётся тем же днём.
func (d Day) AddDays(n int) Day {
	t := time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
	return Day{y: t.Year(), m: t.Month(), d: t.Day()}
}

// Compare — -1, 0 или 1 в порядке календаря.
func (d Day) Compare(o Day) int {
	switch {
	case d.y != o.y:
		return cmpInt(d.y, o.y)
	case d.m != o.m:
		return cmpInt(int(d.m), int(o.m))
	default:
		return cmpInt(d.d, o.d)
	}
}

// Before — d раньше o.
func (d Day) Before(o Day) bool { return d.Compare(o) < 0 }

// After — d позже o.
func (d Day) After(o Day) bool { return d.Compare(o) > 0 }

// Equal — тот же календарный день.
func (d Day) Equal(o Day) bool { return d == o }

// Weekday не зависит от зоны: день недели — свойство календарной даты.
func (d Day) Weekday() time.Weekday {
	return time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.UTC).Weekday()
}

// DaysUntil — целое число календарных дней от d до o (отрицательное, если o
// раньше). Считается в секундах: time.Duration насыщается на разнице больше
// 292 лет, а DATE в Postgres допускает такие даты.
func (d Day) DaysUntil(o Day) int {
	const secondsPerDay = 24 * 60 * 60
	a := time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.UTC).Unix()
	b := time.Date(o.y, o.m, o.d, 0, 0, 0, 0, time.UTC).Unix()
	return int((b - a) / secondsPerDay)
}

// MarshalJSON — строка DayLayout; нулевой день — null.
func (d Day) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(d.String())
}

// UnmarshalJSON принимает строку DayLayout; null и "" дают нулевой день.
func (d *Day) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*d = Day{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("tzkit: day: %w", err)
	}
	if s == "" {
		*d = Day{}
		return nil
	}
	parsed, err := ParseDay(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// MarshalText — строка DayLayout; нулевой день — пустая строка. Работает там,
// где день становится ключом map или query-параметром.
func (d Day) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText принимает строку DayLayout; пустая строка даёт нулевой день.
func (d *Day) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*d = Day{}
		return nil
	}
	parsed, err := ParseDay(string(b))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// StartOfDay — начало суток, в которые попадает момент t, в зоне z.
func StartOfDay(t time.Time, z Zone) time.Time { return DayOf(t, z).Start(z) }

// Span — полуинтервал моментов [start, end) для диапазона дней from..to,
// оба конца включительно по дате, в зоне z. Это каноничный способ превратить
// период из UI («с 1 по 31 августа») в границы SQL-выборки.
func Span(from, to Day, z Zone) (start, end time.Time) {
	return from.Start(z), to.End(z)
}

// gapPolicy — что вернуть, если гражданского времени в зоне не существует.
type gapPolicy int

const (
	// gapToTransition — момент перехода: первое, что в зоне вообще существует после провала.
	gapToTransition gapPolicy = iota
	// gapShiftForward — эквивалентный момент, сдвинутый вперёд на длину провала.
	gapShiftForward
)

// civilSearchWindow с запасом покрывает любое смещение зоны от UTC (tzdata —
// от −12 до +14 часов), поэтому обход периодов начинается заведомо раньше
// искомого момента.
const civilSearchWindow = 48 * time.Hour

// maxCivilPeriods — предохранитель обхода: в окне вокруг суток tzdata держит
// единицы периодов постоянного смещения, и обход обязан закончиться внутри них.
const maxCivilPeriods = 64

// resolveCivil — первый момент, чьё гражданское время в зоне loc равно дню d
// плюс into от полуночи, и признак «такого времени суток в зоне не было»
// (стрелки перевели через него, момент выбран по onGap). Периоды постоянного
// смещения обходятся по порядку, и внутри периода момент считается точно,
// поэтому размер перевода стрелок значения не имеет: переход на 30 минут
// обрабатывается так же, как на два часа.
func resolveCivil(d Day, into time.Duration, loc *time.Location, onGap gapPolicy) (time.Time, bool) {
	naive := time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.UTC).Add(into)
	t := naive.Add(-civilSearchWindow).In(loc)
	for range maxCivilPeriods {
		_, offset := t.Zone()
		at := naive.Add(-time.Duration(offset) * time.Second).In(loc)
		// Кандидат принимается по смещению в нём самом, а не по границам
		// периода: за последним переходом tzdata (дальше зона живёт по
		// строке-правилу) ZoneBounds отдаёт интервал, не содержащий t.
		if _, actual := at.Zone(); actual == offset {
			return at, false
		}
		start, end := t.ZoneBounds()
		if !start.IsZero() && at.Before(start) {
			if onGap == gapShiftForward {
				_, before := start.Add(-time.Nanosecond).Zone()
				return naive.Add(-time.Duration(before) * time.Second).In(loc), true
			}
			return start, true
		}
		if !end.After(t) {
			break
		}
		t = end
	}
	panic(fmt.Sprintf("tzkit: zone %s: no offset period matches %s %v", loc, d, into))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
