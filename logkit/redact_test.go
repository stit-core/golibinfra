package logkit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// maxRedactOverhead — во сколько раз запись без маркеров секрета может
// подорожать от включённого redact'а. Потолок держит pre-filter: без него
// прогонка объединённого regexp'а стоит около восьми таких записей.
const maxRedactOverhead = 3.0

func newRedacted(t *testing.T, buf *bytes.Buffer, cfg RedactConfig) Logger {
	t.Helper()
	cfg.Enabled = true
	return mustLogger(t, Config{Level: "debug"}, buf, WithRedact(cfg))
}

func TestRedact_AttrsAndMsg(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})

	l.Info(
		context.Background(), "login john.doe@example.com from +79161234567",
		"email", "john.doe@example.com",
		"phone", "+79161234567",
		"card", "4111 1111 1111 1111",
		"order_id", "ORD-42",
	)

	m := decodeLine(t, buf.Bytes())
	if m["msg"] != "login [REDACTED] from [REDACTED]" {
		t.Errorf("msg = %q", m["msg"])
	}
	if m["level"] != "INFO" {
		t.Errorf("level = %v (record must not be re-encoded)", m["level"])
	}
	for _, k := range []string{"email", "phone", "card"} {
		if m[k] != "[REDACTED]" {
			t.Errorf("%s = %v, want [REDACTED]", k, m[k])
		}
	}
	if m["order_id"] != "ORD-42" {
		t.Errorf("order_id = %v, must stay intact", m["order_id"])
	}
}

func TestRedact_DefaultPatternsCoverSecrets(t *testing.T) {
	cases := map[string]string{
		"bearer": "authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.SflKxwRJSMeKKF2QT4",
		"basic":  "authorization: Basic dXNlcjpwYXNzd29yZA==",
		"jwt":    "token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NSJ9.dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk rejected",
		"antkey": "key sk-ant-api03-AbCdEfGhIjKlMnOpQrSt-uvw",
		"apikey": "key sk-AbCdEfGhIjKlMnOpQrStUvWx0123",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			l := newRedacted(t, &buf, RedactConfig{})
			l.Info(context.Background(), in, "err", errors.New(in))

			m := decodeLine(t, buf.Bytes())
			msg, _ := m["msg"].(string)
			errText, _ := m["err"].(string)
			if !strings.Contains(msg, "[REDACTED]") || !strings.Contains(errText, "[REDACTED]") {
				t.Fatalf("secret not masked: msg=%q err=%q", msg, errText)
			}
			for _, secret := range []string{"eyJzdWIi", "dXNlcjpwYXNzd29yZA", "sk-ant-api03", "AbCdEfGhIjKlMnOpQrStUvWx0123"} {
				if strings.Contains(msg+errText, secret) {
					t.Errorf("secret fragment %q survived: %q / %q", secret, msg, errText)
				}
			}
		})
	}
}

func TestRedact_NoFalsePositivesOnPlainText(t *testing.T) {
	kept := map[string]string{
		"prose":        "the bearing of the ship changed, base64 basics apply",
		"uuid":         "550e8400-e29b-41d4-a716-446655440000",
		"short_base64": "aGVsbG8gd29ybGQ=",
		"id":           "ORD-42-sk-1",
		"path":         "/api/v1/customers/123456",
		"ten_digits":   "1234567890",
		"hash":         "9f86d081884c7d659a2feaa0c55ad015",
	}
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	kv := make([]any, 0, len(kept)*2)
	for k, v := range kept {
		kv = append(kv, k, v)
	}
	l.Info(context.Background(), "plain text", kv...)

	m := decodeLine(t, buf.Bytes())
	for k, want := range kept {
		if m[k] != want {
			t.Errorf("%s = %v, want unchanged %q", k, m[k], want)
		}
	}
}

func TestRedact_NumbersStayIntact(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})

	l.Info(
		context.Background(), "timing",
		"elapsed", 1234567890123*time.Nanosecond,
		"unix_ms", int64(1725364800000),
		"ratio", 0.5,
		"ok", true,
	)

	m := decodeLine(t, buf.Bytes())
	if m["elapsed"] != float64(1234567890123) {
		t.Errorf("elapsed = %v (%T), want numeric 1234567890123", m["elapsed"], m["elapsed"])
	}
	if m["unix_ms"] != float64(1725364800000) {
		t.Errorf("unix_ms = %v, want numeric 1725364800000", m["unix_ms"])
	}
	if m["ratio"] != 0.5 || m["ok"] != true {
		t.Errorf("ratio/ok changed: %v / %v", m["ratio"], m["ok"])
	}
}

