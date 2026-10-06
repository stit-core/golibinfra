package envconf

import (
	"errors"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type upper string

func (u *upper) Decode(value string) error {
	if value == "bad" {
		return errors.New("bad value")
	}
	*u = upper(strings.ToUpper(value))
	return nil
}

type inner struct {
	Port int `envconfig:"T_PORT" default:"80"`
}

type spec struct {
	Name     string        `envconfig:"T_NAME" default:"svc"`
	Path     string        `envconfig:"T_PATH" default:"/migrations"`
	Count    int32         `envconfig:"T_COUNT" default:"3"`
	Size     uint64        `envconfig:"T_SIZE"`
	Ratio    float64       `envconfig:"T_RATIO" default:"0.5"`
	On       bool          `envconfig:"T_ON" default:"true"`
	Wait     time.Duration `envconfig:"T_WAIT" default:"2s"`
	List     []string      `envconfig:"T_LIST" default:"a,b"`
	Ints     []int         `envconfig:"T_INTS"`
	Word     upper         `envconfig:"T_WORD"`
	Addr     netip.Addr    `envconfig:"T_ADDR"`
	Opt      *int          `envconfig:"T_OPT"`
	Inner    inner
	Ptr      *inner
	Instance string `ignored:"true"`
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"T_NAME", "T_PATH", "T_COUNT", "T_SIZE", "T_RATIO", "T_ON", "T_WAIT", "T_LIST",
		"T_INTS", "T_WORD", "T_ADDR", "T_OPT", "T_PORT", "X_T_NAME", "X_T_PORT", "INSTANCE",
	} {
		t.Setenv(k, "")
	}
}

func TestLoad_DefaultsAndValues(t *testing.T) {
	clearEnv(t)
	t.Setenv("T_COUNT", "7")
	t.Setenv("T_SIZE", "18446744073709551615")
	t.Setenv("T_RATIO", "0.25")
	t.Setenv("T_ON", "false")
	t.Setenv("T_WAIT", "150ms")
	t.Setenv("T_LIST", " x , ,y ")
	t.Setenv("T_INTS", "1,2")
	t.Setenv("T_WORD", "abc")
	t.Setenv("T_ADDR", "10.0.0.1")
	t.Setenv("T_OPT", "5")
	t.Setenv("T_PORT", "8080")
	t.Setenv("INSTANCE", "nope")

	var s spec
	if err := Load(&s); err != nil {
		t.Fatalf("Load: %v", err)
	}
	opt := 5
	want := spec{
		Name: "svc", Path: "/migrations", Count: 7, Size: 18446744073709551615, Ratio: 0.25, Wait: 150 * time.Millisecond,
		List: []string{"x", "y"}, Ints: []int{1, 2}, Word: "ABC", Addr: netip.MustParseAddr("10.0.0.1"), Opt: &opt,
		Inner: inner{Port: 8080}, Ptr: &inner{Port: 8080},
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("Load = %+v\nwant %+v", s, want)
	}
}

func TestLoad_EmptyValueIsUnset(t *testing.T) {
	clearEnv(t)
	var s spec
	if err := Load(&s); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Name != "svc" || s.Path != "/migrations" || s.Count != 3 || !s.On || s.Wait != 2*time.Second ||
		!reflect.DeepEqual(s.List, []string{"a", "b"}) || s.Inner.Port != 80 {
		t.Fatalf("KEY= must take the default: %+v", s)
	}
	keep := spec{Size: 9, Word: "KEPT"}
	if err := Load(&keep); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if keep.Size != 9 || keep.Word != "KEPT" {
		t.Fatalf("KEY= without a default must leave the field as is: %+v", keep)
	}
}

func TestLoad_EmptyMarkerZeroesTextualFields(t *testing.T) {
	clearEnv(t)
	t.Setenv("T_PATH", Empty)
	t.Setenv("T_LIST", Empty)
	t.Setenv("T_WORD", Empty)
	t.Setenv("T_ADDR", Empty)
	s := spec{Word: "PREVIOUS", Addr: netip.MustParseAddr("10.0.0.1")}
	if err := Load(&s); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Path != "" || s.List != nil || s.Word != "" || s.Addr.IsValid() {
		t.Fatalf("%q must set an explicit zero: Path=%q List=%v Word=%q Addr=%v", Empty, s.Path, s.List, s.Word, s.Addr)
	}

	t.Setenv("T_COUNT", Empty)
	if err := Load(&spec{}); err == nil || !strings.HasPrefix(err.Error(), "T_COUNT: ") {
		t.Fatalf("%q in a number = %v, want a parse error of T_COUNT", Empty, err)
	}
}

