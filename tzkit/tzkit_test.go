package tzkit_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.stit.tech/stit-core/golibinfra/tzkit"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	z, err := tzkit.Load("Europe/Moscow")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if z.Name() != "Europe/Moscow" || z.IsZero() {
		t.Fatalf("zone = %v, IsZero=%v", z, z.IsZero())
	}

	if _, err := tzkit.Load(""); !errors.Is(err, tzkit.ErrEmptyZone) {
		t.Errorf("Load(\"\") err = %v, want ErrEmptyZone", err)
	}
	if _, err := tzkit.Load("Local"); !errors.Is(err, tzkit.ErrLocalZone) {
		t.Errorf("Load(Local) err = %v, want ErrLocalZone", err)
	}
	if _, err := tzkit.Load("Europe/Atlantis"); err == nil {
		t.Error("Load(unknown) must fail")
	}
}

func TestZoneZeroValueIsUTC(t *testing.T) {
	t.Parallel()

	var z tzkit.Zone
	if !z.IsZero() || z.Name() != "UTC" || z.Location() != time.UTC {
		t.Fatalf("zero Zone = %v/%v/%v", z.IsZero(), z.Name(), z.Location())
	}
}

func TestZoneOffsetAt(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	if got := msk.OffsetAt(time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)); got != 3*time.Hour {
		t.Errorf("MSK offset = %v, want 3h", got)
	}

	// Зона с переходом на летнее время: смещение зависит от даты.
	berlin := tzkit.MustLoad("Europe/Berlin")
	if got := berlin.OffsetAt(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)); got != time.Hour {
		t.Errorf("Berlin winter offset = %v, want 1h", got)
	}
	if got := berlin.OffsetAt(time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)); got != 2*time.Hour {
		t.Errorf("Berlin summer offset = %v, want 2h", got)
	}
}

// Ядро проблемы, ради которой существует kit: у момента 22:00 UTC московский
// день — уже следующий. Расчёт «сегодня» в UTC ошибается ровно здесь.
func TestDayOfCrossesUTCBoundary(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	at := time.Date(2026, 8, 7, 22, 30, 0, 0, time.UTC)

	if got := tzkit.DayOf(at, msk).String(); got != "2026-08-08" {
		t.Errorf("DayOf(MSK) = %s, want 2026-08-08", got)
	}
	if got := tzkit.DayOf(at, tzkit.UTC()).String(); got != "2026-08-07" {
		t.Errorf("DayOf(UTC) = %s, want 2026-08-07", got)
	}
}

func TestDayStartEndInZone(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	day := tzkit.NewDay(2026, time.August, 8)

	wantEqual(t, "Start", day.Start(msk).UTC(), time.Date(2026, 8, 7, 21, 0, 0, 0, time.UTC))
	wantEqual(t, "End", day.End(msk).UTC(), time.Date(2026, 8, 8, 21, 0, 0, 0, time.UTC))
	wantEqual(t, "End == next Start", day.End(msk), day.AddDays(1).Start(msk))
}

func TestSpanCoversWholeMonthInZone(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	start, end := tzkit.Span(tzkit.NewDay(2026, time.August, 1), tzkit.NewDay(2026, time.August, 31), msk)

	wantEqual(t, "start", start.UTC(), time.Date(2026, 7, 31, 21, 0, 0, 0, time.UTC))
	wantEqual(t, "end", end.UTC(), time.Date(2026, 8, 31, 21, 0, 0, 0, time.UTC))
}

func TestStartOfDay(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	at := time.Date(2026, 8, 8, 0, 30, 0, 0, time.UTC) // 03:30 MSK

	wantEqual(t, "StartOfDay", tzkit.StartOfDay(at, msk).UTC(), time.Date(2026, 8, 7, 21, 0, 0, 0, time.UTC))
}

func TestParseDayIsStrict(t *testing.T) {
	t.Parallel()

	d, err := tzkit.ParseDay("2026-08-08")
	if err != nil || d != tzkit.NewDay(2026, time.August, 8) {
		t.Fatalf("ParseDay = %v, %v", d, err)
	}

	for _, bad := range []string{
		"2026-8-8", "2026-08-8", "08-08-2026", "2026-08-08T00:00:00Z", "", "2026-13-01",
		"2026-02-30", "12026-08-08", " 2026-08-08", "2026-08-08 ", "2026-08-08\n",
	} {
		if _, err := tzkit.ParseDay(bad); err == nil {
			t.Errorf("ParseDay(%q) must fail", bad)
		}
	}
}

