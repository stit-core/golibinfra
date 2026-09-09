package tzkit_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/tzkit"
)

var (
	_ pgtype.DateValuer  = tzkit.Day{}
	_ pgtype.DateScanner = (*tzkit.Day)(nil)
	_ pgtype.TimeValuer  = tzkit.WallTime(0)
	_ pgtype.TimeScanner = (*tzkit.WallTime)(nil)
)

func TestMustConstructors(t *testing.T) {
	t.Parallel()

	if got := tzkit.MustParseDay("2026-08-08"); got != tzkit.NewDay(2026, time.August, 8) {
		t.Errorf("MustParseDay = %v", got)
	}
	if got := tzkit.MustParseWallTime("17:05"); got.String() != "17:05" {
		t.Errorf("MustParseWallTime = %v", got)
	}

	for name, fn := range map[string]func(){
		"MustParseDay":      func() { tzkit.MustParseDay("2026-8-8") },
		"MustParseWallTime": func() { tzkit.MustParseWallTime("25:00") },
		"MustLoad":          func() { tzkit.MustLoad("Europe/Atlantis") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("%s must panic on invalid input", name)
				}
			}()
			fn()
		})
	}
}

func TestDayText(t *testing.T) {
	t.Parallel()

	day := tzkit.NewDay(2026, time.August, 8)
	b, err := day.MarshalText()
	if err != nil || string(b) != "2026-08-08" {
		t.Fatalf("MarshalText = %q, %v", b, err)
	}
	if b, err := (tzkit.Day{}).MarshalText(); err != nil || len(b) != 0 {
		t.Fatalf("MarshalText(zero) = %q, %v", b, err)
	}

	var got tzkit.Day
	if err := got.UnmarshalText([]byte("2026-08-08")); err != nil || got != day {
		t.Fatalf("UnmarshalText = %v, %v", got, err)
	}
	if err := got.UnmarshalText(nil); err != nil || !got.IsZero() {
		t.Fatalf("UnmarshalText(empty) = %v, %v", got, err)
	}
	if err := got.UnmarshalText([]byte("2026-8-8")); err == nil {
		t.Error("UnmarshalText must reject a loose format")
	}

	// Ключ map — путь именно через TextMarshaler.
	out, err := json.Marshal(map[tzkit.Day]int{day: 1})
	if err != nil || string(out) != `{"2026-08-08":1}` {
		t.Fatalf("map key = %s, %v", out, err)
	}
}

func TestWallTimeText(t *testing.T) {
	t.Parallel()

	b, err := tzkit.MustParseWallTime("08:30").MarshalText()
	if err != nil || string(b) != "08:30" {
		t.Fatalf("MarshalText = %q, %v", b, err)
	}

	var got tzkit.WallTime
	if err := got.UnmarshalText([]byte("20:30")); err != nil || got != tzkit.MustParseWallTime("20:30") {
		t.Fatalf("UnmarshalText = %v, %v", got, err)
	}
	if err := got.UnmarshalText([]byte("20:30:00")); err == nil {
		t.Error("UnmarshalText must reject seconds")
	}
}

// Регрессия: значение вне суток не должно выглядеть валидным временем ни в
// String, ни в тексте, ни в JSON — обратно оно не разберётся.
func TestWallTimeOutsideTheDay(t *testing.T) {
	t.Parallel()

	for _, w := range []tzkit.WallTime{-5, 1440, 1500} {
		if w.Valid() {
			t.Fatalf("WallTime(%d).Valid() must be false", int(w))
		}
		if got := w.String(); !strings.HasPrefix(got, "WallTime(") {
			t.Errorf("String(%d) = %q, want an explicit invalid marker", int(w), got)
		}
		if _, err := w.MarshalText(); err == nil {
			t.Errorf("MarshalText(%d) must fail", int(w))
		}
		if _, err := w.MarshalJSON(); err == nil {
			t.Errorf("MarshalJSON(%d) must fail", int(w))
		}
		if _, err := w.TimeValue(); err == nil {
			t.Errorf("TimeValue(%d) must fail", int(w))
		}
	}
}

// Регрессия: «25:00» тихо переносило момент в следующие сутки.
func TestDayAtRejectsWallTimeOutsideTheDay(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("Day.At must panic on a wall time outside the day")
		}
	}()
	tzkit.NewDay(2026, time.August, 8).At(tzkit.WallTime(1500), tzkit.UTC())
}