func TestRedact_CoversWithAttrsAndGroups(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})

	l.With("owner", "owner@example.com").Info(
		context.Background(), "grouped",
		"contact", map[string]any{"email": "nested@example.com", "age": 33},
	)

	m := decodeLine(t, buf.Bytes())
	if m["owner"] != "[REDACTED]" {
		t.Errorf("With() attr not redacted: %v", m["owner"])
	}
	contact, _ := m["contact"].(map[string]any)
	if contact["email"] != "[REDACTED]" {
		t.Errorf("nested email not redacted: %v", m["contact"])
	}
	if contact["age"] != float64(33) {
		t.Errorf("nested number changed: %v", contact["age"])
	}
}

func TestRedact_SlogGroupValueAndLogValuer(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	l.Info(context.Background(), "attrs",
		slog.Group("user", slog.String("email", "in-group@example.com"), slog.Int("age", 30)),
		"resolved", emailValuer{},
	)

	m := decodeLine(t, buf.Bytes())
	user, _ := m["user"].(map[string]any)
	if user["email"] != "[REDACTED]" {
		t.Errorf("slog.Group value not redacted: %v", m["user"])
	}
	if user["age"] != float64(30) {
		t.Errorf("group number changed: %v", user["age"])
	}
	if m["resolved"] != "[REDACTED]" {
		t.Errorf("LogValuer result not redacted: %v", m["resolved"])
	}
}

type emailValuer struct{}

func (emailValuer) LogValue() slog.Value { return slog.StringValue("valuer@example.com") }

func TestRedact_SlogGroupChainAndError(t *testing.T) {
	var buf bytes.Buffer
	sl := mustSlog(t, Config{Level: "debug"}, &buf, WithRedact(RedactConfig{Enabled: true}))
	sl.WithGroup("req").With("user", "u@example.com").Info(
		"done",
		"err", errors.New("mail to x@example.com failed"),
	)

	m := decodeLine(t, buf.Bytes())
	req, _ := m["req"].(map[string]any)
	if req["user"] != "[REDACTED]" {
		t.Errorf("group With attr not redacted: %v", m)
	}
	if req["err"] != "mail to [REDACTED] failed" {
		t.Errorf("error text not redacted: %v", req["err"])
	}
}

func TestRedact_StructValueStaysJSON(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	l.Info(context.Background(), "doc",
		"masked", map[string]any{"email": "a@b.io", "n": 1},
		"intact", map[string]any{"id": "ORD-42", "n": 2},
		"raw", json.RawMessage(`{"email":"c@d.io"}`),
		"html", map[string]any{"tag": "<b>&</b>"},
	)

	m := decodeLine(t, buf.Bytes())
	masked, _ := m["masked"].(map[string]any)
	if masked["email"] != "[REDACTED]" || masked["n"] != float64(1) {
		t.Errorf("masked struct = %v", m["masked"])
	}
	intact, _ := m["intact"].(map[string]any)
	if intact["id"] != "ORD-42" {
		t.Errorf("untouched struct must stay an object: %v", m["intact"])
	}
	raw, _ := m["raw"].(map[string]any)
	if raw["email"] != "[REDACTED]" {
		t.Errorf("json.RawMessage not redacted: %v", m["raw"])
	}
	html, _ := m["html"].(map[string]any)
	if html["tag"] != "<b>&</b>" {
		t.Errorf("HTML escaping must match the untouched representation: %v", m["html"])
	}
}

func TestRedact_BrokenJSONFallsBackToString(t *testing.T) {
	var buf bytes.Buffer
	// Жадный паттерн проходит через кавычку и рвёт JSON-объект.
	l := newRedacted(t, &buf, RedactConfig{Patterns: Patterns{`secret-.+`}})
	l.Info(context.Background(), "doc", "doc", map[string]any{"k": "secret-abc", "n": 1})

	m := decodeLine(t, buf.Bytes())
	got, ok := m["doc"].(string)
	if !ok {
		t.Fatalf("broken JSON must degrade to a string, got %T %v", m["doc"], m["doc"])
	}
	if strings.Contains(got, "!ERROR") || strings.Contains(got, "secret-abc") {
		t.Fatalf("doc = %q", got)
	}
}

func TestRedact_BytesValue(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{Patterns: Patterns{`secret-\w+`}})
	l.Info(context.Background(), "payload", "body", []byte("token secret-abc"))

	m := decodeLine(t, buf.Bytes())
	enc, _ := m["body"].(string)
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("[]byte must stay base64-encoded bytes: %v (%v)", m["body"], err)
	}
	if string(raw) != "token [REDACTED]" {
		t.Errorf("body = %q", raw)
	}
}