func TestNewDayNormalizes(t *testing.T) {
	t.Parallel()

	if got := tzkit.NewDay(2026, time.April, 31).String(); got != "2026-05-01" {
		t.Fatalf("NewDay(2026-04-31) = %s, want 2026-05-01", got)
	}
}

func TestDayJSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		Due tzkit.Day `json:"due"`
	}

	out, err := json.Marshal(payload{Due: tzkit.NewDay(2026, time.August, 8)})
	if err != nil || string(out) != `{"due":"2026-08-08"}` {
		t.Fatalf("Marshal = %s, %v", out, err)
	}
	out, err = json.Marshal(payload{})
	if err != nil || string(out) != `{"due":null}` {
		t.Fatalf("Marshal(zero) = %s, %v", out, err)
	}

	var got payload
	if err := json.Unmarshal([]byte(`{"due":"2026-08-08"}`), &got); err != nil || got.Due != tzkit.NewDay(2026, time.August, 8) {
		t.Fatalf("Unmarshal = %v, %v", got.Due, err)
	}
	if err := json.Unmarshal([]byte(`{"due":null}`), &got); err != nil || !got.Due.IsZero() {
		t.Fatalf("Unmarshal(null) = %v, %v", got.Due, err)
	}

	// Момент с офсетом в поле дня — ошибка, а не тихое усечение до даты.
	if err := json.Unmarshal([]byte(`{"due":"2026-08-08T00:00:00Z"}`), &got); err == nil {
		t.Error("date-time in a Day field must be rejected")
	}
}

func TestDayCompareAndArithmetic(t *testing.T) {
	t.Parallel()

	a := tzkit.NewDay(2026, time.August, 8)
	b := tzkit.NewDay(2026, time.August, 9)

	if !a.Before(b) || !b.After(a) || !a.Equal(tzkit.NewDay(2026, time.August, 8)) {
		t.Error("Before/After/Equal")
	}
	if a.AddDays(1) != b {
		t.Errorf("AddDays(1) = %v, want %v", a.AddDays(1), b)
	}
	if a.DaysUntil(b) != 1 || b.DaysUntil(a) != -1 {
		t.Errorf("DaysUntil = %d / %d", a.DaysUntil(b), b.DaysUntil(a))
	}
	if a.Weekday() != time.Saturday {
		t.Errorf("Weekday = %v, want Saturday", a.Weekday())
	}
}

// AddDays двигает календарь, а не прибавляет 24 часа: в сутки перехода на
// летнее время между началами соседних дней 23 часа, но день следующий.
func TestAddDaysSurvivesDST(t *testing.T) {
	t.Parallel()

	berlin := tzkit.MustLoad("Europe/Berlin")
	day := tzkit.NewDay(2026, time.March, 28)
	dstDay := day.AddDays(1)

	if dstDay.String() != "2026-03-29" {
		t.Fatalf("AddDays = %s", dstDay)
	}
	if got := dstDay.Start(berlin).Sub(day.Start(berlin)); got != 24*time.Hour {
		t.Errorf("gap between starts = %v, want 24h", got)
	}
	// В сутки перевода стрелок 23 часа — но это по-прежнему одни календарные сутки.
	if got := dstDay.End(berlin).Sub(dstDay.Start(berlin)); got != 23*time.Hour {
		t.Errorf("DST day length = %v, want 23h", got)
	}
}

func TestDayAt(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	at := tzkit.NewDay(2026, time.August, 8).At(tzkit.MustParseWallTime("17:00"), msk)

	wantEqual(t, "At", at.UTC(), time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC))
}

