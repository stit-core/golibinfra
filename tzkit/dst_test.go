package tzkit_test

import (
	"archive/zip"
	"fmt"
	"go/build"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/tzkit"
)

// Зоны, где полночи может не быть или она может случиться дважды: перевод
// стрелок в 00:00 (Santiago, Havana, Asuncion, Amman, Azores), провал,
// упирающийся в полночь (Nuuk, Scoresbysund), и сутки, которых в зоне не было
// (Apia). Полный набор tzdata проходит TestTzdataSweep.
var sweepZones = []string{
	"America/Santiago",
	"Atlantic/Azores",
	"America/Havana",
	"America/Asuncion",
	"Asia/Amman",
	"America/Nuuk",
	"America/Scoresbysund",
	"Pacific/Apia",
	"Europe/Moscow",
	"UTC",
}

// Границы sweep'ов: 2000–2030 покрывает всю историю переводов стрелок, ради
// которых существует resolveCivil, и весь горизонт планирования сервисов.
var (
	sweepFrom = tzkit.NewDay(2000, time.January, 1)
	sweepTo   = tzkit.NewDay(2031, time.January, 1)
)

// wallStep — шаг перебора времени суток в минутах: сетка мельче самого мелкого
// перевода стрелок в базе (Australia/Lord_Howe двигает стрелки на 30 минут),
// поэтому попадает внутрь любого провала и любого наложения.
const wallStep = 10

// boundaryViolation — нарушение инварианта границ суток d в зоне z: Start
// принадлежит своим суткам и является их первым моментом, End — первый момент
// следующих. Календарный день, которого в зоне не было вовсе, пуст: Start == End.
func boundaryViolation(z tzkit.Zone, d tzkit.Day) string {
	start, end := d.Start(z), d.End(z)
	switch {
	case end.Before(start):
		return fmt.Sprintf("End %v is before Start %v", end, start)
	case start.Equal(end):
		if !tzkit.DayOf(start, z).After(d) {
			return fmt.Sprintf("empty day resolves to %s", tzkit.DayOf(start, z))
		}
	case tzkit.DayOf(start, z) != d:
		return fmt.Sprintf("Start %v belongs to %s", start, tzkit.DayOf(start, z))
	case tzkit.DayOf(end.Add(-time.Nanosecond), z) != d:
		last := end.Add(-time.Nanosecond)
		return fmt.Sprintf("last nanosecond %v belongs to %s", last, tzkit.DayOf(last, z))
	}
	return ""
}

// wallViolation — нарушение инварианта At на сутках d в зоне z: момент любого
// времени суток лежит в [Start, End); у суток, которых не было, — равен Start.
func wallViolation(z tzkit.Zone, d tzkit.Day) string {
	start, end := d.Start(z), d.End(z)
	for m := 0; m < 24*60; m += wallStep {
		w := tzkit.WallTime(m)
		at := d.At(w, z)
		switch {
		case !start.Before(end):
			if !at.Equal(start) {
				return fmt.Sprintf("At(%s) = %v, want the empty-day moment %v", w, at, start)
			}
		case at.Before(start) || !at.Before(end):
			return fmt.Sprintf("At(%s) = %v, outside [%v, %v)", w, at, start, end)
		case tzkit.DayOf(at, z) != d:
			return fmt.Sprintf("At(%s) = %v belongs to %s", w, at, tzkit.DayOf(at, z))
		}
	}
	return ""
}

// transitionDays — сутки, на которых зона меняет смещение, вместе с соседними:
// вне их смещение постоянно и инварианты держатся арифметически. Смена
// определяется по смещениям на границах суток, а не обходом ZoneBounds: за
// последним переходом tzdata тот отдаёт интервал, не содержащий своего момента.
func transitionDays(z tzkit.Zone) []tzkit.Day {
	changed := map[tzkit.Day]bool{}
	for day := sweepFrom.AddDays(-1); !day.After(sweepTo); day = day.AddDays(1) {
		start, end := day.Start(z), day.End(z)
		_, atStart := start.Zone()
		_, atEnd := end.Zone()
		if atStart != atEnd || !start.Before(end) {
			changed[day] = true
		}
	}

	var out []tzkit.Day
	for day := sweepFrom; day.Before(sweepTo); day = day.AddDays(1) {
		if changed[day.AddDays(-1)] || changed[day] || changed[day.AddDays(1)] {
			out = append(out, day)
		}
	}
	return out
}

