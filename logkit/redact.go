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

// secretKeyWords — слова-секреты: слово ключа (и без «s» множественного числа: secrets,
// passwords) совпадает с ним или оканчивается на него (accesstoken, newpassword); api_key,
// api_keys, XAPIKey, private_key и code_verifier ловит пара слов.
var secretKeyWords = [...]string{
	"apikey", "authorization", "cookie", "cookies", "credential", "credentials", "jwt",
	"passphrase", "passwd", "password", "privatekey", "pwd", "secret", "signature", "token",
}

// secretExactWords — слова-секреты только целым словом: bypass, inside, autorefresh, sacred — не они.
var secretExactWords = [...]string{"cred", "creds", "pass", "pw", "refresh", "sid", "tokens"}

// codeQualifiers — слова, после которых code — секрет (verification_code, smsCode); голый code —
// код ошибки, как status_code и error_code.
var codeQualifiers = [...]string{
	"access", "auth", "authorization", "backup", "confirm", "confirmation", "device", "email", "invite",
	"login", "magic", "mfa", "oauth", "recovery", "reset", "security", "sms", "verification", "verify",
}

// counterWords — последнее слово, с которым ключ рядом с кодом — счётчик или срок, а не код
// (otp_attempts, pin_length, otp_ttl_seconds).
var counterWords = [...]string{
	"attempt", "attempts", "count", "counter", "digits", "expires", "expiry", "interval", "len",
	"length", "limit", "max", "min", "minutes", "ms", "retries", "sec", "seconds", "size", "timeout",
	"tries", "ttl", "window",
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
// …), в том числе у map, полей структур и в JSON из RawMessage, []byte и строк, — целиком;
// числа (кроме ключей-кодов otp, pin и verification_code), bool, time и duration не трогает.
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

// minRedactToken — длина токена base64url (буквы обоих регистров и цифра), который маскирует
// дефолтный набор: ключи API, токены сброса и сессий длиннее, а UUID, SHA коммита, ULID и имена
// хостов такого сочетания не дают.
const minRedactToken = 20

// Маски дефолтного набора, которым нужна проверка совпадения (valueMasks); пароль userinfo — до
// последнего `@` слова: в пароле бывают неэкранированные `@`, `/`, `?`, `#`.
var (
	tokenRe    = regexp.MustCompile(`[A-Za-z0-9_-]{20,}`)
	userinfoRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s/?#@:]*:(\S+)@`)
)

// defaultHint — необходимое условие совпадения дефолтного набора: у каждой
// маски есть обязательный литерал («@», «+», «bearer», «basic», «eyJ», «sk-»),
// цепочка минимум из minRedactDigits цифр либо из minRedactHex hex-символов, токен
// base64url от minRedactToken символов или «=» после имени со словом ключа-секрета.
// Скан по байтам дешевле прогонки regexp'а на порядки: её цена — O(длина ×
// размер программы), а программа объединённого набора — больше двух сотен
// инструкций.
func defaultHint[T string | []byte](s T) bool {
	run, hex, sep := 0, 0, false
	tok, seen := 0, uint8(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		class := byteClasses[c]
		if class&classToken != 0 {
			tok++
			if seen |= class; tok >= minRedactToken && seen&classMixed == classMixed {
				return true
			}
		} else {
			tok, seen = 0, 0
			if c == '=' && hasKeyTrigger(pairKey(s, i)) {
				return true
			}
		}
		if class&classHex != 0 {
			if hex++; hex >= minRedactHex {
				return true
			}
		} else {
			hex = 0
		}
		if class&classDigit != 0 {
			run++
			sep = false
			if run >= minRedactDigits {
				return true
			}
			continue
		}
		if class&classSpecial == 0 {
			run, sep = 0, false
			continue
		}
		switch {
		case c == '@' || c == '+':
			return true
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

// Классы байта для defaultHint: один поиск в таблице вместо цепочки сравнений на каждый байт.
const (
	classToken uint8 = 1 << iota // алфавит base64url
	classUpper
	classLower
	classDigit
	classHex
	classSpecial // начало литерала маски или разделитель цифр карты
	classMixed   = classUpper | classLower | classDigit
)

var byteClasses = func() (t [256]uint8) {
	for c := range 256 {
		b := byte(c)
		switch {
		case isUpper(b):
			t[c] = classToken | classUpper
		case isLower(b):
			t[c] = classToken | classLower
		case isDigit(b):
			t[c] = classToken | classDigit
		case b == '-' || b == '_':
			t[c] = classToken
		}
		if isHexDigit(b) {
			t[c] |= classHex
		}
		if strings.IndexByte("@+bBes", b) >= 0 || numSep(b) {
			t[c] |= classSpecial
		}
	}
	return t
}()

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
	// JSON в строке (тело запроса, ответ upstream): ключи-секреты — как в []byte.
	if strings.IndexByte(s, '"') >= 0 && hasKeyTrigger(s) {
		if masked := h.maskJSONKeys([]byte(s)); string(masked) != s {
			s = string(masked)
		}
	}
	if h.rules.hint && !defaultHint(s) {
		return s
	}
	if h.rules.re.MatchString(s) {
		s = h.rules.re.ReplaceAllLiteralString(s, h.with)
	}
	if h.rules.hint {
		s = h.valueMasks(s)
	}
	return s
}

func (h *redactHandler) bytes(b []byte) []byte {
	if h.rules.hint && !defaultHint(b) {
		return b
	}
	if h.rules.re.Match(b) {
		b = h.rules.re.ReplaceAllLiteral(b, []byte(h.with))
	}
	if h.rules.hint {
		if s, masked := string(b), h.valueMasks(string(b)); masked != s {
			return []byte(masked)
		}
	}
	return b
}

// valueMasks — маски дефолтного набора с проверкой совпадения: токен base64url от
// minRedactToken символов с буквами обоих регистров и цифрой, пароль userinfo URL
// (`postgres://app:…@db`) и значение пары `ключ=значение` с ключом-секретом по словарю.
func (h *redactHandler) valueMasks(s string) string {
	s = tokenRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.ContainsFunc(m, func(r rune) bool { return r >= 'A' && r <= 'Z' }) &&
			strings.ContainsFunc(m, func(r rune) bool { return r >= 'a' && r <= 'z' }) &&
			strings.ContainsFunc(m, func(r rune) bool { return r >= '0' && r <= '9' }) {
			return h.with
		}
		return m
	})
	s = replaceGroup(s, userinfoRe, 1, h.with, func([]string) bool { return true })
	return h.maskPairs(s)
}

// maskPairs прячет значение пары `ключ = значение`, чей ключ — секрет по словарю: query,
// keyword-DSN (`password=…`, `password = '…'` с кавычками libpq), `otpauth://…?secret=…`. Значение
// — в кавычках или до пробела, `&` и кавычки (с `#`, `=`, `;` внутри); число прячется, только если
// ключ — код. Значение пары с обычным ключом не съедается: в нём ищутся вложенные пары
// (`redirect=https://x/?token=…`).
func (h *redactHandler) maskPairs(s string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '=' {
			continue
		}
		class := classifyKey(pairKey(s, i))
		if class == keyPlain {
			continue
		}
		start := i + 1
		for start < len(s) && isRegexpSpace(s[start]) {
			start++
		}
		end := pairValueEnd(s, start)
		if end == start || class == keySecret && !strings.ContainsFunc(s[start:end], func(r rune) bool { return r < '0' || r > '9' }) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(h.with)
		last, i = end, end-1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// pairKey — ключ пары, чей `=` стоит в s[eq]: символы `[A-Za-z0-9_.-]` перед ним (пробелы у `=`
// допустимы).
func pairKey[T string | []byte](s T, eq int) T {
	end := eq
	for end > 0 && isRegexpSpace(s[end-1]) {
		end--
	}
	start := end
	for start > 0 && (isAlnum(s[start-1]) || s[start-1] == '_' || s[start-1] == '.' || s[start-1] == '-') {
		start--
	}
	return s[start:end]
}

// pairValueEnd — конец значения пары, начатого в s[start]: в кавычках — за закрывающей (без
// неё — до конца строки), иначе до пробела, `&` или кавычки; значение не начинается с `=`
// (`token == nil` — не пара).
func pairValueEnd(s string, start int) int {
	if start >= len(s) || s[start] == '=' {
		return start
	}
	if q := s[start]; q == '\'' || q == '"' {
		for j := start + 1; j < len(s); j++ {
			if s[j] == q && s[j-1] != '\\' {
				return j + 1
			}
		}
		return len(s)
	}
	end := start
	for end < len(s) && !isRegexpSpace(s[end]) && s[end] != '&' && s[end] != '"' && s[end] != '\'' {
		end++
	}
	return end
}

// isRegexpSpace — пробел класса `\s` regexp.
func isRegexpSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }

// replaceGroup заменяет группу group каждого совпадения re в s на with, если mask(группы) —
// true; без замен отдаёт s.
func replaceGroup(s string, re *regexp.Regexp, group int, with string, mask func([]string) bool) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	var b strings.Builder
	last := 0
	for _, m := range matches {
		groups := make([]string, len(m)/2)
		for g := range groups {
			groups[g] = s[m[2*g]:m[2*g+1]]
		}
		if !mask(groups) {
			continue
		}
		b.WriteString(s[last:m[2*group]])
		b.WriteString(with)
		last = m[2*group+1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
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

// keyTriggers — подстроки, без которых ни один ключ не секрет: слова словаря (pass — и
// password, passwd, passphrase, passcode; pw — и pwd; cred — и credential, creds), api и private
// (api_key, private_key), коды pin, otp и code (и code_verifier).
var keyTriggers = []string{
	"api", "authorization", "code", "cookie", "cred", "jwt", "otp", "pass", "pin", "private",
	"pw", "refresh", "secret", "sid", "signature", "token",
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
func hasKeyTrigger[T string | []byte](key T) bool {
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
	class, code := keyPlain, false
	var prev string
	for i := 0; i < len(key); {
		w, j := nextKeyWord(key, i)
		if w == "" {
			break
		}
		switch {
		case codeWord(w) || foldEq(w, "code") && anyFoldEq(codeQualifiers[:], prev):
			code = true
		case secretWord(w),
			(foldEq(w, "key") || foldEq(w, "keys")) && (hasSuffixFold(prev, "api") || foldEq(prev, "private")),
			foldEq(w, "verifier") && foldEq(prev, "code"):
			class = keySecret
		}
		prev, i = w, j
	}
	if code && !anyFoldEq(counterWords[:], prev) {
		return keyCode
	}
	return class
}

func secretWord(w string) bool {
	singular := w
	if len(w) > 1 && (w[len(w)-1] == 's' || w[len(w)-1] == 'S') {
		singular = w[:len(w)-1]
	}
	for _, sw := range secretKeyWords {
		if hasSuffixFold(w, sw) || hasSuffixFold(singular, sw) {
			return true
		}
	}
	return anyFoldEq(secretExactWords[:], w) || anyFoldEq(secretExactWords[:], singular)
}

// codeWord — слово-код: pin, pincode, passcode или слово на otp (otp, totp). Ключ с ним где
// угодно прячет значение любого вида, и число тоже, — если ключ не кончается словом-счётчиком.
func codeWord(w string) bool {
	return foldEq(w, "pin") || foldEq(w, "pincode") || foldEq(w, "passcode") || hasSuffixFold(w, "otp")
}

func anyFoldEq(words []string, w string) bool {
	for _, x := range words {
		if foldEq(x, w) {
			return true
		}
	}
	return false
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

// anyAttr: error — как строка (так его рендерит и JSONHandler); прочее — маршалится один раз,
// дальше по конвейеру идёт готовый JSON с замаскированными ключами-секретами.
func (h *redactHandler) anyAttr(key string, x any) slog.Attr {
	switch t := x.(type) {
	case nil:
		return slog.Any(key, x)
	case json.RawMessage:
		return h.jsonAttr(key, h.maskJSONKeys(t))
	case []byte:
		if json.Valid(t) {
			t = h.maskJSONKeys(t)
		}
		return slog.Any(key, h.bytes(t))
	case error:
		if _, isMarshaler := x.(json.Marshaler); !isMarshaler {
			return slog.String(key, h.text(t.Error()))
		}
	}
	b, err := marshalJSON(x)
	if err != nil {
		// Без JSON-формы text и OTel печатали бы значение через %+v целиком (заголовки
		// *http.Request, поля структур): во все sink'и уходит только ошибка кодирования.
		return slog.String(key, h.text("!ERROR:"+err.Error()))
	}
	return h.jsonAttr(key, h.maskJSONKeys(b))
}

// maskJSONKeys прячет значения под ключами-секретами JSON-документа b на любой глубине — у map,
// полей структур, за указателями и в выводе своего MarshalJSON: строку, объект и массив —
// заменой, число — под ключом-кодом. По JSON, а не по Go-значению: имена полей — как их видит
// encoding/json. Без таких ключей и у неразбираемого b отдаёт b как есть, не копируя.
func (h *redactHandler) maskJSONKeys(b []byte) []byte {
	var out []byte
	last := 0
	for i := 0; i < len(b); i++ {
		q := bytes.IndexByte(b[i:], '"')
		if q < 0 {
			break
		}
		i += q
		end := jsonStringEnd(b, i)
		if end < 0 {
			return b
		}
		colon := skipJSONSpace(b, end)
		if colon >= len(b) || b[colon] != ':' {
			i = end - 1
			continue
		}
		class := keyPlain
		if key := b[i+1 : end-1]; hasKeyTrigger(key) {
			class = classifyWords(string(key))
		}
		start := skipJSONSpace(b, colon+1)
		if class == keyPlain {
			if inner, stop, ok := h.maskJSONInString(b, start); ok {
				out = append(out, b[last:start]...)
				out = append(out, inner...)
				last, i = stop, stop-1
				continue
			}
			i = colon
			continue
		}
		stop := jsonValueEnd(b, start)
		if stop < 0 {
			return b
		}
		if maskableJSON(b[start], class) {
			out = append(out, b[last:start]...)
			out = append(out, '"')
			out = append(out, h.with...)
			out = append(out, '"')
			last = stop
		}
		i = stop - 1
	}
	if out == nil {
		return b
	}
	return append(out, b[last:]...)
}

// maskJSONInString — строка-значение JSON с JSON внутри (тело запроса в поле структуры), уже без
// значений под ключами-секретами; ok — было что прятать.
func (h *redactHandler) maskJSONInString(b []byte, start int) (masked []byte, stop int, ok bool) {
	if start >= len(b) || b[start] != '"' {
		return nil, 0, false
	}
	stop = jsonStringEnd(b, start)
	if stop < 0 || !bytes.Contains(b[start:stop], []byte(`\"`)) || !hasKeyTrigger(b[start:stop]) {
		return nil, 0, false
	}
	var inner string
	if json.Unmarshal(b[start:stop], &inner) != nil {
		return nil, 0, false
	}
	m := h.maskJSONKeys([]byte(inner))
	if string(m) == inner {
		return nil, 0, false
	}
	masked, err := marshalJSON(string(m))
	return masked, stop, err == nil
}

// maskableJSON — правило maskable для JSON-значения по его первому байту: строка, объект и
// массив прячутся под любым ключом-секретом, число — только под кодом, true, false и null — нет.
func maskableJSON(first byte, class keySecrecy) bool {
	switch first {
	case '"', '{', '[':
		return true
	case 't', 'f', 'n':
		return false
	default:
		return class == keyCode
	}
}

// jsonStringEnd — позиция за закрывающей кавычкой строки, открытой в b[i]; -1, если строка не
// закрыта. Кавычка экранирована нечётным числом «\\» перед ней.
func jsonStringEnd(b []byte, i int) int {
	for j := i + 1; j < len(b); j++ {
		q := bytes.IndexByte(b[j:], '"')
		if q < 0 {
			return -1
		}
		j += q
		escapes := 0
		for k := j - 1; k > i && b[k] == '\\'; k-- {
			escapes++
		}
		if escapes%2 == 0 {
			return j + 1
		}
	}
	return -1
}

// jsonValueEnd — позиция за JSON-значением, начатым в b[i]; -1 у оборванного.
func jsonValueEnd(b []byte, i int) int {
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		return jsonStringEnd(b, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				end := jsonStringEnd(b, j)
				if end < 0 {
					return -1
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	}
	j := i
	for j < len(b) && b[j] != ',' && b[j] != '}' && b[j] != ']' && !isJSONSpace(b[j]) {
		j++
	}
	return j
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) && isJSONSpace(b[i]) {
		i++
	}
	return i
}

func isJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

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
