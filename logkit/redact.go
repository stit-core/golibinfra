package logkit

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"sync"
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

// secretKeyWords — слова-секреты: слово ключа совпадает с ним или оканчивается на
// него (accesstoken, newpassword); api_key и private_key ловит пара слов.
var secretKeyWords = [...]string{
	"apikey", "authorization", "cookie", "cookies", "credential", "credentials", "jwt",
	"passphrase", "passwd", "password", "privatekey", "pwd", "secret", "token",
}

// keySecrecy — что имя ключа говорит о значении под ним.
type keySecrecy int

const (
	keyPlain  keySecrecy = iota // значение видят только маски
	keySecret                   // строка, группа и структура заменяются целиком
	keyCode                     // то же и число
)

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
// …), в том числе у map, — целиком; числа (кроме ключей-кодов otp и pin), bool,
// time и duration не трогает.
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
	if maskable(v, classifyKey(a.Key)) {
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

// maskable: под ключом-секретом прячутся строки, группы и структуры, под кодом —
// ещё и числа; bool, время и nil секрета не несут, как и число под обычным
// секретом (token_count — счётчик).
func maskable(v slog.Value, class keySecrecy) bool {
	if class == keyPlain {
		return false
	}
	switch v.Kind() {
	case slog.KindString, slog.KindGroup:
		return true
	case slog.KindAny:
		return v.Any() != nil
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64:
		return class == keyCode
	default:
		return false
	}
}

// classifyKey: ключ без подстроки из keyTriggers — не секрет и на слова не
// режется: обход проверяет каждый ключ map, а подстроки нет почти ни у одного.
func classifyKey(key string) keySecrecy {
	if !hasKeyTrigger(key) {
		return keyPlain
	}
	return classifyWords(key)
}

// keyTriggers — подстроки, без которых ни один ключ не секрет: слова словаря,
// api и private (api_key, private_key), коды pin и otp.
var keyTriggers = []string{
	"api", "authorization", "cookie", "credential", "jwt", "otp", "passphrase", "passwd",
	"password", "pin", "private", "pwd", "secret", "token",
}

// keyTriggersAt — keyTriggers по первой букве в обоих регистрах: в позиции ключа
// сверяются только те, что с неё начинаются.
var keyTriggersAt = func() (t [256][]string) {
	for _, w := range keyTriggers {
		t[w[0]] = append(t[w[0]], w)
		t[w[0]-'a'+'A'] = append(t[w[0]-'a'+'A'], w)
	}
	return t
}()

// hasKeyTrigger — в ключе есть подстрока из keyTriggers без учёта регистра.
func hasKeyTrigger(key string) bool {
	for i := 0; i < len(key); i++ {
		for _, w := range keyTriggersAt[key[i]] {
			if foldPrefix(key[i:], w) {
				return true
			}
		}
	}
	return false
}

// classifyWords: ключ режется на слова по разделителям, границам camelCase и цифр
// («X-Api-Key», «accessToken», «oauth2Token»), слова сверяются со словарём.
// По словам, а не подстрокой: «input_tokens» — счётчик, а не секрет.
func classifyWords(key string) keySecrecy {
	class := keyPlain
	var prev string
	for i := 0; i < len(key); {
		w, j := nextKeyWord(key, i)
		if w == "" {
			break
		}
		switch {
		case codeWord(w):
			return keyCode
		case secretWord(w) || foldEq(w, "key") && (foldEq(prev, "api") || foldEq(prev, "private")):
			class = keySecret
		}
		prev, i = w, j
	}
	return class
}

func secretWord(w string) bool {
	for _, sw := range secretKeyWords {
		if hasSuffixFold(w, sw) {
			return true
		}
	}
	return false
}

// codeWord — слово-код: pin, pincode или слово на otp (otp, totp). Ключ с ним
// где угодно прячет значение любого вида, и число тоже.
func codeWord(w string) bool {
	return foldEq(w, "pin") || foldEq(w, "pincode") || hasSuffixFold(w, "otp")
}

func foldEq(a, b string) bool { return len(a) == len(b) && strings.EqualFold(a, b) }

func hasSuffixFold(w, suffix string) bool {
	return len(w) >= len(suffix) && strings.EqualFold(w[len(w)-len(suffix):], suffix)
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
	// Копия с маскированием — до кодирования: запасной путь пишет её же, исходник
	// под ключом-секретом — никогда.
	x = h.maskKeys(x)
	b, err := marshalJSON(x)
	if err != nil {
		return slog.Any(key, x)
	}
	return h.jsonAttr(key, b)
}

// maskKeys прячет значения под ключами-секретами у map со строковыми ключами
// (http.Header, url.Values, map[string]any) — на любой глубине map, срезов и
// массивов, в том числе за interface; без таких ключей отдаёт x как есть, не
// копируя. Копия кодируется в тот же JSON, что оригинал, поэтому указатели не
// разыменовываются, а типы со своим MarshalJSON или MarshalText не разбираются.
func (h *redactHandler) maskKeys(x any) any {
	v := container(reflect.ValueOf(x))
	var w walk
	if !v.IsValid() || !w.secretsIn(v) {
		return x
	}
	return h.masked(v, map[visit]any{})
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// container — v (сквозь interface), если в нём могут быть ключи map: map со
// строковыми ключами, срез или массив, в элементах которого бывают такие map;
// иначе невалидный Value.
func container(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Map:
		if v.Type().Key().Kind() == reflect.String && !selfEncoding(v.Type()) {
			return v
		}
	case reflect.Slice, reflect.Array:
		// Элементы среза адресуемы: MarshalJSON на указателе у них зовётся, у копии — нет.
		if elem := v.Type().Elem(); holdsMaps(elem) && !selfEncoding(v.Type()) && !selfEncoding(elem) {
			return v
		}
	}
	return reflect.Value{}
}

// holdsMaps — в значении типа t может оказаться map со строковыми ключами;
// рекурсивный тип после нескольких уровней считается «может».
func holdsMaps(t reflect.Type) bool {
	for range 8 {
		switch t.Kind() {
		case reflect.Interface:
			return true
		case reflect.Map:
			return t.Key().Kind() == reflect.String
		case reflect.Slice, reflect.Array:
			t = t.Elem()
		default:
			return false
		}
	}
	return true
}

// selfEncodingCache — reflect.Type → bool: обход спрашивает про каждую map и срез
// записи, а четыре Implements дороги; типов в логах немного.
var selfEncodingCache sync.Map

// selfEncoding — тип или указатель на него сам решает, как выглядеть в JSON.
func selfEncoding(t reflect.Type) bool {
	if self, ok := selfEncodingCache.Load(t); ok {
		return self.(bool)
	}
	p := reflect.PointerTo(t)
	self := t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) ||
		p.Implements(jsonMarshalerType) || p.Implements(textMarshalerType)
	selfEncodingCache.Store(t, self)
	return self
}