// Целые — десятичные: ведущий ноль не делает число восьмеричным, 0x — ошибка.
func TestLoad_IntegersAreDecimal(t *testing.T) {
	clearEnv(t)
	t.Setenv("T_COUNT", "010")
	t.Setenv("T_SIZE", "010")
	var s spec
	if err := Load(&s); err != nil || s.Count != 10 || s.Size != 10 {
		t.Fatalf("Load = %v, Count=%d Size=%d, want 10 and 10", err, s.Count, s.Size)
	}
	t.Setenv("T_COUNT", "0x10")
	if err := Load(&spec{}); err == nil || !strings.HasPrefix(err.Error(), "T_COUNT: ") {
		t.Fatalf("0x10 = %v, want a parse error of T_COUNT", err)
	}
}

// Список из одних разделителей — ошибка: пустой список задаёт только Empty. Пустые элементы
// между значениями отбрасываются.
func TestLoad_ListWithoutElementsIsAnError(t *testing.T) {
	for _, value := range []string{",", " , ", " "} {
		clearEnv(t)
		t.Setenv("T_LIST", value)
		err := Load(&spec{})
		if err == nil || err.Error() != `T_LIST: list has no elements: an empty list is "-"` {
			t.Fatalf("T_LIST=%q: Load = %v, want the empty list error", value, err)
		}
	}
	clearEnv(t)
	t.Setenv("T_LIST", "a,,b")
	var s spec
	if err := Load(&s); err != nil || !reflect.DeepEqual(s.List, []string{"a", "b"}) {
		t.Fatalf("Load = %v, List=%v, want [a b]", err, s.List)
	}
}

