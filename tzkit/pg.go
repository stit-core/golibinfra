package tzkit

import (
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

// DayFromPg — Day из колонки DATE. NULL даёт нулевой Day.
//
// Читаются те же календарные поля, что пишет PgDate; прогонять значение через
// .UTC()/.In() нельзя — это сдвинуло бы дату.
func DayFromPg(v pgtype.Date) Day {
	if !v.Valid {
		return Day{}
	}
	return Day{y: v.Time.Year(), m: v.Time.Month(), d: v.Time.Day()}
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

// WallTimeFromPg — WallTime из колонки TIME. Второй результат — признак NOT NULL.
// Секунды и микросекунды колонки отбрасываются: WallTime — минутная сетка.
func WallTimeFromPg(v pgtype.Time) (WallTime, bool) {
	if !v.Valid {
		return 0, false
	}
	return WallTime(v.Microseconds / microsPerMinute), true
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

// WallTimePtrFromPg — необязательный WallTime из колонки TIME; NULL даёт nil.
func WallTimePtrFromPg(v pgtype.Time) *WallTime {
	w, ok := WallTimeFromPg(v)
	if !ok {
		return nil
	}
	return &w
}
