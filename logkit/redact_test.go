package logkit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
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
		"short_hex":    "9f86d081884c7d659a2feaa0c55ad01",
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
	l.Info(context.Background(), "payload", "plain", body, "mail", []byte("mail to a@b.io"))

	m := decodeLine(t, buf.Bytes())
	plain, _ := m["plain"].(string)
	raw, err := base64.StdEncoding.DecodeString(plain)
	if err != nil || string(raw) != string(body) {
		t.Fatalf("plain body = %v (%v)", m["plain"], err)
	}
	masked, _ := m["mail"].(string)
	raw, err = base64.StdEncoding.DecodeString(masked)
	if err != nil || string(raw) != "mail to [REDACTED]" {
		t.Fatalf("mail body = %q (%v)", raw, err)
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
		"short hex":   "9f86d081884c7d659a2feaa0c55ad01",
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
		"hex token":  "reset 9f86d081884c7d659a2feaa0c55ad015",
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
	// Почти одни hex-символы и длина у порога minRedactHex: такие цепочки
	// первый алфавит не порождает.
	const hexAlphabet = "0123456789abcdefABCDEF-g"
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := range 200_000 {
		alphabet, n := alphabet, 1+rnd.IntN(24)
		if i%16 == 0 {
			alphabet, n = hexAlphabet, minRedactHex-4+rnd.IntN(8)
		}
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
// записи без redact'а — и со структурой, и с http.Header, и со срезом map,
// которые обход ключей разбирает. Отношение двух замеров в одном процессе
// устойчиво к скорости машины, минимум чередующихся раундов — к шуму соседей.
func TestRedact_CostOfRecordWithoutSecrets(t *testing.T) {
	if testing.Short() {
		t.Skip("timing comparison")
	}
	items := make([]map[string]any, 20)
	for i := range items {
		items[i] = map[string]any{"id": i, "name": "item", "price": 10}
	}
	header := http.Header{}
	for _, k := range []string{
		"Accept", "Accept-Encoding", "Accept-Language", "Cache-Control", "Connection",
		"Host", "Pragma", "Referer", "User-Agent", "X-Request-Id",
	} {
		header.Set(k, "v")
	}
	records := map[string][]any{
		"struct": {"doc", map[string]any{"id": "ORD-42", "items": []string{"a", "b"}, "total": 1500}},
		"header": {"headers", header},
		"maps":   {"items", items},
	}
	newLogger := func(opts ...Option) Logger {
		l, err := New(Config{Level: "info"}, append(opts, WithWriter(io.Discard))...)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return l
	}
	plainLog, redactLog := newLogger(), newLogger(WithRedact(RedactConfig{Enabled: true}))
	ctx := context.Background()
	for name, kv := range records {
		cost := func(l Logger) time.Duration {
			start := time.Now()
			for range 500 {
				l.Info(ctx, "record", kv...)
			}
			return time.Since(start)
		}
		plain, redacted := time.Duration(math.MaxInt64), time.Duration(math.MaxInt64)
		for range 7 {
			plain, redacted = min(plain, cost(plainLog)), min(redacted, cost(redactLog))
		}
		ratio := float64(redacted) / float64(plain)
		t.Logf("%s: plain %s, redacted %s (%.2fx)", name, plain/500, redacted/500, ratio)
		if ratio > maxRedactOverhead {
			t.Errorf("%s: redact costs %.1fx the plain path, want <= %.1fx", name, ratio, maxRedactOverhead)
		}
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

// SEC-02 аудита шаблона: без env маскирование включено — mailer, пишущий токен
// сброса пароля ключом token и ссылкой с hex-токеном, в лог его не отдаёт.
func TestRedact_EnabledByDefault(t *testing.T) {
	clearEnv(t, "LOG_REDACT_ENABLED", "LOG_REDACT_PATTERNS", "LOG_REDACT_REPLACE_WITH")
	cfg, err := NewRedactConfig()
	if err != nil {
		t.Fatalf("NewRedactConfig: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("LOG_REDACT_ENABLED must default to true")
	}

	const token = "9f86d081884c7d659a2feaa0c55ad0159f86d081884c7d659a2feaa0c55ad015"
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf, WithRedact(cfg))
	l.Info(context.Background(), "password reset requested",
		"email", "admin@example.com", "token", "opaque-reset-value", "link", "https://app.example/reset?token="+token)

	out := buf.String()
	for _, secret := range []string{"admin@example.com", "opaque-reset-value", token} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked: %s", secret, out)
		}
	}
	if m := decodeLine(t, buf.Bytes()); m["link"] != "https://app.example/reset?token=[REDACTED]" {
		t.Fatalf("link = %v", m["link"])
	}
}

func TestRedact_SecretKeysMaskWholeValue(t *testing.T) {
	secret := []string{
		"token", "access_token", "accessToken", "refresh-token", "oauth2Token", "id_token",
		"password", "new_password", "passwordHash", "passwd", "passphrase",
		"secret", "client_secret", "AWS_SECRET_ACCESS_KEY",
		"authorization", "Authorization", "Proxy-Authorization",
		"cookie", "Set-Cookie", "cookies", "credential", "credentials",
		"api_key", "apiKey", "APIKey", "X-Api-Key", "apikey", "private_key", "privateKey",
		"jwt", "otp", "otp_code", "pin", "totp_uri",
		"accesstoken", "refreshtoken", "newpassword", "clientsecret", "setcookie",
		"pwd", "userPwd", "db_passwd",
	}
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{Patterns: Patterns{`never-matches-\d`}})
	kv := make([]any, 0, len(secret)*2)
	for _, k := range secret {
		kv = append(kv, k, "plain-value-"+k)
	}
	l.Info(context.Background(), "secrets", kv...)

	m := decodeLine(t, buf.Bytes())
	for _, k := range secret {
		if m[k] != "[REDACTED]" {
			t.Errorf("%s = %v, want [REDACTED]", k, m[k])
		}
	}
}

func TestRedact_NonSecretKeysStay(t *testing.T) {
	kept := []string{
		"input_tokens", "tokens", "tokenizer", "key", "cache_key", "idempotency_key",
		"author", "passage", "secretary", "keyboard", "session_id", "auth_method",
		"spin", "pinned", "otpauth", "cwd",
	}
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	kv := make([]any, 0, len(kept)*2)
	for _, k := range kept {
		kv = append(kv, k, "plain-value")
	}
	l.Info(context.Background(), "plain", kv...)

	m := decodeLine(t, buf.Bytes())
	for _, k := range kept {
		if m[k] != "plain-value" {
			t.Errorf("%s = %v, want unchanged", k, m[k])
		}
	}
}

// Под ключом-секретом прячутся строка, группа и структура (включая []byte и
// ключи внутри групп и With), но не числа, bool и nil: token_count — счётчик.
func TestRedact_SecretKeyValueKinds(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	l.With("session_token", "with-value").Info(context.Background(), "kinds",
		slog.Group("credentials", slog.String("user", "u1"), slog.String("pass", "p1")),
		slog.Group("req", slog.String("password", "in-group"), slog.Int("attempt", 2)),
		"cookie", map[string]any{"sid": "abc"},
		"token", []byte("raw-bytes"),
		"token_count", 42,
		"password_set", true,
		"secret", nil,
		"user", credsValuer{},
		"otp", 123456,
		"pin", uint64(4321),
		"totp", 111111,
		"totp_code", 222222,
		"pincode", 3333,
		"otp_attempts", 3,
		"pin_length", 4,
		"otp_ttl_seconds", 300,
	)

	m := decodeLine(t, buf.Bytes())
	for _, k := range []string{"session_token", "credentials", "cookie", "token"} {
		if m[k] != "[REDACTED]" {
			t.Errorf("%s = %v, want [REDACTED]", k, m[k])
		}
	}
	req, _ := m["req"].(map[string]any)
	if req["password"] != "[REDACTED]" || req["attempt"] != float64(2) {
		t.Errorf("req group = %v", m["req"])
	}
	if m["token_count"] != float64(42) || m["password_set"] != true || m["secret"] != nil {
		t.Errorf("numbers, bool and nil must stay: %v / %v / %v", m["token_count"], m["password_set"], m["secret"])
	}
	for _, k := range []string{"otp", "pin", "totp", "totp_code", "pincode"} {
		if m[k] != "[REDACTED]" {
			t.Errorf("%s = %v: a code is a secret even as a number", k, m[k])
		}
	}
	for k, want := range map[string]float64{"otp_attempts": 3, "pin_length": 4, "otp_ttl_seconds": 300} {
		if m[k] != want {
			t.Errorf("%s = %v, want the counter %v", k, m[k], want)
		}
	}
	user, _ := m["user"].(map[string]any)
	if user["login"] != "u1" || user["password"] != "[REDACTED]" {
		t.Errorf("LogValuer group keys must be checked: %v", m["user"])
	}
}

// credsValuer — структура с секретным полем, логируемая группой через LogValuer.
type credsValuer struct{}

func (credsValuer) LogValue() slog.Value {
	return slog.GroupValue(slog.String("login", "u1"), slog.String("password", "p1"))
}

func TestRedact_HexChains(t *testing.T) {
	const (
		md5    = "9f86d081884c7d659a2feaa0c55ad015"
		sha256 = "9F86D081884C7D659A2FEAA0C55AD0159F86D081884C7D659A2FEAA0C55AD015"
		short  = "9f86d081884c7d659a2feaa0c55ad01"
	)
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	l.Info(context.Background(), "verify "+sha256, "digest", md5, "short", short, "doc", map[string]any{"ref": md5})

	m := decodeLine(t, buf.Bytes())
	if m["msg"] != "verify [REDACTED]" || m["digest"] != "[REDACTED]" {
		t.Errorf("hex chains >= 32 must be masked: msg=%v digest=%v", m["msg"], m["digest"])
	}
	if doc, _ := m["doc"].(map[string]any); doc["ref"] != "[REDACTED]" {
		t.Errorf("hex chain inside a struct must be masked: %v", m["doc"])
	}
	if m["short"] != short {
		t.Errorf("hex chain < 32 must stay: %v", m["short"])
	}
}

// Ключи у map со строковыми ключами — http.Header, url.Values, map[string]any и
// вложенные map — проверяются так же, как ключи атрибутов.
func TestRedact_MapKeys(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	l.Info(context.Background(), "request",
		"headers", http.Header{"Cookie": {"session=opaque-cookie"}, "Accept": {"text/html"}},
		"form", url.Values{"token": {"opaque-token"}, "page": {"2"}},
		"body", map[string]any{
			"password": "opaque-password",
			"user":     map[string]any{"pwd": "opaque-pwd", "name": "alice"},
			"count":    3,
		},
		"plain", map[string]string{"name": "bob"},
	)

	out := buf.String()
	for _, secret := range []string{"opaque-cookie", "opaque-token", "opaque-password", "opaque-pwd"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked: %s", secret, out)
		}
	}
	m := decodeLine(t, buf.Bytes())
	headers, _ := m["headers"].(map[string]any)
	if accept, _ := headers["Accept"].([]any); headers["Cookie"] != "[REDACTED]" || len(accept) != 1 || accept[0] != "text/html" {
		t.Errorf("headers = %v", m["headers"])
	}
	form, _ := m["form"].(map[string]any)
	if page, _ := form["page"].([]any); form["token"] != "[REDACTED]" || len(page) != 1 || page[0] != "2" {
		t.Errorf("form = %v", m["form"])
	}
	body, _ := m["body"].(map[string]any)
	user, _ := body["user"].(map[string]any)
	if body["password"] != "[REDACTED]" || user["pwd"] != "[REDACTED]" || user["name"] != "alice" || body["count"] != float64(3) {
		t.Errorf("body = %v", m["body"])
	}
	if plain, _ := m["plain"].(map[string]any); plain["name"] != "bob" {
		t.Errorf("plain = %v", m["plain"])
	}
}