func TestLoad_ErrorNamesVariable(t *testing.T) {
	tests := []struct{ key, value, want string }{
		{"T_COUNT", "abc", `T_COUNT: strconv.ParseInt: parsing "abc": invalid syntax`},
		{"T_COUNT", "99999999999", `T_COUNT: strconv.ParseInt: parsing "99999999999": value out of range`},
		{"T_SIZE", "-1", `T_SIZE: strconv.ParseUint: parsing "-1": invalid syntax`},
		{"T_ON", "sure", `T_ON: strconv.ParseBool: parsing "sure": invalid syntax`},
		{"T_WAIT", "5", `T_WAIT: time: missing unit in duration "5"`},
		{"T_INTS", "1,x", `T_INTS: strconv.ParseInt: parsing "x": invalid syntax`},
		{"T_WORD", "bad", "T_WORD: bad value"},
		{"T_ADDR", "host", `T_ADDR: ParseAddr("host"): unable to parse IP`},
		{"T_PORT", "http", `T_PORT: strconv.ParseInt: parsing "http": invalid syntax`},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tt.key, tt.value)
			err := Load(&spec{})
			if err == nil || err.Error() != tt.want {
				t.Fatalf("Load = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoad_Required(t *testing.T) {
	type req struct {
		DSN  string `envconfig:"T_DSN" required:"true"`
		Mode string `envconfig:"T_MODE" required:"true" default:"fast"`
	}
	for _, value := range []string{"", Empty} {
		t.Setenv("T_DSN", value)
		t.Setenv("T_MODE", "")
		var r req
		if err := Load(&r); err == nil || err.Error() != "T_DSN is required" {
			t.Fatalf("T_DSN=%q: Load = %v, want T_DSN is required", value, err)
		}
	}
	t.Setenv("T_DSN", "postgres://x")
	var r req
	if err := Load(&r); err != nil || r.DSN != "postgres://x" || r.Mode != "fast" {
		t.Fatalf("Load = %+v, %v", r, err)
	}
}

func TestLoadPrefixed_Names(t *testing.T) {
	clearEnv(t)
	t.Setenv("T_NAME", "bare")
	t.Setenv("T_PORT", "1")

	var isolated spec
	if err := LoadPrefixed(&isolated, "x", false); err != nil {
		t.Fatalf("LoadPrefixed: %v", err)
	}
	if isolated.Name != "svc" || isolated.Inner.Port != 80 {
		t.Fatalf("without fallback bare names must not leak in: %+v", isolated)
	}

	var withFallback spec
	if err := LoadPrefixed(&withFallback, "x", true); err != nil {
		t.Fatalf("LoadPrefixed: %v", err)
	}
	if withFallback.Name != "bare" || withFallback.Inner.Port != 1 {
		t.Fatalf("with fallback an unset prefixed name takes the bare one: %+v", withFallback)
	}

	t.Setenv("X_T_NAME", "prefixed")
	t.Setenv("X_T_PORT", "2")
	var prefixed spec
	if err := LoadPrefixed(&prefixed, "x", true); err != nil {
		t.Fatalf("LoadPrefixed: %v", err)
	}
	if prefixed.Name != "prefixed" || prefixed.Inner.Port != 2 {
		t.Fatalf("the prefixed name wins, nested struct shares the prefix: %+v", prefixed)
	}

	t.Setenv("X_T_PORT", "")
	t.Setenv("T_PORT", "nan")
	if err := LoadPrefixed(&spec{}, "x", true); err == nil || !strings.HasPrefix(err.Error(), "T_PORT: ") {
		t.Fatalf("an error of the fallback value names the bare variable: %v", err)
	}
}

func TestLoad_UntaggedFieldIsAnError(t *testing.T) {
	type app struct {
		Region string
	}
	err := Load(&app{})
	if err == nil || !strings.Contains(err.Error(), "app.Region has no envconfig tag") {
		t.Fatalf("Load = %v, want an error naming the untagged field", err)
	}
}

// level — тип с разбором Set (как у flag.Value), которого у ридера нет.
type level int

func (l *level) Set(string) error { *l = 1; return nil }

// Тип, значение которого ридер не разбирает, — ошибка Load с именем поля, заданная переменная
// или нет: map, []byte, Set, UnmarshalBinary (url.URL) и список из них, структура без
// экспортируемых полей.
func TestLoad_UnsupportedTypeIsAnError(t *testing.T) {
	type withMap struct {
		F map[string]string `envconfig:"T_F"`
	}
	type withBytes struct {
		F []byte `envconfig:"T_F"`
	}
	type withSetter struct {
		F level `envconfig:"T_F"`
	}
	type withURL struct {
		F url.URL `envconfig:"T_F"`
	}
	type withURLPointer struct {
		F *url.URL `envconfig:"T_F"`
	}
	type withURLList struct {
		F []url.URL `envconfig:"T_F"`
	}
	type withLocation struct {
		F *time.Location `envconfig:"T_F"`
	}
	type withChan struct {
		F chan int `envconfig:"T_F"`
	}
	tests := []struct {
		dst  any
		want string
	}{
		{&withMap{}, "field withMap.F: unsupported type map[string]string: "},
		{&withBytes{}, "field withBytes.F: unsupported type []uint8: "},
		{&withSetter{}, "field withSetter.F: unsupported type envconf.level: "},
		{&withURL{}, "field withURL.F: unsupported type url.URL: "},
		{&withURLPointer{}, "field withURLPointer.F: unsupported type *url.URL: "},
		{&withURLList{}, "field withURLList.F: unsupported type []url.URL: "},
		{&withLocation{}, "field withLocation.F: unsupported type *time.Location: "},
		{&withChan{}, "field withChan.F: unsupported type chan int: "},
	}
	for _, value := range []string{"", "1"} {
		for _, tt := range tests {
			t.Setenv("T_F", value)
			err := Load(tt.dst)
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) || !strings.Contains(err.Error(), "Decode or UnmarshalText") {
				t.Fatalf("T_F=%q: Load(%T) = %v, want %q naming what the reader parses", value, tt.dst, err, tt.want)
			}
		}
	}
}

// Структура со своим разбором, которого нет у ридера, — не секция конфига: без тега — поле без
// тега, а не обход её внутренних полей.
func TestLoad_UntaggedOpaqueStructIsAnUntaggedField(t *testing.T) {
	type withURL struct {
		F url.URL
	}
	type withLocation struct {
		F *time.Location
	}
	for _, dst := range []any{&withURL{}, &withLocation{}} {
		err := Load(dst)
		if err == nil || !strings.HasSuffix(err.Error(), ".F has no envconfig tag: tag it or mark it ignored:\"true\"") {
			t.Fatalf("Load(%T) = %v, want an error naming the untagged field F", dst, err)
		}
	}
}

// Регрессия B1 валидатора A: тег у вложенной структуры не даёт приставку и молча не читал её
// поля — теперь это ошибка с именем поля.
func TestLoad_TaggedNestedStructIsAnError(t *testing.T) {
	type byValue struct {
		DB inner `envconfig:"DB"`
	}
	type byPointer struct {
		DB *inner `envconfig:"DB"`
	}
	for _, dst := range []any{&byValue{}, &byPointer{}} {
		err := Load(dst)
		if err == nil || !strings.Contains(err.Error(), ".DB: envconfig tag on a nested struct") ||
			!strings.Contains(err.Error(), "config.EnvPrefixed") {
			t.Fatalf("Load(%T) = %v, want an error naming the tagged nested struct and the way to a second section", dst, err)
		}
	}
}

func TestLoad_WantsPointerToStruct(t *testing.T) {
	for _, dst := range []any{spec{}, (*spec)(nil), new(int)} {
		if err := Load(dst); err == nil {
			t.Fatalf("Load(%T) = nil, want error", dst)
		}
	}
}
