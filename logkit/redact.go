package logkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode"
)

const defaultReplaceWith = "[REDACTED]"

// Дефолтный набор — эвристики компактных форм PII и очевидных секретов.
// Порядок важен: «Bearer <jwt>» должен схлопываться целиком, поэтому схемы
// авторизации стоят раньше JWT.
var defaultRedactPatterns = Patterns{
	`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`,
	`\+\d{10,15}\b|\b[78]\d{10}\b`,
	`\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`,
	`(?i)\bbearer\s+[A-Za-z0-9\-._~+/]+=*`,
	`(?i)\bbasic\s+[A-Za-z0-9+/]{8,}={0,2}`,
	`\beyJ[A-Za-z0-9_-]{6,}\.eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]+`,
	`\bsk-ant-[A-Za-z0-9_-]{16,}`,
	`\bsk-[A-Za-z0-9]{20,}\b`,
	`[0-9A-Fa-f]{32,}`,
}

// secretKeyWords — слова ключа атрибута, значение под которым маскируется
// целиком; составные api_key и private_key ловит secretKey по паре слов.
var secretKeyWords = [...]string{
	"apikey", "authorization", "cookie", "cookies", "credential", "credentials", "jwt",
	"otp", "passphrase", "passwd", "password", "privatekey", "secret", "token",
}

type redactHandler struct {
	inner slog.Handler
	rules redactRules
	with  string
}

// redactRules — скомпилированный набор масок.
type redactRules struct {
	re *regexp.Regexp
	// hint включён только для дефолтного набора: pre-filter выведен из него.
	hint bool
}

// NewRedactHandler маскирует msg и строковые значения атрибутов (включая
// With()-attrs, группы, LogValuer, error и JSON-представление Any-значений), а
// значение под ключом-секретом (token, password, secret, authorization, cookie,
// …) — целиком; числа, bool, time и duration не трогает.
func NewRedactHandler(inner slog.Handler, cfg RedactConfig) (slog.Handler, error) {
	if inner == nil {
		return nil, errors.New("logkit: NewRedactHandler: nil inner handler")
	}
	rules, err := compileRedact(cfg)
	if err != nil {
		return nil, err
	}
	with := cfg.ReplaceWith
	if with == "" {
		with = defaultReplaceWith
	}
	return &redactHandler{inner: inner, rules: rules, with: with}, nil
}

// compileRedact собирает один альтернативный regexp: одна прогонка по строке
// вместо N. Замена — литеральная, поэтому «$» в ReplaceWith безопасен.
func compileRedact(cfg RedactConfig) (redactRules, error) {
	with := cfg.ReplaceWith
	if with == "" {
		with = defaultReplaceWith
	}
	for _, r := range with {
		if r == '"' || r == '\\' || unicode.IsControl(r) {
			return redactRules{}, fmt.Errorf(
				"%w: LOG_REDACT_REPLACE_WITH %q: %q breaks the JSON encoding of redacted values",
				ErrConfig, with, string(r),
			)
		}
	}

	patterns := cfg.Patterns
	hint := len(patterns) == 0
	if hint {
		patterns = defaultRedactPatterns
	}
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		sub, err := regexp.Compile(p)
		if err != nil {
			return redactRules{}, fmt.Errorf("%w: LOG_REDACT_PATTERNS %q: %w", ErrConfig, p, err)
		}
		// Совпадение с пустой строкой вставляет замену между каждой парой
		// символов: нечитаемым становится весь лог, а не одно значение.
		if sub.MatchString("") {
			return redactRules{}, fmt.Errorf("%w: LOG_REDACT_PATTERNS %q: pattern matches the empty string", ErrConfig, p)
		}
		parts = append(parts, "(?:"+p+")")
	}
	re, err := regexp.Compile(strings.Join(parts, "|"))
	if err != nil {
		return redactRules{}, fmt.Errorf("%w: LOG_REDACT_PATTERNS %q: %w", ErrConfig, patterns, err)
	}
	return redactRules{re: re, hint: hint}, nil
}

// minRedactDigits — сколько цифр подряд нужно, чтобы совпал самый короткий из
// «цифровых» дефолтов (телефон — 11, карта — 16 с разделителями).
const minRedactDigits = 11

// minRedactHex — длина hex-цепочки, которую маскирует дефолтный набор: токен
// из 16 случайных байт, MD5, SHA-1 и SHA-256 в hex.
const minRedactHex = 32

// defaultHint — необходимое условие совпадения дефолтного набора: у каждой
// маски есть обязательный литерал («@», «+», «bearer», «basic», «eyJ», «sk-»),
// цепочка минимум из minRedactDigits цифр либо из minRedactHex hex-символов.
// Скан по байтам дешевле прогонки regexp'а на порядки: её цена — O(длина ×
// размер программы), а программа объединённого набора — больше двух сотен
// инструкций.
func defaultHint[T string | []byte](s T) bool {
	run, hex, sep := 0, 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isHexDigit(c) {
			if hex++; hex >= minRedactHex {
				return true
			}
		} else {
			hex = 0
		}
		switch {
		case c == '@' || c == '+':
			return true
		case isDigit(c):
			run++
			sep = false
			if run >= minRedactDigits {
				return true
			}
			continue
		case numSep(c) && run > 0 && !sep:
			sep = true
			continue
		case c == 'b' || c == 'B':
			if foldPrefix(s[i:], "bearer") || foldPrefix(s[i:], "basic") {
				return true
			}
		case c == 'e':
			if literalPrefix(s[i:], "eyJ") {
				return true
			}
		case c == 's':
			if literalPrefix(s[i:], "sk-") {
				return true
			}
		}
		run, sep = 0, false
	}
	return false
}

