package httpkit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
)

var latin = regexp.MustCompile(`[A-Za-z]`)

// Тексты, которые кит пишет сам, — русские целиком (ни слова по-английски), укладываются в 200 знаков, читаются как
// законченные предложения.
func TestKitTexts_AreRussianSentences(t *testing.T) {
	for name, text := range map[string]string{
		"unauthorized":          textUnauthorized,
		"rate limited":          textRateLimited,
		"route not found":       textRouteNotFound,
		"method not allowed":    textMethodNotAllowed,
		"body too large":        textBodyTooLarge,
		"body too large (size)": bodyTooLargeText(1 << 20),
		"idempotency progress":  textIdempotencyInProgress,
		"idempotency reused":    textIdempotencyKeyReused,
		"origin not allowed":    textOriginNotAllowed,
		"validation failed":     detailTexts["validation failed"],
		"body required":         detailTexts["request body is required"],
		"body read timeout":     detailTexts["request body read timeout"],
		"marshal response":      detailTexts["unable to marshal response"],
	} {
		if latin.MatchString(text) {
			t.Errorf("%s: %q has Latin letters", name, text)
		}
		if n := utf8.RuneCountInString(text); n > 200 {
			t.Errorf("%s: %d runes, want at most 200", name, n)
		}
		if !strings.HasSuffix(text, ".") {
			t.Errorf("%s: %q must end with a period", name, text)
		}
	}
}

func TestStatusTitle(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusBadRequest:                  "Запрос составлен неверно",
		http.StatusUnauthorized:                "Нужен вход в систему",
		http.StatusForbidden:                   "Не хватает прав",
		http.StatusNotFound:                    "Не найдено",
		http.StatusMethodNotAllowed:            "Способ запроса не подходит",
		http.StatusRequestTimeout:              "Время ожидания истекло",
		http.StatusConflict:                    "Конфликт с текущими данными",
		http.StatusRequestEntityTooLarge:       "Запрос слишком большой",
		http.StatusUnsupportedMediaType:        "Формат запроса не подходит",
		http.StatusUnprocessableEntity:         "Данные не прошли проверку",
		http.StatusTooManyRequests:             "Слишком много запросов",
		http.StatusInternalServerError:         "Ошибка в работе системы",
		http.StatusBadGateway:                  "Сбой связи между сервисами",
		http.StatusServiceUnavailable:          "Сервис временно недоступен",
		http.StatusGatewayTimeout:              "Сервис не дождался ответа",
		http.StatusTeapot:                      "Запрос не выполнен", // 4xx без своего заголовка
		http.StatusHTTPVersionNotSupported:     "Ошибка в работе системы",
		http.StatusRequestHeaderFieldsTooLarge: "Слишком большие заголовки",
	} {
		if got := statusTitle(status); got != want {
			t.Errorf("statusTitle(%d) = %q, want %q", status, got, want)
		}
	}
	// Не ошибка: остаётся текст net/http, у статуса 0 (схема ошибки huma) — пусто.
	if got := statusTitle(http.StatusNotModified); got != http.StatusText(http.StatusNotModified) {
		t.Errorf("statusTitle(304) = %q", got)
	}
	if got := statusTitle(0); got != "" {
		t.Errorf("statusTitle(0) = %q, want empty", got)
	}
	for status, title := range statusTitles {
		if latin.MatchString(title) || utf8.RuneCountInString(title) > 30 {
			t.Errorf("title of %d = %q: Russian only, at most 30 runes", status, title)
		}
		if status < http.StatusBadRequest || status > 599 {
			t.Errorf("title of %d: only error statuses have a title", status)
		}
	}
}

func TestDetailText(t *testing.T) {
	for msg, want := range map[string]string{
		"validation failed":                             "Данные не прошли проверку. Исправьте указанные поля и повторите запрос.",
		"request body is required":                      "В запросе нет данных. Передайте их и повторите запрос.",
		"request body is too large limit=1048576 bytes": "Запрос слишком большой: допустимо меньше 1 МБ. Уменьшите его размер и повторите.",
		"request body is too large limit=65536 bytes":   "Запрос слишком большой: допустимо меньше 64 КБ. Уменьшите его размер и повторите.",
		"request body is too large limit=1500 bytes":    "Запрос слишком большой: допустимо меньше 1500 байт. Уменьшите его размер и повторите.",
		"item not found":                                "item not found", // текст потребителя остаётся как есть
		"":                                              "",
	} {
		if got := detailText(msg); got != want {
			t.Errorf("detailText(%q) = %q, want %q", msg, got, want)
		}
	}
	if got := bodyTooLargeText(0); got != textBodyTooLarge {
		t.Errorf("bodyTooLargeText(0) = %q: without a known limit — the text without a number", got)
	}
}