// Pre-filter дефолтного набора работает и по []byte: содержимое без маркеров
// секрета доходит до лога нетронутым и не стоит прогонки regexp'а.
func TestRedact_BytesValueWithDefaultSet(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	body := []byte(`{"status":"ok"}`)
	l.Info(context.Background(), "payload", "plain", body, "secret", []byte("mail to a@b.io"))

	m := decodeLine(t, buf.Bytes())
	plain, _ := m["plain"].(string)
	raw, err := base64.StdEncoding.DecodeString(plain)
	if err != nil || string(raw) != string(body) {
		t.Fatalf("plain body = %v (%v)", m["plain"], err)
	}
	masked, _ := m["secret"].(string)
	raw, err = base64.StdEncoding.DecodeString(masked)
	if err != nil || string(raw) != "mail to [REDACTED]" {
		t.Fatalf("secret body = %q (%v)", raw, err)
	}

	// Цепочка цифр проходит pre-filter, но не совпадает ни с одной маской.
	buf.Reset()
	l.Info(context.Background(), "payload", "uuid", []byte("550e8400-e29b-41d4-a716-446655440000"))
	uuid, _ := decodeLine(t, buf.Bytes())["uuid"].(string)
	raw, err = base64.StdEncoding.DecodeString(uuid)
	if err != nil || string(raw) != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("uuid body = %q (%v)", raw, err)
	}
}

func TestRedact_UnmarshalableValueSurvives(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	l.Info(context.Background(), "payload", "ch", make(chan int), "nil", nil, "id", "ORD-42")

	m := decodeLine(t, buf.Bytes())
	if m["id"] != "ORD-42" {
		t.Fatalf("record broken by an unmarshalable value: %v", m)
	}
}

func TestRedact_CustomPatternsAndReplacement(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{Patterns: Patterns{`secret-\w+`}, ReplaceWith: "***"})

	l.Info(context.Background(), "token secret-abc", "email", "kept@example.com")

	m := decodeLine(t, buf.Bytes())
	if m["msg"] != "token ***" {
		t.Errorf("msg = %v", m["msg"])
	}
	if m["email"] != "kept@example.com" {
		t.Errorf("default patterns must not apply when custom ones are set: %v", m["email"])
	}
}

func TestRedact_Disabled(t *testing.T) {
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf, WithRedact(RedactConfig{Enabled: false}))
	l.Info(context.Background(), "user login", "email", "john.doe@example.com")

	if m := decodeLine(t, buf.Bytes()); m["email"] != "john.doe@example.com" {
		t.Errorf("email must not be redacted when disabled: %v", m["email"])
	}
}

func TestRedact_InvalidConfigIsError(t *testing.T) {
	cases := map[string]RedactConfig{
		"pattern":     {Enabled: true, Patterns: Patterns{`(`}},
		"replacement": {Enabled: true, ReplaceWith: `"`},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Config{}, WithWriter(io.Discard), WithRedact(cfg)); !errors.Is(err, ErrConfig) {
				t.Fatalf("err = %v, want ErrConfig", err)
			}
		})
	}
}

func TestNewRedactHandler_NilInner(t *testing.T) {
	if _, err := NewRedactHandler(nil, RedactConfig{Enabled: true}); err == nil {
		t.Fatal("nil inner handler must be rejected")
	}
}

// Дефолтный набор проходит pre-filter: запись без маркеров секрета не доходит
// до regexp'а, цена которого — O(длина × размер программы).
func TestRedact_DefaultHintSkipsRecordsWithoutSecrets(t *testing.T) {
	skipped := map[string]string{
		"http log":    `GET /v1/orders/42 handled in 12ms upstream=payments status=200`,
		"json doc":    `{"id":"ORD-42","items":["a","b"],"total":1500}`,
		"prose":       "the bearing of the ship changed",
		"hash":        "9f86d081884c7d659a2feaa0c55ad015",
		"stack frame": "logkit.(*slogLogger).Info(0xc000112000)",
	}
	for name, in := range skipped {
		t.Run(name, func(t *testing.T) {
			if defaultHint(in) {
				t.Fatalf("hint must skip the regexp for %q", in)
			}
		})
	}

	// Цепочка от minRedactDigits цифр доходит до regexp'а, даже если он её не
	// маскирует: короче маску телефона и карты не отличить по одним байтам.
	scanned := map[string]string{
		"email":      "login john@example.com",
		"phone":      "call +79161234567",
		"card":       "4111 1111 1111 1111",
		"bearer":     "authorization: BEARER abcdef",
		"basic":      "authorization: basic dXNlcjpwYXNz",
		"jwt":        "token eyJhbGciOi",
		"apikey":     "key sk-ant-api03",
		"digits run": "550e8400-e29b-41d4-a716-446655440000",
	}
	for name, in := range scanned {
		t.Run(name, func(t *testing.T) {
			if !defaultHint(in) {
				t.Fatalf("hint must let %q through to the regexp", in)
			}
		})
	}
}