// Регрессия: обход ключей map помнит пройденные map — map, ссылающийся на
// себя, не уводит его в бесконечную рекурсию (fatal stack overflow, recover не
// спасает). Цикл отвергает JSON, как без маскирования; секрет не утекает.
func TestRedact_CyclicMap(t *testing.T) {
	plain := map[string]any{"name": "loop"}
	plain["self"] = plain
	secret := map[string]any{"password": "opaque-password"}
	secret["self"] = secret
	slice := []any{nil, map[string]any{"password": "opaque-password"}}
	slice[0] = slice

	for name, doc := range map[string]any{"plain": plain, "secret": secret, "slice": slice} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			l := newRedacted(t, &buf, RedactConfig{})
			l.Info(context.Background(), "cyclic", "doc", doc, "id", "ORD-42")

			if strings.Contains(buf.String(), "opaque-password") {
				t.Fatalf("secret leaked: %s", buf.String())
			}
			m := decodeLine(t, buf.Bytes())
			if got, _ := m["doc"].(string); !strings.Contains(got, "cycle") {
				t.Errorf("doc = %v, want the JSON cycle error", m["doc"])
			}
			if m["id"] != "ORD-42" {
				t.Errorf("record broken by a cyclic map: %v", m)
			}
		})
	}
}

// Ключи map проверяются и там, где секрет лежит только во вложенной map, в
// срезе, подсрезе или массиве map; прочие значения не трогаются.
func TestRedact_MapKeysInContainers(t *testing.T) {
	base := []any{map[string]any{"n": 1}, map[string]any{"password": "opaque-sub"}}
	var nilMap map[string]any
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	l.Info(context.Background(), "containers",
		"typed", map[string]map[string]string{"db": {"passwd": "opaque-typed", "host": "db1"}},
		"nested", map[string]any{"cfg": map[string]any{"token": "opaque-nested"}, "name": "svc"},
		"list", []map[string]any{{"password": "opaque-list", "n": 1}},
		"anys", []any{"x", map[string]any{"secret": "opaque-any"}},
		"array", [1]map[string]string{{"pwd": "opaque-array"}},
		"subs", []any{base[:1], base},
		"nils", map[string]any{"token": "opaque-nils", "m": nilMap, "p": (*int)(nil)},
		"words", []string{"password", "token"},
	)

	out := buf.String()
	for _, secret := range []string{"opaque-typed", "opaque-nested", "opaque-list", "opaque-any", "opaque-array", "opaque-sub", "opaque-nils"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked: %s", secret, out)
		}
	}
	m := decodeLine(t, buf.Bytes())
	typed, _ := m["typed"].(map[string]any)
	if db, _ := typed["db"].(map[string]any); db["passwd"] != "[REDACTED]" || db["host"] != "db1" {
		t.Errorf("typed = %v", m["typed"])
	}
	nested, _ := m["nested"].(map[string]any)
	if cfg, _ := nested["cfg"].(map[string]any); cfg["token"] != "[REDACTED]" || nested["name"] != "svc" {
		t.Errorf("nested = %v", m["nested"])
	}
	if list, _ := m["list"].([]any); len(list) != 1 || list[0].(map[string]any)["n"] != float64(1) {
		t.Errorf("list = %v", m["list"])
	}
	if anys, _ := m["anys"].([]any); len(anys) != 2 || anys[0] != "x" {
		t.Errorf("anys = %v", m["anys"])
	}
	if nils, _ := m["nils"].(map[string]any); nils["m"] != nil || nils["p"] != nil {
		t.Errorf("nil values must stay null: %v", m["nils"])
	}
	if words, _ := m["words"].([]any); len(words) != 2 || words[0] != "password" {
		t.Errorf("a slice of strings is not a map: %v", m["words"])
	}
}