func TestNewWallTime(t *testing.T) {
	t.Parallel()

	w, err := tzkit.NewWallTime(17, 5)
	if err != nil || w != tzkit.MustParseWallTime("17:05") {
		t.Fatalf("NewWallTime(17, 5) = %v, %v", w, err)
	}
	for _, bad := range [][2]int{{24, 0}, {-1, 0}, {0, 60}, {0, -1}} {
		if _, err := tzkit.NewWallTime(bad[0], bad[1]); err == nil {
			t.Errorf("NewWallTime%v must fail", bad)
		}
	}
}

func TestDayDateAndCompare(t *testing.T) {
	t.Parallel()

	y, m, d := tzkit.NewDay(2026, time.August, 8).Date()
	if y != 2026 || m != time.August || d != 8 {
		t.Errorf("Date = %d-%v-%d", y, m, d)
	}

	base := tzkit.NewDay(2026, time.August, 8)
	tests := []struct {
		other tzkit.Day
		want  int
	}{
		{tzkit.NewDay(2025, time.August, 8), 1},
		{tzkit.NewDay(2027, time.August, 8), -1},
		{tzkit.NewDay(2026, time.July, 8), 1},
		{tzkit.NewDay(2026, time.September, 8), -1},
		{tzkit.NewDay(2026, time.August, 7), 1},
		{tzkit.NewDay(2026, time.August, 8), 0},
	}
	for _, tt := range tests {
		if got := base.Compare(tt.other); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", base, tt.other, got, tt.want)
		}
	}
}

// Регрессия: разница считалась через time.Duration, которая насыщается на
// 292 годах, — 1700 → 2026 давало 106751 вместо 119069.
func TestDaysUntilBeyondDurationRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		from, to tzkit.Day
		want     int
	}{
		{tzkit.NewDay(2026, time.August, 8), tzkit.NewDay(2026, time.August, 9), 1},
		{tzkit.NewDay(2026, time.August, 9), tzkit.NewDay(2026, time.August, 8), -1},
		{tzkit.NewDay(1700, time.January, 1), tzkit.NewDay(2026, time.January, 1), 119069},
		{tzkit.NewDay(1, time.January, 1), tzkit.NewDay(9999, time.January, 1), 3651694},
		{tzkit.NewDay(9999, time.January, 1), tzkit.NewDay(1, time.January, 1), -3651694},
	}
	for _, tt := range tests {
		if got := tt.from.DaysUntil(tt.to); got != tt.want {
			t.Errorf("%s.DaysUntil(%s) = %d, want %d", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestZoneStringAndIn(t *testing.T) {
	t.Parallel()

	msk := tzkit.MustLoad("Europe/Moscow")
	if msk.String() != "Europe/Moscow" || tzkit.UTC().String() != "UTC" {
		t.Errorf("String = %q / %q", msk.String(), tzkit.UTC().String())
	}

	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	in := msk.In(at)
	if !in.Equal(at) {
		t.Errorf("In changed the instant: %v != %v", in, at)
	}
	if in.Hour() != 15 || in.Location() != msk.Location() {
		t.Errorf("In = %v, want 15:00 in Europe/Moscow", in)
	}
}

// Регрессия: PG допускает TIME '24:00:00', для WallTime это не время суток —
// раньше такое значение доезжало до ответа и роняло сериализацию.
func TestWallTimeFromPgOutsideTheDay(t *testing.T) {
	t.Parallel()

	const micros = int64(time.Second / time.Microsecond)
	if _, ok := tzkit.WallTimeFromPg(pgtype.Time{Microseconds: 24 * 3600 * micros, Valid: true}); ok {
		t.Error("TIME '24:00:00' must not become a wall time")
	}
	if tzkit.WallTimePtrFromPg(pgtype.Time{Microseconds: 24 * 3600 * micros, Valid: true}) != nil {
		t.Error("TIME '24:00:00' must give nil")
	}
	if _, ok := tzkit.WallTimeFromPg(pgtype.Time{Microseconds: -1, Valid: true}); ok {
		t.Error("negative TIME must not become a wall time")
	}

	// Секунды колонки отбрасываются: WallTime — минутная сетка.
	got, ok := tzkit.WallTimeFromPg(pgtype.Time{Microseconds: (17*3600 + 5*60 + 42) * micros, Valid: true})
	if !ok || got != tzkit.MustParseWallTime("17:05") {
		t.Errorf("17:05:42 = %v, %v; want 17:05", got, ok)
	}
}

func TestPgxScannerAndValuer(t *testing.T) {
	t.Parallel()

	day := tzkit.NewDay(2026, time.August, 8)
	v, err := day.DateValue()
	if err != nil || !v.Valid {
		t.Fatalf("DateValue = %v, %v", v, err)
	}
	var scanned tzkit.Day
	if err := scanned.ScanDate(v); err != nil || scanned != day {
		t.Fatalf("ScanDate = %v, %v", scanned, err)
	}
	if err := scanned.ScanDate(pgtype.Date{}); err != nil || !scanned.IsZero() {
		t.Fatalf("ScanDate(NULL) = %v, %v", scanned, err)
	}
	if err := scanned.ScanDate(pgtype.Date{Valid: true, InfinityModifier: pgtype.Infinity}); err == nil {
		t.Error("ScanDate(infinity) must fail: it is not a calendar day")
	}
	if !tzkit.DayFromPg(pgtype.Date{Valid: true, InfinityModifier: pgtype.NegativeInfinity}).IsZero() {
		t.Error("DayFromPg(-infinity) must give a zero Day")
	}

	w := tzkit.MustParseWallTime("19:00")
	tv, err := w.TimeValue()
	if err != nil || !tv.Valid {
		t.Fatalf("TimeValue = %v, %v", tv, err)
	}
	var scannedWall tzkit.WallTime
	if err := scannedWall.ScanTime(tv); err != nil || scannedWall != w {
		t.Fatalf("ScanTime = %v, %v", scannedWall, err)
	}
	if err := scannedWall.ScanTime(pgtype.Time{}); err == nil {
		t.Error("ScanTime(NULL) must fail: midnight is a valid wall time")
	}
}

func TestNewConfig(t *testing.T) {
	t.Setenv("APP_TZ", "Europe/Moscow")

	cfg, err := tzkit.NewConfig()
	if err != nil || cfg.TZ != "Europe/Moscow" {
		t.Fatalf("NewConfig = %v, %v", cfg, err)
	}

	t.Setenv("APP_TZ", "")
	if _, err := tzkit.NewConfig(); !errors.Is(err, tzkit.ErrConfig) || !errors.Is(err, tzkit.ErrEmptyZone) {
		t.Fatalf("NewConfig(empty APP_TZ) err = %v, want ErrConfig and ErrEmptyZone", err)
	}
}

// APP_TZ обязателен: дефолта нет, потому что подставленная зона — это чужие
// границы суток без единого сигнала.
func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		tz      string
		wantErr error
	}{
		"zone":    {tz: "Europe/Moscow"},
		"utc":     {tz: "UTC"},
		"empty":   {tz: "", wantErr: tzkit.ErrEmptyZone},
		"local":   {tz: "Local", wantErr: tzkit.ErrLocalZone},
		"unknown": {tz: "Europe/Atlantis", wantErr: tzkit.ErrConfig},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := tzkit.Config{TZ: tt.tz}.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tzkit.ErrConfig) || !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate = %v, want ErrConfig and %v", err, tt.wantErr)
			}
		})
	}
}