// Инвариант границ суток на каждом дне 2000–2030 в зонах с переходом в полночь.
// Без него Span уносит последний час суток в соседнюю выборку.
func TestDayBoundariesSweep(t *testing.T) {
	t.Parallel()

	for _, name := range sweepZones {
		z := tzkit.MustLoad(name)
		violations := 0
		for day := sweepFrom; day.Before(sweepTo); day = day.AddDays(1) {
			if v := boundaryViolation(z, day); v != "" {
				violations++
				if violations <= 3 {
					t.Errorf("%s %s: %s", name, day, v)
				}
			}
		}
		if violations != 0 {
			t.Errorf("%s: %d days with broken boundaries", name, violations)
		}
	}
}

// Полночь, которой не существует: сутки открывает момент перевода стрелок.
func TestDayStartWhenMidnightIsSkipped(t *testing.T) {
	t.Parallel()

	santiago := tzkit.MustLoad("America/Santiago")
	day := tzkit.NewDay(2026, time.September, 6)
	start := day.Start(santiago)

	if got, want := start.UTC(), time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Start = %v, want %v", got, want)
	}
	if got := start.Format("15:04:05 -0700"); got != "01:00:00 -0300" {
		t.Errorf("Start in zone = %s, want 01:00:00 -0300", got)
	}
	if got := tzkit.DayOf(start, santiago); got != day {
		t.Errorf("DayOf(Start) = %s, want %s", got, day)
	}
	if got := day.End(santiago).Sub(start); got != 23*time.Hour {
		t.Errorf("day length = %v, want 23h", got)
	}
}

// Полночь, которая случается дважды: сутки открывает первое вхождение.
func TestDayStartWhenMidnightRepeats(t *testing.T) {
	t.Parallel()

	// Гавана 1 ноября 2026 переводит стрелки назад в 01:00 — полночь наступает дважды.
	havana := tzkit.MustLoad("America/Havana")
	day := tzkit.NewDay(2026, time.November, 1)
	start := day.Start(havana)

	if got := start.Format("15:04:05 -0700"); got != "00:00:00 -0400" {
		t.Errorf("Start in zone = %s, want the first midnight 00:00:00 -0400", got)
	}
	if got := tzkit.DayOf(start.Add(-time.Nanosecond), havana); got != day.AddDays(-1) {
		t.Errorf("nanosecond before Start belongs to %s, want %s", got, day.AddDays(-1))
	}
	if got := day.End(havana).Sub(start); got != 25*time.Hour {
		t.Errorf("day length = %v, want 25h", got)
	}
}

// Сутки перевода стрелок длятся 23 и 25 часов, но остаются одними сутками.
func TestDayLengthAroundDST(t *testing.T) {
	t.Parallel()

	berlin := tzkit.MustLoad("Europe/Berlin")
	spring := tzkit.NewDay(2026, time.March, 29)
	autumn := tzkit.NewDay(2026, time.October, 25)

	if got := spring.End(berlin).Sub(spring.Start(berlin)); got != 23*time.Hour {
		t.Errorf("spring day = %v, want 23h", got)
	}
	if got := autumn.End(berlin).Sub(autumn.Start(berlin)); got != 25*time.Hour {
		t.Errorf("autumn day = %v, want 25h", got)
	}
	if got := spring.Start(berlin).Format("15:04:05 -0700"); got != "00:00:00 +0100" {
		t.Errorf("spring Start = %s, want 00:00:00 +0100", got)
	}
}

