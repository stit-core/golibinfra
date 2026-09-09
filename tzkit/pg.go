package tzkit

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

const microsPerMinute = int64(time.Minute / time.Microsecond)

// PgDate — Day в колонку DATE. Нулевой Day даёт NULL.
//
// pgx кодирует DATE по календарным полям Y/M/D самого time.Time, поэтому момент
// собирается в UTC: зона здесь техническая и на записанную дату не влияет.
func (d Day) PgDate() pgtype.Date {
	if d.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: time.Date(d.y, d.m, d.d, 0, 0, 0, 0, time.UTC), Valid: true}
}

// DayFromPg — Day из колонки DATE. NULL и infinity дают нулевой Day.
//
// Читаются те же календарные поля, что пишет PgDate; прогонять значение через
// .UTC()/.In() нельзя — это сдвинуло бы дату.
func DayFromPg(v pgtype.Date) Day {
	if !v.Valid || v.InfinityModifier != pgtype.Finite {
		return Day{}
	}
	return Day{y: v.Time.Year(), m: v.Time.Month(), d: v.Time.Day()}
}

// DateValue — pgtype.DateValuer: Day уходит аргументом запроса без обёрток.
func (d Day) DateValue() (pgtype.Date, error) { return d.PgDate(), nil }

// ScanDate — pgtype.DateScanner: Day сканируется из DATE напрямую. NULL даёт
// нулевой Day, ±infinity — ошибку: это не календарный день.
func (d *Day) ScanDate(v pgtype.Date) error {
	if v.Valid && v.InfinityModifier != pgtype.Finite {
		return fmt.Errorf("tzkit: DATE %v is not a calendar day", v.InfinityModifier)
	}
	*d = DayFromPg(v)
	return nil
}

// PgTime — WallTime в колонку TIME.
func (w WallTime) PgTime() pgtype.Time {
	return pgtype.Time{Microseconds: int64(w) * microsPerMinute, Valid: true}
}

// PgTimePtr — необязательный WallTime в колонку TIME; nil даёт NULL.
func PgTimePtr(w *WallTime) pgtype.Time {
	if w == nil {
		return pgtype.Time{}
	}
	return w.PgTime()
}

// WallTimeFromPg — WallTime из колонки TIME. Второй результат — «есть время
// суток»: false даёт и NULL, и значение вне суток (Postgres допускает
// TIME '24:00:00', для WallTime это не время). Секунды и микросекунды колонки
// отбрасываются: WallTime — минутная сетка.
func WallTimeFromPg(v pgtype.Time) (WallTime, bool) {
	if !v.Valid || v.Microseconds < 0 {
		return 0, false
	}
	w := WallTime(v.Microseconds / microsPerMinute)
	if !w.Valid() {
		return 0, false
	}
	return w, true
}

// TimeValue — pgtype.TimeValuer: WallTime уходит аргументом запроса без обёрток.
func (w WallTime) TimeValue() (pgtype.Time, error) {
	if !w.Valid() {
		return pgtype.Time{}, fmt.Errorf("tzkit: wall time %d is outside the day", int(w))
	}
	return w.PgTime(), nil
}

// ScanTime — pgtype.TimeScanner: WallTime сканируется из TIME напрямую. NULL и
// значение вне суток — ошибка: полночь у WallTime валидна, поэтому «пусто»
// от неё не отличить; необязательную колонку сканируйте в **WallTime.
func (w *WallTime) ScanTime(v pgtype.Time) error {
	parsed, ok := WallTimeFromPg(v)
	if !ok {
		return fmt.Errorf("tzkit: TIME %d µs (valid=%t) is not a wall time", v.Microseconds, v.Valid)
	}
	*w = parsed
	return nil
}

// PgDatePtr — необязательный Day в колонку DATE; nil даёт NULL.
func PgDatePtr(d *Day) pgtype.Date {
	if d == nil {
		return pgtype.Date{}
	}
	return d.PgDate()
}

// DayPtrFromPg — необязательный Day из колонки DATE; NULL даёт nil.
func DayPtrFromPg(v pgtype.Date) *Day {
	if !v.Valid {
		return nil
	}
	d := DayFromPg(v)
	return &d
}

// WallTimePtrFromPg — необязательный WallTime из колонки TIME; NULL и значение
// вне суток дают nil.
func WallTimePtrFromPg(v pgtype.Time) *WallTime {
	w, ok := WallTimeFromPg(v)
	if !ok {
		return nil
	}
	return &w
}
