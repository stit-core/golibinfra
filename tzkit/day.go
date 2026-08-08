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

// ParseDay разбирает строго "2006-01-02". Формат проверяется обратным
// форматированием: time.Parse принял бы и "2026-8-8", а на проводе такого быть
// не должно.
func ParseDay(s string) (Day, error) {
	t, err := time.Parse(DayLayout, s)
	if err != nil {
		return Day{}, fmt.Errorf("tzkit: день %q: %w", s, err)
	}
	if t.Format(DayLayout) != s {
		return Day{}, fmt.Errorf("tzkit: день %q: ожидается формат %s", s, DayLayout)
	}
	return Day{y: t.Year(), m: t.Month(), d: t.Day()}, nil
}

func (d Day) IsZero() bool { return d == Day{} }

func (d Day) Date() (int, time.Month, int) { return d.y, d.m, d.d }

func (d Day) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.y, int(d.m), d.d)
}

// Start — полночь дня в зоне z. В зоне с переходом на летнее время, где 00:00
// не существует, time.Date нормализует момент вперёд — это корректная первая
// точка суток.
func (d Day) Start(z Zone) time.Time {
	return time.Date(d.y, d.m, d.d, 0, 0, 0, 0, z.Location())
}

// End — начало следующего дня в зоне z; верхняя граница полуинтервала [Start, End).
func (d Day) End(z Zone) time.Time { return d.AddDays(1).Start(z) }

// At — момент времени суток w в этом дне и зоне z.
func (d Day) At(w WallTime, z Zone) time.Time {
	return time.Date(d.y, d.m, d.d, w.Hour(), w.Minute(), 0, 0, z.Location())
}

// AddDays двигает календарную дату, а не прибавляет 24 часа: через переход на
// летнее время день остаётся тем же днём.
func (d Day) AddDays(n int) Day {
	t := time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
	return Day{y: t.Year(), m: t.Month(), d: t.Day()}
}

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

func (d Day) Before(o Day) bool { return d.Compare(o) < 0 }
func (d Day) After(o Day) bool  { return d.Compare(o) > 0 }
func (d Day) Equal(o Day) bool  { return d == o }

// Weekday не зависит от зоны: день недели — свойство календарной даты.
func (d Day) Weekday() time.Weekday {
	return time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.UTC).Weekday()
}

// DaysUntil — целое число календарных дней от d до o (отрицательное, если o раньше).
func (d Day) DaysUntil(o Day) int {
	a := time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.UTC)
	b := time.Date(o.y, o.m, o.d, 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours() / 24)
}

func (d Day) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(d.String())
}

func (d *Day) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*d = Day{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("tzkit: день: %w", err)
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

func (d Day) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

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