func TestModuleProvidesZone(t *testing.T) {
	t.Parallel()

	var got tzkit.Zone
	app := fx.New(
		fx.NopLogger,
		fx.Supply(tzkit.Config{TZ: "Europe/Moscow"}),
		tzkit.Module,
		fx.Invoke(func(z tzkit.Zone) { got = z }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	if got.Name() != "Europe/Moscow" {
		t.Fatalf("Zone = %s, want Europe/Moscow", got.Name())
	}

	// Невалидная зона валит сборку графа, а не первый запрос.
	bad := fx.New(
		fx.NopLogger,
		fx.Supply(tzkit.Config{}),
		tzkit.Module,
		fx.Invoke(func(tzkit.Zone) {}),
	)
	if err := bad.Err(); !errors.Is(err, tzkit.ErrEmptyZone) {
		t.Fatalf("fx err = %v, want ErrEmptyZone", err)
	}
}

func TestJSONRejectsNonStrings(t *testing.T) {
	t.Parallel()

	var d tzkit.Day
	if err := json.Unmarshal([]byte(`42`), &d); err == nil {
		t.Error("Day must reject a non-string JSON value")
	}
	if err := json.Unmarshal([]byte(`""`), &d); err != nil || !d.IsZero() {
		t.Errorf(`Day from "" = %v, %v; want the zero day`, d, err)
	}

	var w tzkit.WallTime
	if err := json.Unmarshal([]byte(`1020`), &w); err == nil {
		t.Error("WallTime must reject a non-string JSON value")
	}
}