// hiddenCreds прячет пароль в MarshalJSON на указателе — тип сам решает, что показать.
type hiddenCreds struct{ User, Pass string }

func (c *hiddenCreds) MarshalJSON() ([]byte, error) { return []byte(`{"user":"` + c.User + `"}`), nil }

type account struct{ Creds hiddenCreds }

// ownJSON и ownJSONPtr кодируются своим MarshalJSON — на значении и на указателе.
type ownJSON map[string]string

func (ownJSON) MarshalJSON() ([]byte, error) { return []byte(`"own"`), nil }

type ownJSONPtr map[string]string

func (*ownJSONPtr) MarshalJSON() ([]byte, error) { return []byte(`"own-ptr"`), nil }

// Регрессия R2: копия с маскированием кодируется в тот же JSON, что оригинал.
// Указатель не разыменовывается — MarshalJSON на указателе у поля зовётся, как
// без маскирования; типы со своим MarshalJSON не разбираются.
func TestRedact_MapKeysKeepTypeEncoding(t *testing.T) {
	var buf bytes.Buffer
	l := newRedacted(t, &buf, RedactConfig{})
	l.Info(context.Background(), "types",
		"doc", map[string]any{
			"token": "opaque-token",
			"acct":  &account{Creds: hiddenCreds{User: "u", Pass: "hidden-pass"}},
			"own":   ownJSON{"password": "own-secret"},
		},
		"list", []ownJSONPtr{{"password": "own-ptr-secret"}},
	)

	out := buf.String()
	for _, leaked := range []string{"opaque-token", "hidden-pass"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("%q leaked: %s", leaked, out)
		}
	}
	m := decodeLine(t, buf.Bytes())
	doc, _ := m["doc"].(map[string]any)
	acct, _ := doc["acct"].(map[string]any)
	if creds, _ := acct["Creds"].(map[string]any); creds["user"] != "u" || doc["own"] != "own" {
		t.Errorf("doc = %v, want the types' own encoding", m["doc"])
	}
	if list, _ := m["list"].([]any); len(list) != 1 || list[0] != "own-ptr" {
		t.Errorf("list = %v, want MarshalJSON of addressable elements", m["list"])
	}
}