// Регрессия: pre-filter — необходимое условие совпадения, а не эвристика.
// Пропущенная им строка не должна совпадать с объединённым regexp'ом ни при
// каких входных данных, иначе секрет тихо уйдёт в лог незамаскированным.
func TestRedact_DefaultHintNeverMissesAMatch(t *testing.T) {
	rules, err := compileRedact(RedactConfig{})
	if err != nil {
		t.Fatalf("compileRedact: %v", err)
	}
	if !rules.hint {
		t.Fatal("the default set must enable the pre-filter")
	}

	const alphabet = "aAbBeEsSkK0123456789@+-. _/=:jJyY\"{}"
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := range 200_000 {
		n := 1 + rnd.IntN(24)
		var sb strings.Builder
		for range n {
			sb.WriteByte(alphabet[rnd.IntN(len(alphabet))])
		}
		s := sb.String()
		if !defaultHint(s) && rules.re.MatchString(s) {
			t.Fatalf("case %d: hint skipped %q, but the pattern set matches it", i, s)
		}
		if b := []byte(s); defaultHint(s) != defaultHint(b) {
			t.Fatalf("case %d: hint disagrees between string and []byte for %q", i, s)
		}
	}
}

// Свой набор паттернов прогоняется regexp'ом без pre-filter'а: он выведен из
// дефолтных масок и о чужих ничего не знает.
func TestRedact_CustomPatternsSkipTheHint(t *testing.T) {
	rules, err := compileRedact(RedactConfig{Patterns: Patterns{`secret-\w+`}})
	if err != nil {
		t.Fatalf("compileRedact: %v", err)
	}
	if rules.hint {
		t.Fatal("the pre-filter must stay off for custom patterns")
	}

	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{Patterns: Patterns{`secret-\w+`}})
	l.Info(context.Background(), "value secret-xyz")
	if m := decodeLine(t, buf.Bytes()); m["msg"] != "value [REDACTED]" {
		t.Fatalf("msg = %v", m["msg"])
	}
}

// Порог цены: запись без маркеров секрета не должна стоить кратно дороже той же
// записи без redact'а. Сравниваются два прогона в одном процессе — отношение
// устойчиво к скорости машины, в отличие от абсолютных наносекунд.
func TestRedact_CostOfRecordWithoutSecrets(t *testing.T) {
	if testing.Short() {
		t.Skip("timing comparison")
	}
	doc := map[string]any{"id": "ORD-42", "items": []string{"a", "b"}, "total": 1500}
	measure := func(opts ...Option) time.Duration {
		l, err := New(Config{Level: "info"}, append(opts, WithWriter(io.Discard))...)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ctx := context.Background()
		res := testing.Benchmark(func(b *testing.B) {
			for b.Loop() {
				l.Info(ctx, "order processed", "doc", doc)
			}
		})
		return time.Duration(res.NsPerOp())
	}

	plain := measure()
	redacted := measure(WithRedact(RedactConfig{Enabled: true}))
	t.Logf("plain %s, redacted %s (%.2fx)", plain, redacted, float64(redacted)/float64(plain))
	if ratio := float64(redacted) / float64(plain); ratio > maxRedactOverhead {
		t.Fatalf("redact costs %.1fx the plain path (%s vs %s), want <= %.1fx",
			ratio, redacted, plain, maxRedactOverhead)
	}
}

func TestRedact_PhonePatternIgnoresPlainNumbers(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})

	l.Info(context.Background(), "ids", "ten_digits", "1234567890", "ru_phone", "89161234567", "e164", "+441234567890")

	m := decodeLine(t, buf.Bytes())
	if m["ten_digits"] != "1234567890" {
		t.Errorf("plain 10-digit id must stay: %v", m["ten_digits"])
	}
	if m["ru_phone"] != "[REDACTED]" || m["e164"] != "[REDACTED]" {
		t.Errorf("phones must be redacted: %v / %v", m["ru_phone"], m["e164"])
	}
}