func TestWallTime(t *testing.T) {
	t.Parallel()

	w := tzkit.MustParseWallTime("17:05")
	if w.Hour() != 17 || w.Minute() != 5 || w.String() != "17:05" || w.Duration() != 17*time.Hour+5*time.Minute || !w.Valid() {
		t.Fatalf("WallTime 17:05 = %d:%d %q %v valid=%v", w.Hour(), w.Minute(), w.String(), w.Duration(), w.Valid())
	}

	for _, bad := range []string{"7:05", "17:5", "25:00", "17:60", "", "17:05:00"} {
		if _, err := tzkit.ParseWallTime(bad); err == nil {
			t.Errorf("ParseWallTime(%q) must fail", bad)
		}
	}
	if _, err := tzkit.NewWallTime(24, 0); err == nil {
		t.Error("NewWallTime(24, 0) must fail")
	}
}

func TestWallTimeJSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		From tzkit.WallTime `json:"from"`
	}

	out, err := json.Marshal(payload{From: tzkit.MustParseWallTime("08:00")})
	if err != nil || string(out) != `{"from":"08:00"}` {
		t.Fatalf("Marshal = %s, %v", out, err)
	}

	var got payload
	if err := json.Unmarshal([]byte(`{"from":"20:30"}`), &got); err != nil || got.From != tzkit.MustParseWallTime("20:30") {
		t.Fatalf("Unmarshal = %v, %v", got.From, err)
	}
	if err := json.Unmarshal([]byte(`{"from":"20:30:00"}`), &got); err == nil {
		t.Error("seconds in a WallTime field must be rejected")
	}
}

func TestPgDateRoundTrip(t *testing.T) {
	t.Parallel()

	day := tzkit.NewDay(2026, time.August, 8)
	if got := tzkit.DayFromPg(day.PgDate()); got != day {
		t.Errorf("round trip = %v, want %v", got, day)
	}
	if (tzkit.Day{}).PgDate().Valid {
		t.Error("zero Day must map to NULL")
	}
	if !tzkit.DayFromPg(pgtype.Date{}).IsZero() {
		t.Error("NULL must map to zero Day")
	}
	if tzkit.DayPtrFromPg(pgtype.Date{}) != nil {
		t.Error("DayPtrFromPg(NULL) must be nil")
	}
	if got := tzkit.DayPtrFromPg(tzkit.PgDatePtr(&day)); got == nil || *got != day {
		t.Errorf("ptr round trip = %v", got)
	}
	if tzkit.PgDatePtr(nil).Valid {
		t.Error("PgDatePtr(nil) must be NULL")
	}
}

// Момент, чей UTC-день отличается от дня приложения, не должен «съезжать» при
// записи в DATE: в колонку идёт день зоны приложения.
func TestPgDateKeepsApplicationDay(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	at := time.Date(2026, 8, 7, 22, 0, 0, 0, time.UTC) // 8 августа 01:00 MSK

	if got := tzkit.DayFromPg(tzkit.DayOf(at, msk).PgDate()).String(); got != "2026-08-08" {
		t.Fatalf("DATE = %s, want 2026-08-08", got)
	}
}

func TestPgTimeRoundTrip(t *testing.T) {
	t.Parallel()

	w := tzkit.MustParseWallTime("19:00")
	got, ok := tzkit.WallTimeFromPg(w.PgTime())
	if !ok || got != w {
		t.Errorf("round trip = %v, %v", got, ok)
	}
	if _, ok := tzkit.WallTimeFromPg(pgtype.Time{}); ok {
		t.Error("NULL must give ok=false")
	}
	if tzkit.WallTimePtrFromPg(pgtype.Time{}) != nil {
		t.Error("WallTimePtrFromPg(NULL) must be nil")
	}
	if p := tzkit.WallTimePtrFromPg(tzkit.PgTimePtr(&w)); p == nil || *p != w {
		t.Errorf("ptr round trip = %v", p)
	}
	if tzkit.PgTimePtr(nil).Valid {
		t.Error("PgTimePtr(nil) must be NULL")
	}
}

func TestNewZone(t *testing.T) {
	t.Parallel()

	z, err := tzkit.NewZone(tzkit.Config{TZ: "Europe/Moscow"})
	if err != nil || z.Name() != "Europe/Moscow" {
		t.Fatalf("NewZone = %v, %v", z, err)
	}
	if _, err := tzkit.NewZone(tzkit.Config{}); !errors.Is(err, tzkit.ErrEmptyZone) {
		t.Fatalf("NewZone(empty) err = %v, want ErrEmptyZone", err)
	}
}

func wantEqual(t *testing.T, what string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}