// Регрессия R2: копия, которую JSON не кодирует, уходит в запасной путь сама —
// text-обработчик печатает её, а не исходник с секретом.
func TestRedact_FallbackKeepsMasking(t *testing.T) {
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug", Format: "text"}, &buf, WithRedact(RedactConfig{Enabled: true}))
	l.Info(context.Background(), "unencodable", "doc", map[string]any{"password": "opaque-fallback", "ch": make(chan int)})

	if out := buf.String(); strings.Contains(out, "opaque-fallback") || !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("fallback must keep the masked copy: %s", out)
	}
}

// Регрессия: pre-filter ключей — необходимое условие секрета по ключу. Ключ,
// который classifyKey считает секретом, обязан пройти keyHint, иначе обход
// ключей пропустит его, и значение уйдёт в лог открытым.
func TestRedact_KeyHintNeverMissesASecretKey(t *testing.T) {
	fragments := []string{
		"token", "Token", "TOKEN", "password", "Passwd", "pwd", "passphrase", "secret", "Secret",
		"authorization", "cookie", "Cookies", "credential", "jwt", "otp", "TOTP", "pin", "Pin", "code",
		"api", "API", "Api", "key", "Key", "private", "access", "new", "id", "count", "value",
		"_", "-", ".", " ", "2", "x", "s", "e", "r", "p", "o", "t", "k", "n",
	}
	rnd := rand.New(rand.NewPCG(3, 4))
	for i := range 200_000 {
		var sb strings.Builder
		for range 1 + rnd.IntN(5) {
			sb.WriteString(fragments[rnd.IntN(len(fragments))])
		}
		key := sb.String()
		if classifyKey(key) != keyPlain && !keyHint([]byte(key)) {
			t.Fatalf("case %d: key %q is a secret, but keyHint skips it", i, key)
		}
	}
}