func isHexDigit(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// numSep — разделитель между группами цифр номера карты; надмножество «\s»
// и «-» из маски, чтобы цепочка не рвалась раньше, чем у regexp'а.
func numSep(c byte) bool {
	return c == '-' || c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

// foldPrefix сравнивает начало s с ASCII-строкой want без учёта регистра;
// want — только строчные буквы.
func foldPrefix[T string | []byte](s T, want string) bool {
	if len(s) < len(want) {
		return false
	}
	for i := 0; i < len(want); i++ {
		if s[i]|0x20 != want[i] {
			return false
		}
	}
	return true
}

func literalPrefix[T string | []byte](s T, want string) bool {
	if len(s) < len(want) {
		return false
	}
	for i := 0; i < len(want); i++ {
		if s[i] != want[i] {
			return false
		}
	}
	return true
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.text(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = h.attr(a)
	}
	return &redactHandler{inner: h.inner.WithAttrs(out), rules: h.rules, with: h.with}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name), rules: h.rules, with: h.with}
}

func (h *redactHandler) text(s string) string {
	if h.rules.hint && !defaultHint(s) {
		return s
	}
	if !h.rules.re.MatchString(s) {
		return s
	}
	return h.rules.re.ReplaceAllLiteralString(s, h.with)
}

func (h *redactHandler) bytes(b []byte) []byte {
	if h.rules.hint && !defaultHint(b) {
		return b
	}
	if !h.rules.re.Match(b) {
		return b
	}
	return h.rules.re.ReplaceAllLiteral(b, []byte(h.with))
}

func (h *redactHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if secretKey(a.Key) && maskable(v) {
		return slog.String(a.Key, h.with)
	}
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.text(v.String()))
	case slog.KindGroup:
		in := v.Group()
		out := make([]slog.Attr, len(in))
		for i, g := range in {
			out[i] = h.attr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		return h.anyAttr(a.Key, v.Any())
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}

// maskable: под секретным ключом прячутся строки, группы и структуры; числа,
// bool, время и nil секрета не несут (token_count — счётчик).
func maskable(v slog.Value) bool {
	switch v.Kind() {
	case slog.KindString, slog.KindGroup:
		return true
	case slog.KindAny:
		return v.Any() != nil
	default:
		return false
	}
}

// secretKey: ключ режется на слова по разделителям, границам camelCase и цифр
// («X-Api-Key», «accessToken», «oauth2Token»), и слово ищется в secretKeyWords.
// По словам, а не подстрокой: «input_tokens» — счётчик, а не секрет.
func secretKey(key string) bool {
	var prev string
	for i := 0; i < len(key); {
		var w string
		if w, i = nextKeyWord(key, i); w == "" {
			return false
		}
		for _, sw := range secretKeyWords {
			if strings.EqualFold(w, sw) {
				return true
			}
		}
		if strings.EqualFold(w, "key") && (strings.EqualFold(prev, "api") || strings.EqualFold(prev, "private")) {
			return true
		}
		prev = w
	}
	return false
}

// nextKeyWord отдаёт слово ключа с позиции from и позицию за ним.
func nextKeyWord(key string, from int) (string, int) {
	for from < len(key) && !isAlnum(key[from]) {
		from++
	}
	i := from
	for i < len(key) && isAlnum(key[i]) && (i == from || !keyWordBoundary(key, i)) {
		i++
	}
	return key[from:i], i
}

// keyWordBoundary: граница перед key[i] — цифра↔буква, строчная→заглавная и
// конец аббревиатуры перед словом («APIKey» → API, Key).
func keyWordBoundary(key string, i int) bool {
	p, c := key[i-1], key[i]
	switch {
	case isDigit(p) != isDigit(c):
		return true
	case isLower(p) && isUpper(c):
		return true
	default:
		return isUpper(p) && isUpper(c) && i+1 < len(key) && isLower(key[i+1])
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
func isAlnum(c byte) bool { return isDigit(c) || isLower(c) || isUpper(c) }

// anyAttr: error — как строка (так его рендерит и JSONHandler); прочее —
// маршалится один раз, дальше по конвейеру идёт уже готовый JSON.
func (h *redactHandler) anyAttr(key string, x any) slog.Attr {
	switch t := x.(type) {
	case nil:
		return slog.Any(key, x)
	case json.RawMessage:
		return h.jsonAttr(key, t)
	case []byte:
		return slog.Any(key, h.bytes(t))
	case error:
		if _, isMarshaler := x.(json.Marshaler); !isMarshaler {
			return slog.String(key, h.text(t.Error()))
		}
	}
	b, err := marshalJSON(x)
	if err != nil {
		return slog.Any(key, x)
	}
	return h.jsonAttr(key, b)
}

// jsonAttr: маска работает по JSON-байтам, поэтому паттерн или ReplaceWith с
// кавычкой могут сделать значение невалидным — тогда оно уходит строкой, а не
// «!ERROR».
func (h *redactHandler) jsonAttr(key string, b []byte) slog.Attr {
	rb := h.bytes(b)
	if !json.Valid(rb) {
		return slog.String(key, string(rb))
	}
	return slog.Any(key, json.RawMessage(rb))
}

// marshalJSON повторяет кодирование slog.JSONHandler (без HTML-escape), чтобы
// маскированное и нетронутое значение выглядели одинаково.
func marshalJSON(x any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(x); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