// At: времени в провале соответствует момент, сдвинутый вперёд на длину
// провала; времени, которое повторяется, — первое вхождение.
func TestDayAtAroundDST(t *testing.T) {
	t.Parallel()

	berlin := tzkit.MustLoad("Europe/Berlin")

	gap := tzkit.NewDay(2026, time.March, 29).At(tzkit.MustParseWallTime("02:30"), berlin)
	if got := gap.Format("15:04:05 -0700"); got != "03:30:00 +0200" {
		t.Errorf("At in a gap = %s, want 03:30:00 +0200", got)
	}

	overlap := tzkit.NewDay(2026, time.October, 25).At(tzkit.MustParseWallTime("02:30"), berlin)
	if got := overlap.Format("15:04:05 -0700"); got != "02:30:00 +0200" {
		t.Errorf("At in an overlap = %s, want the first occurrence 02:30:00 +0200", got)
	}
	if got := overlap.Add(time.Hour).Format("15:04:05 -0700"); got != "02:30:00 +0100" {
		t.Errorf("an hour after the first occurrence = %s, want 02:30:00 +0100", got)
	}
}

// At не уводит момент в соседние сутки: время суток из провала остаётся в своём
// дне на всех переводах стрелок 2000–2030 с шагом wallStep минут.
func TestDayAtStaysWithinTheDay(t *testing.T) {
	t.Parallel()

	for _, name := range sweepZones {
		z := tzkit.MustLoad(name)
		for _, day := range transitionDays(z) {
			if v := wallViolation(z, day); v != "" {
				t.Fatalf("%s %s: %s", name, day, v)
			}
		}
	}
}

// Провал перевода стрелок, упирающийся в полночь: 23:00–23:59 в зоне не
// существует, и сдвиг вперёд вынес бы момент в следующую дату. America/Nuuk и
// America/Scoresbysund проходят через это каждый март.
func TestDayAtClampsWhenTheGapReachesMidnight(t *testing.T) {
	t.Parallel()

	z := tzkit.MustLoad("America/Scoresbysund")
	day := tzkit.NewDay(2025, time.March, 29)
	at := day.At(tzkit.MustParseWallTime("23:30"), z)

	if got, want := at, day.End(z).Add(-time.Nanosecond); !got.Equal(want) {
		t.Errorf("At(23:30) = %v, want the last moment of the day %v", got, want)
	}
	if got := tzkit.DayOf(at, z); got != day {
		t.Errorf("At(23:30) belongs to %s, want %s", got, day)
	}
	if got := at.Format("15:04:05.999999999 -0700"); got != "22:59:59.999999999 -0200" {
		t.Errorf("At(23:30) in zone = %s, want 22:59:59.999999999 -0200", got)
	}
}

// Календарного дня в зоне могло не быть вовсе: Pacific/Apia 30 декабря 2011
// перешла через линию перемены дат, пропустив сутки целиком.
func TestDayThatDidNotExistInTheZone(t *testing.T) {
	t.Parallel()

	z := tzkit.MustLoad("Pacific/Apia")
	day := tzkit.NewDay(2011, time.December, 30)
	start, end := day.Start(z), day.End(z)

	if !start.Equal(end) {
		t.Fatalf("Start = %v, End = %v, want an empty day", start, end)
	}
	if got := tzkit.DayOf(start, z); got != day.AddDays(1) {
		t.Errorf("DayOf(Start) = %s, want %s", got, day.AddDays(1))
	}
	spanStart, spanEnd := tzkit.Span(day, day, z)
	if spanEnd.Sub(spanStart) != 0 {
		t.Errorf("Span = [%v, %v), want an empty interval", spanStart, spanEnd)
	}
	for _, s := range []string{"00:00", "12:00", "23:59"} {
		if got := day.At(tzkit.MustParseWallTime(s), z); !got.Equal(start) {
			t.Errorf("At(%s) = %v, want %v", s, got, start)
		}
	}
	// Соседние сутки существуют и стыкуются встык через пустой день.
	if got, want := day.AddDays(-1).End(z), start; !got.Equal(want) {
		t.Errorf("End of the previous day = %v, want %v", got, want)
	}
}