func TestFieldTexts_Translate(t *testing.T) {
	for msg, want := range map[string]string{
		"expected length >= 5":                                             "число знаков: не меньше 5",
		"expected length <= 200":                                           "число знаков: не больше 200",
		"expected array length >= 1":                                       "число элементов в списке: не меньше 1",
		"expected array length <= 50":                                      "число элементов в списке: не больше 50",
		"expected object with at least 1 properties":                       "число полей: не меньше 1",
		"expected object with at most 3 properties":                        "число полей: не больше 3",
		"expected number >= 0.5":                                           "число не меньше 0.5",
		"expected number > 0":                                              "число больше 0",
		"expected number <= 100":                                           "число не больше 100",
		"expected number < 100":                                            "число меньше 100",
		"expected number to be a multiple of 5":                            "число должно делиться на 5 без остатка",
		"expected required property title to be present":                   "не заполнено обязательное поле title",
		"expected property end to be present when start is present":        "поле end обязательно, если указано поле start",
		`expected value to be one of "open, in_progress, done"`:            "значение должно быть одним из: open, in_progress, done",
		"expected value to be done":                                        "значение должно быть таким: done",
		"expected string to be a lowercase slug":                           "значение должно соответствовать: a lowercase slug",
		"expected string to match pattern ^[a-z]+$":                        "значение должно подходить под шаблон ^[a-z]+$",
		"expected string to be RFC 4122 uuid: invalid UUID length: 3":      "нужен идентификатор вида 123e4567-e89b-12d3-a456-426614174000",
		"expected string to be RFC 5322 email: mail: no angle-addr":        "нужен адрес электронной почты, например name@example.com",
		"expected string to be RFC 3339 date-time":                         "нужны дата и время, например 2026-11-09T14:30:00+03:00",
		"expected string to be RFC 3339 date":                              "нужна дата, например 2026-11-09",
		"expected string to be regex: error parsing regexp":                "нужно регулярное выражение, записанное без ошибок",
		"expected duration: time: invalid duration":                        "нужна длительность, например 1h30m (h – часы, m – минуты, s – секунды)",
		"expected boolean":                                                 "нужно значение «да» или «нет»",
		"required form parameter is missing":                               "не заполнено обязательное поле формы",
		"File required":                                                    "нужен файл",
		"At least one file is required":                                    "нужен хотя бы один файл",
		"Multiple files received but only one was expected":                "нужен один файл, а передано несколько",
		"Invalid mime type: got text/plain, expected image/png":            "тип файла не подходит: передан text/plain, нужен image/png",
		"Failed to open file":                                              "файл не удалось прочитать",
		"Failed to infer file media type":                                  "не удалось определить тип файла",
		"cannot read multipart form: multipart: NextPart: EOF":             "данные формы не удалось прочитать: запрос оборван или записан с ошибкой",
		"expected integer":                                                 "нужно целое число",
		"expected number":                                                  "нужно число",
		"expected string":                                                  "нужен текст",
		"expected array":                                                   "нужен список",
		"expected object":                                                  "нужен набор полей",
		"expected array items to be unique":                                "в списке не должно быть повторяющихся элементов",
		"unexpected property":                                              "лишнее поле: такого поля в запросе нет",
		"unknown query parameter":                                          "лишний параметр запроса: такого параметра у запроса нет",
		"required query parameter is missing":                              "не указан обязательный параметр запроса",
		"required header parameter is missing":                             "не указан обязательный заголовок запроса",
		"invalid integer":                                                  "нужно целое число",
		"invalid value: ParseAddr(\"S3CRET\"): unable to parse IP":         "значение указано неверно",
		"unexpected end of JSON input":                                     "данные запроса оборваны: запись не закончена",
		"invalid character '}' looking for beginning of object key string": "данные запроса записаны с ошибкой: встретился недопустимый знак",
		"unknown content type: text/plain":                                 "формат данных не поддерживается: text/plain",
		"end must follow start":                                            "end must follow start", // текст потребителя остаётся как есть
	} {
		if got := fieldTexts.translate(msg); got != want {
			t.Errorf("translate(%q) = %q, want %q", msg, got, want)
		}
	}
}

