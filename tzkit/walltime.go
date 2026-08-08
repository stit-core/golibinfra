package tzkit

import (
	"encoding/json"
	"fmt"
	"time"
)

// WallLayout — единственный формат времени суток на проводе.
const WallLayout = "15:04"

const minutesPerDay = 24 * 60

// WallTime — время суток («17:00») как минуты от полуночи. Не момент: момент
// получается только вместе с днём и зоной (Day.At).
//
// Нулевое значение — полночь, а не «не задано»; для необязательного поля
// используйте *WallTime.
type WallTime int

// NewWallTime строит время суток из часов и минут.
func NewWallTime(hour, minute int) (WallTime, error) {
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("tzkit: время суток %02d:%02d вне диапазона", hour, minute)
	}
	return WallTime(hour*60 + minute), nil
}

// ParseWallTime разбирает строго "15:04" — ровно 5 символов, ведущие нули
// обязательны.
func ParseWallTime(s string) (WallTime, error) {
	if len(s) != len(WallLayout) {
		return 0, fmt.Errorf("tzkit: время суток %q: ожидается формат %s", s, WallLayout)
	}
	t, err := time.Parse(WallLayout, s)
	if err != nil {
		return 0, fmt.Errorf("tzkit: время суток %q: %w", s, err)
	}
	return WallTime(t.Hour()*60 + t.Minute()), nil
}

func (w WallTime) Hour() int   { return int(w) / 60 }
func (w WallTime) Minute() int { return int(w) % 60 }

// Duration — смещение от полуночи.
func (w WallTime) Duration() time.Duration { return time.Duration(w) * time.Minute }

func (w WallTime) String() string { return fmt.Sprintf("%02d:%02d", w.Hour(), w.Minute()) }

// Valid — значение в пределах суток.
func (w WallTime) Valid() bool { return w >= 0 && w < minutesPerDay }

func (w WallTime) MarshalJSON() ([]byte, error) {
	if !w.Valid() {
		return nil, fmt.Errorf("tzkit: время суток %d вне суток", int(w))
	}
	return json.Marshal(w.String())
}

func (w *WallTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("tzkit: время суток: %w", err)
	}
	parsed, err := ParseWallTime(s)
	if err != nil {
		return err
	}
	*w = parsed
	return nil
}

func (w WallTime) MarshalText() ([]byte, error) { return []byte(w.String()), nil }

func (w *WallTime) UnmarshalText(b []byte) error {
	parsed, err := ParseWallTime(string(b))
	if err != nil {
		return err
	}
	*w = parsed
	return nil
}