// Инварианты границ суток на всех зонах tzdata и всех днях 2000–2030, а At —
// на всех сутках перевода стрелок с шагом wallStep минут: зоны с провалом до
// полуночи и с пропущенными сутками ни в какую выборку по именам не попадают.
// Вне суток перевода смещение постоянно, и At сводится к арифметике над Start.
func TestTzdataSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("skip the full tzdata sweep in -short")
	}
	t.Parallel()

	zones := tzdataZones(t)
	if len(zones) < 400 {
		t.Fatalf("tzdata has %d zones, want the full database", len(zones))
	}
	t.Logf("sweeping %d zones × %s..%s × %d wall times", len(zones), sweepFrom, sweepTo, 24*60/wallStep)

	names := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	report := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		t.Errorf(format, args...)
	}
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for name := range names {
				z, err := tzkit.Load(name)
				if err != nil {
					report("Load(%q): %v", name, err)
					continue
				}
				for day := sweepFrom; day.Before(sweepTo); day = day.AddDays(1) {
					if v := boundaryViolation(z, day); v != "" {
						report("%s %s: %s", name, day, v)
						break
					}
				}
				for _, day := range transitionDays(z) {
					if v := wallViolation(z, day); v != "" {
						report("%s %s: %s", name, day, v)
						break
					}
				}
			}
		})
	}
	for _, name := range zones {
		names <- name
	}
	close(names)
	wg.Wait()
}

// tzdataZones — имена всех зон базы: сначала zoneinfo.zip тулчейна (ровно то,
// что кит встраивает через time/tzdata), затем каталог zoneinfo системы.
func tzdataZones(t *testing.T) []string {
	t.Helper()

	if names := zonesFromZip(filepath.Join(build.Default.GOROOT, "lib", "time", "zoneinfo.zip")); len(names) > 0 {
		return names
	}
	for _, dir := range []string{os.Getenv("TZDIR"), "/usr/share/zoneinfo", "/usr/share/lib/zoneinfo", "/etc/zoneinfo"} {
		if names := zonesFromDir(dir); len(names) > 0 {
			return names
		}
	}
	t.Skip("no tzdata database found: set TZDIR to a zoneinfo tree")
	return nil
}

func zonesFromZip(path string) []string {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil
	}
	defer r.Close()

	out := make([]string, 0, len(r.File))
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, "/") {
			out = append(out, f.Name)
		}
	}
	return out
}

func zonesFromDir(dir string) []string {
	if dir == "" {
		return nil
	}
	var out []string
	_ = fs.WalkDir(os.DirFS(dir), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isTZif(filepath.Join(dir, path)) {
			return nil //nolint:nilerr // нечитаемый файл каталога — просто не зона
		}
		out = append(out, path)
		return nil
	})
	return out
}

// Каталог zoneinfo держит рядом с зонами таблицы (iso3166.tab, tzdata.zi):
// зону от них отличает сигнатура TZif.
func isTZif(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return string(magic[:]) == "TZif"
}

// Span на сутках с переходом покрывает их целиком: последний час не уезжает в
// соседнюю выборку.
func TestSpanCoversDSTDayCompletely(t *testing.T) {
	t.Parallel()

	santiago := tzkit.MustLoad("America/Santiago")
	day := tzkit.NewDay(2026, time.September, 5)
	start, end := tzkit.Span(day, day, santiago)

	last := end.Add(-time.Nanosecond)
	if got := tzkit.DayOf(last, santiago); got != day {
		t.Errorf("last nanosecond of the span belongs to %s, want %s", got, day)
	}
	if got := end.Sub(start); got != 24*time.Hour {
		t.Errorf("span length = %v, want 24h", got)
	}
	if got := tzkit.DayOf(end, santiago); got != day.AddDays(1) {
		t.Errorf("span end belongs to %s, want %s", got, day.AddDays(1))
	}
}

func TestStartOfDayAroundMidnightTransition(t *testing.T) {
	t.Parallel()

	havana := tzkit.MustLoad("America/Havana")
	at := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	day := tzkit.DayOf(at, havana)

	if got := tzkit.StartOfDay(at, havana); !got.Equal(day.Start(havana)) {
		t.Errorf("StartOfDay = %v, want %v", got, day.Start(havana))
	}
	if got := tzkit.DayOf(tzkit.StartOfDay(at, havana), havana); got != day {
		t.Errorf("StartOfDay belongs to %s, want %s", got, day)
	}
}
