package tzkit_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"git.stit.tech/stit-core/golibinfra/tzkit"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	z, err := tzkit.Load("Europe/Moscow")
	require.NoError(t, err)
	require.Equal(t, "Europe/Moscow", z.Name())
	require.False(t, z.IsZero())

	_, err = tzkit.Load("")
	require.ErrorIs(t, err, tzkit.ErrEmptyZone)

	_, err = tzkit.Load("Local")
	require.ErrorIs(t, err, tzkit.ErrLocalZone)

	_, err = tzkit.Load("Europe/Atlantis")
	require.Error(t, err)
}

func TestZoneZeroValueIsUTC(t *testing.T) {
	t.Parallel()

	var z tzkit.Zone
	require.True(t, z.IsZero())
	require.Equal(t, "UTC", z.Name())
	require.Equal(t, time.UTC, z.Location())
}

func TestZoneOffsetAt(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	require.Equal(t, 3*time.Hour, msk.OffsetAt(time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)))

	// Зона с переходом на летнее время: смещение зависит от даты.
	berlin := tzkit.MustLoad("Europe/Berlin")
	require.Equal(t, time.Hour, berlin.OffsetAt(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)))
	require.Equal(t, 2*time.Hour, berlin.OffsetAt(time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)))
}

// Ядро проблемы, ради которой существует kit: у момента 22:00 UTC московский
// день — уже следующий. Расчёт «сегодня» в UTC ошибается ровно здесь.
func TestDayOfCrossesUTCBoundary(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	at := time.Date(2026, 8, 7, 22, 30, 0, 0, time.UTC)

	require.Equal(t, "2026-08-08", tzkit.DayOf(at, msk).String())
	require.Equal(t, "2026-08-07", tzkit.DayOf(at, tzkit.UTC()).String())
}

func TestDayStartEndInZone(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	day := tzkit.NewDay(2026, time.August, 8)

	require.Equal(t, time.Date(2026, 8, 7, 21, 0, 0, 0, time.UTC), day.Start(msk).UTC())
	require.Equal(t, time.Date(2026, 8, 8, 21, 0, 0, 0, time.UTC), day.End(msk).UTC())
	require.Equal(t, day.AddDays(1).Start(msk), day.End(msk))
}

func TestSpanCoversWholeMonthInZone(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	from := tzkit.NewDay(2026, time.August, 1)
	to := tzkit.NewDay(2026, time.August, 31)

	start, end := tzkit.Span(from, to, msk)

	require.Equal(t, time.Date(2026, 7, 31, 21, 0, 0, 0, time.UTC), start.UTC())
	require.Equal(t, time.Date(2026, 8, 31, 21, 0, 0, 0, time.UTC), end.UTC())
}

func TestStartOfDay(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	at := time.Date(2026, 8, 8, 0, 30, 0, 0, time.UTC) // 03:30 MSK

	require.Equal(t, time.Date(2026, 8, 7, 21, 0, 0, 0, time.UTC), tzkit.StartOfDay(at, msk).UTC())
}

func TestParseDayIsStrict(t *testing.T) {
	t.Parallel()

	d, err := tzkit.ParseDay("2026-08-08")
	require.NoError(t, err)
	require.Equal(t, tzkit.NewDay(2026, time.August, 8), d)

	for _, bad := range []string{"2026-8-8", "08-08-2026", "2026-08-08T00:00:00Z", "", "2026-13-01"} {
		_, err := tzkit.ParseDay(bad)
		require.Error(t, err, bad)
	}
}

func TestNewDayNormalizes(t *testing.T) {
	t.Parallel()

	require.Equal(t, "2026-05-01", tzkit.NewDay(2026, time.April, 31).String())
}

func TestDayJSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		Due tzkit.Day `json:"due"`
	}

	out, err := json.Marshal(payload{Due: tzkit.NewDay(2026, time.August, 8)})
	require.NoError(t, err)
	require.JSONEq(t, `{"due":"2026-08-08"}`, string(out))

	out, err = json.Marshal(payload{})
	require.NoError(t, err)
	require.JSONEq(t, `{"due":null}`, string(out))

	var got payload
	require.NoError(t, json.Unmarshal([]byte(`{"due":"2026-08-08"}`), &got))
	require.Equal(t, tzkit.NewDay(2026, time.August, 8), got.Due)

	require.NoError(t, json.Unmarshal([]byte(`{"due":null}`), &got))
	require.True(t, got.Due.IsZero())

	// Момент с офсетом в поле дня — ошибка, а не тихое усечение до даты.
	require.Error(t, json.Unmarshal([]byte(`{"due":"2026-08-08T00:00:00Z"}`), &got))
}