// Сообщение, которое цитирует ввод, по-русски его не несёт: в поле бывает пароль.
func TestFieldTexts_DoNotEchoInput(t *testing.T) {
	for _, msg := range []string{
		"expected string to be RFC 5322 email: S3CRET",
		"expected string to be RFC 3986 uri: S3CRET",
		"expected string to be RFC 4122 uuid: S3CRET",
		"expected string to be regex: S3CRET",
		"expected duration: S3CRET",
		"invalid value: S3CRET",
		"invalid JSON: S3CRET",
		"invalid character 'S' looking for beginning of value",
	} {
		if got := fieldTexts.translate(msg); strings.Contains(got, "S3CRET") || got == msg {
			t.Errorf("translate(%q) = %q: translated and without the input", msg, got)
		}
	}
}

// Каждое сообщение таблицы — по-русски: пример формата может быть латиницей, слов по-английски нет.
func TestFieldTexts_AreRussian(t *testing.T) {
	cyrillic := regexp.MustCompile(`[А-Яа-я]`)
	english := regexp.MustCompile(`\b(expected|invalid|unknown|required|missing|property|RFC|UUID|JSON|regex|string|array|object)\b`)
	rules := fieldTexts.rules
	if len(rules) == 0 || len(fieldTexts.exact) == 0 {
		t.Fatalf("empty table: %d rules, %d exact", len(rules), len(fieldTexts.exact))
	}
	check := func(format, text string) {
		t.Helper()
		if !cyrillic.MatchString(text) || english.MatchString(text) {
			t.Errorf("%q → %q: the text must be Russian", format, text)
		}
		if n := utf8.RuneCountInString(text); n > 200 {
			t.Errorf("%q → %q: %d runes, want at most 200", format, text, n)
		}
	}
	for format, text := range fieldTexts.exact {
		check(format, text)
	}
	for _, r := range rules {
		check(r.format, r.text)
	}
}

// Подстановка в русском тексте — только из шаблона huma: ${n} не больше числа подстановок.
func TestFieldTexts_PlaceholdersMatchFormats(t *testing.T) {
	ref := regexp.MustCompile(`\$\{(\d+)\}`)
	for _, r := range fieldTexts.rules {
		for _, m := range ref.FindAllStringSubmatch(r.text, -1) {
			if n, _ := strconv.Atoi(m[1]); n < 1 || n > r.re.NumSubexp() {
				t.Errorf("%q → %q: ${%d} has no value in the huma message", r.format, r.text, n)
			}
		}
	}
}

// Новое сообщение huma после обновления библиотеки без строки в таблице осталось бы английским: тест называет его.
func TestFieldTexts_CoverEveryHumaMessage(t *testing.T) {
	// Каталог huma — по функции, которую httpkit не подменяет: huma.NewError кит заменяет своим newError.
	pc := reflect.ValueOf(huma.DefaultConfig).Pointer()
	file, _ := runtime.FuncForPC(pc).FileLine(pc)
	src := filepath.Join(filepath.Dir(file), "validation", "messages.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), src, nil, 0)
	if err != nil {
		t.Fatalf("huma sources are not readable: %v", err)
	}
	var found int
	ast.Inspect(parsed, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			lit, isLit := spec.Values[i].(*ast.BasicLit)
			if !strings.HasPrefix(name.Name, "Msg") || !isLit {
				continue
			}
			format, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: %v", name.Name, err)
			}
			found++
			if !fieldTexts.knows(format) {
				t.Errorf("validation.%s (%q) has no Russian text in fieldTexts", name.Name, format)
			}
		}
		return true
	})
	if found < 40 {
		t.Fatalf("found %d validation.Msg* in %s: the parser lost the list", found, src)
	}
}

func TestNewTextTable_RejectsUnsupportedVerb(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a verb other than d, v and s must panic: the table is code")
		}
	}()
	newTextTable([2]string{"expected %q", "ничего"})
}