// visit — пройденный контейнер: цикл (m["self"] = m) и общее поддерево
// обходятся один раз; у массива адреса нет, он не отмечается.
type visit struct {
	typ reflect.Type
	ptr uintptr
	len int
}

func identity(v reflect.Value) (visit, bool) {
	switch v.Kind() {
	case reflect.Map:
		return visit{typ: v.Type(), ptr: v.Pointer()}, true
	case reflect.Slice:
		return visit{typ: v.Type(), ptr: v.Pointer(), len: v.Len()}, true
	default:
		return visit{}, false
	}
}

// walk — состояние поиска секрета: пройденные контейнеры.
type walk struct {
	seen map[visit]bool
}

// secretsIn: есть ли секрет в контейнере v или во вложенных.
func (w *walk) secretsIn(v reflect.Value) bool {
	v = container(v)
	if !v.IsValid() || v.Kind() != reflect.Array && v.IsNil() {
		return false
	}
	// Значения map смотрятся, только если ключ-секрет или в них может быть map;
	// map без таких значений в цикл не входит и не отмечается.
	nested := v.Kind() != reflect.Map || holdsMaps(v.Type().Elem())
	if id, ok := identity(v); ok && nested {
		if w.seen[id] {
			return false
		}
		if w.seen == nil {
			w.seen = map[visit]bool{}
		}
		w.seen[id] = true
	}
	if v.Kind() != reflect.Map {
		for i := range v.Len() {
			if w.secretsIn(v.Index(i)) {
				return true
			}
		}
		return false
	}
	key, val := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
	for it := v.MapRange(); it.Next(); {
		key.SetIterKey(it)
		class := classifyKey(key.String())
		if class == keyPlain && !nested {
			continue
		}
		val.SetIterValue(it)
		if class != keyPlain && maskable(slog.AnyValue(val.Interface()), class) {
			return true
		}
		if nested && w.secretsIn(val) {
			return true
		}
	}
	return false
}

// masked — копия контейнера v с заменой значений под ключами-секретами: map —
// map[string]any, срез и массив — []any, JSON тот же. memo — копии пройденных
// контейнеров: рекурсия по циклу конечна, а сам цикл остаётся циклом, и его,
// как без маскирования, отвергает JSON.
func (h *redactHandler) masked(v reflect.Value, memo map[visit]any) any {
	c := container(v)
	if !c.IsValid() || c.Kind() != reflect.Array && c.IsNil() {
		return v.Interface()
	}
	id, tracked := identity(c)
	if done, ok := memo[id]; tracked && ok {
		return done
	}
	if c.Kind() != reflect.Map {
		out := make([]any, c.Len())
		if tracked {
			memo[id] = out
		}
		for i := range out {
			out[i] = h.masked(c.Index(i), memo)
		}
		return out
	}
	out := make(map[string]any, c.Len())
	memo[id] = out
	for it := c.MapRange(); it.Next(); {
		k, val := it.Key().String(), it.Value()
		if maskable(slog.AnyValue(val.Interface()), classifyKey(k)) {
			out[k] = h.with
			continue
		}
		out[k] = h.masked(val, memo)
	}
	return out
}

// jsonAttr: маска работает по JSON-байтам, поэтому паттерн или ReplaceWith с
// кавычкой могут сделать значение невалидным — тогда оно уходит строкой, а не
// «!ERROR».
func (h *redactHandler) jsonAttr(key string, b []byte) slog.Attr {
	rb := h.bytes(b)
	// Нетронутые маской байты — вывод json.Marshal, повторная проверка не нужна.
	unchanged := len(rb) == len(b) && len(b) > 0 && &rb[0] == &b[0]
	if !unchanged && !json.Valid(rb) {
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