func TestDayCompareAndArithmetic(t *testing.T) {
	t.Parallel()

	a := tzkit.NewDay(2026, time.August, 8)
	b := tzkit.NewDay(2026, time.August, 9)

	require.True(t, a.Before(b))
	require.True(t, b.After(a))
	require.True(t, a.Equal(tzkit.NewDay(2026, time.August, 8)))
	require.Equal(t, b, a.AddDays(1))
	require.Equal(t, 1, a.DaysUntil(b))
	require.Equal(t, -1, b.DaysUntil(a))
	require.Equal(t, time.Saturday, a.Weekday())
}

// AddDays двигает календарь, а не прибавляет 24 часа: в сутки перехода на
// летнее время между началами соседних дней 23 часа, но день следующий.
func TestAddDaysSurvivesDST(t *testing.T) {
	t.Parallel()

	berlin := tzkit.MustLoad("Europe/Berlin")
	day := tzkit.NewDay(2026, time.March, 28)
	dstDay := day.AddDays(1)

	require.Equal(t, "2026-03-29", dstDay.String())
	require.Equal(t, 24*time.Hour, dstDay.Start(berlin).Sub(day.Start(berlin)))
	// В сутки перевода стрелок 23 часа — но это по-прежнему одни календарные сутки.
	require.Equal(t, 23*time.Hour, dstDay.End(berlin).Sub(dstDay.Start(berlin)))
}

func TestDayAt(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	at := tzkit.NewDay(2026, time.August, 8).At(mustWall(t, "17:00"), msk)

	require.Equal(t, time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC), at.UTC())
}

func TestWallTime(t *testing.T) {
	t.Parallel()

	w := mustWall(t, "17:05")
	require.Equal(t, 17, w.Hour())
	require.Equal(t, 5, w.Minute())
	require.Equal(t, "17:05", w.String())
	require.Equal(t, 17*time.Hour+5*time.Minute, w.Duration())
	require.True(t, w.Valid())

	for _, bad := range []string{"7:05", "17:5", "25:00", "17:60", "", "17:05:00"} {
		_, err := tzkit.ParseWallTime(bad)
		require.Error(t, err, bad)
	}

	_, err := tzkit.NewWallTime(24, 0)
	require.Error(t, err)
}

func TestWallTimeJSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		From tzkit.WallTime `json:"from"`
	}

	out, err := json.Marshal(payload{From: mustWall(t, "08:00")})
	require.NoError(t, err)
	require.JSONEq(t, `{"from":"08:00"}`, string(out))

	var got payload
	require.NoError(t, json.Unmarshal([]byte(`{"from":"20:30"}`), &got))
	require.Equal(t, mustWall(t, "20:30"), got.From)

	require.Error(t, json.Unmarshal([]byte(`{"from":"20:30:00"}`), &got))
}

func TestPgDateRoundTrip(t *testing.T) {
	t.Parallel()

	day := tzkit.NewDay(2026, time.August, 8)
	require.Equal(t, day, tzkit.DayFromPg(day.PgDate()))

	require.False(t, tzkit.Day{}.PgDate().Valid)
	require.True(t, tzkit.DayFromPg(pgtype.Date{}).IsZero())

	require.Nil(t, tzkit.DayPtrFromPg(pgtype.Date{}))
	require.Equal(t, day, *tzkit.DayPtrFromPg(tzkit.PgDatePtr(&day)))
	require.False(t, tzkit.PgDatePtr(nil).Valid)
}

// Момент, чей UTC-день отличается от дня приложения, не должен «съезжать» при
// записи в DATE: в колонку идёт день зоны приложения.
func TestPgDateKeepsApplicationDay(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	at := time.Date(2026, 8, 7, 22, 0, 0, 0, time.UTC) // 8 августа 01:00 MSK

	require.Equal(t, "2026-08-08", tzkit.DayFromPg(tzkit.DayOf(at, msk).PgDate()).String())
}

func TestPgTimeRoundTrip(t *testing.T) {
	t.Parallel()

	w := mustWall(t, "19:00")
	got, ok := tzkit.WallTimeFromPg(w.PgTime())
	require.True(t, ok)
	require.Equal(t, w, got)

	_, ok = tzkit.WallTimeFromPg(pgtype.Time{})
	require.False(t, ok)

	require.Nil(t, tzkit.WallTimePtrFromPg(pgtype.Time{}))
	require.Equal(t, w, *tzkit.WallTimePtrFromPg(tzkit.PgTimePtr(&w)))
	require.False(t, tzkit.PgTimePtr(nil).Valid)
}

func TestNewZone(t *testing.T) {
	t.Parallel()

	z, err := tzkit.NewZone(tzkit.Config{TZ: "Europe/Moscow"})
	require.NoError(t, err)
	require.Equal(t, "Europe/Moscow", z.Name())

	_, err = tzkit.NewZone(tzkit.Config{})
	require.ErrorIs(t, err, tzkit.ErrEmptyZone)
}

func mustWall(t *testing.T, s string) tzkit.WallTime {
	t.Helper()
	w, err := tzkit.ParseWallTime(s)
	require.NoError(t, err)
	return w
}
