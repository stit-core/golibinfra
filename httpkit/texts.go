package httpkit

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2/validation"
)

// Тексты ответов API — title, detail и errors[].message — русские: их читает человек, который систему не
// строил, — без внутренних слов и кодов, с тем, что делать. Код ответа (`code`), статус и форма тела от языка не
// зависят; логи, ошибки Go и sentinel'ы английские.
const (
	textUnauthorized          = "Вы не вошли в систему или вход больше не действует. Войдите заново и повторите действие."
	textRateLimited           = "Слишком много запросов за короткое время. Подождите немного и повторите действие."
	textRouteNotFound         = "Такого адреса в системе нет. Проверьте адрес запроса."
	textMethodNotAllowed      = "Для этого адреса такой способ запроса не подходит. Выберите другой способ."
	textBodyTooLarge          = "Запрос слишком большой. Уменьшите его размер и повторите."
	textIdempotencyInProgress = "Запрос с таким ключом идемпотентности еще выполняется. Подождите и не отправляйте его повторно."
	textIdempotencyKeyReused  = "Ключ идемпотентности уже использован для другого запроса. Для нового запроса укажите новый ключ."
	textOriginNotAllowed      = "Менять данные можно только в приложении, открытом по его основному адресу. Откройте приложение по этому адресу и повторите действие."
)

// statusTitles — title ответа по статусу: до 30 знаков, читается без detail (у 5xx detail нет).
var statusTitles = map[int]string{
	http.StatusBadRequest:                  "Запрос составлен неверно",
	http.StatusUnauthorized:                "Нужен вход в систему",
	http.StatusPaymentRequired:             "Нужна оплата",
	http.StatusForbidden:                   "Не хватает прав",
	http.StatusNotFound:                    "Не найдено",
	http.StatusMethodNotAllowed:            "Способ запроса не подходит",
	http.StatusNotAcceptable:               "Нет подходящего формата ответа",
	http.StatusRequestTimeout:              "Время ожидания истекло",
	http.StatusConflict:                    "Конфликт с текущими данными",
	http.StatusGone:                        "Больше недоступно",
	http.StatusLengthRequired:              "Не указан размер запроса",
	http.StatusPreconditionFailed:          "Условие запроса не выполнено",
	http.StatusRequestEntityTooLarge:       "Запрос слишком большой",
	http.StatusRequestURITooLong:           "Адрес запроса слишком длинный",
	http.StatusUnsupportedMediaType:        "Формат запроса не подходит",
	http.StatusUnprocessableEntity:         "Данные не прошли проверку",
	http.StatusLocked:                      "Запись заблокирована",
	http.StatusPreconditionRequired:        "Не указано условие запроса",
	http.StatusTooManyRequests:             "Слишком много запросов",
	http.StatusRequestHeaderFieldsTooLarge: "Слишком большие заголовки",
	http.StatusInternalServerError:         "Ошибка в работе системы",
	http.StatusNotImplemented:              "Действие не поддерживается",
	http.StatusBadGateway:                  "Сбой связи между сервисами",
	http.StatusServiceUnavailable:          "Сервис временно недоступен",
	http.StatusGatewayTimeout:              "Сервис не дождался ответа",
}

// statusTitle — title ответа-ошибки по статусу. Статус вне 400–599 — не ошибка, у него остаётся текст net/http.
func statusTitle(status int) string {
	if title, ok := statusTitles[status]; ok {
		return title
	}
	switch {
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
		return "Запрос не выполнен"
	case status >= http.StatusInternalServerError && status < 600:
		return "Ошибка в работе системы"
	}
	return http.StatusText(status)
}

// byteSize — размер лимита словами читателя: целые мегабайты и килобайты, иначе байты.
func byteSize(n int64) string {
	switch {
	case n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " МБ"
	case n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + " КБ"
	}
	return strconv.FormatInt(n, 10) + " байт"
}

// bodyTooLargeText — detail 413: с лимитом, если он известен.
func bodyTooLargeText(limit int64) string {
	if limit <= 0 {
		return textBodyTooLarge
	}
	return "Запрос слишком большой: допустимо не больше " + byteSize(limit) + ". Уменьшите его размер и повторите."
}

// bodyLimitMessage — сообщение huma о теле сверх лимита операции (`request body is too large limit=N bytes`).
var bodyLimitMessage = regexp.MustCompile(`^request body is too large limit=(\d+) bytes$`)

// detailTexts — сообщения huma, которые становятся detail ответа: общий итог проверки и ошибки чтения запроса.
var detailTexts = map[string]string{
	"validation failed":          "Данные не прошли проверку. Исправьте указанные поля и повторите запрос.",
	"request body is required":   "В запросе нет данных. Передайте их и повторите запрос.",
	"request body read timeout":  "Данные запроса приходили слишком медленно. Повторите запрос.",
	"unable to marshal response": "Система не может ответить в формате, который просит запрос.",
}

// detailText — detail ответа: сообщение huma по-русски; текст, которого в таблице нет (свой текст потребителя),
// остаётся как есть.
func detailText(msg string) string {
	if text, ok := detailTexts[msg]; ok {
		return text
	}
	if m := bodyLimitMessage.FindStringSubmatch(msg); m != nil {
		limit, _ := strconv.ParseInt(m[1], 10, 64)
		return bodyTooLargeText(limit)
	}
	return msg
}

// textTable — таблица перевода сообщений huma: шаблон huma (fmt: %d, %v, %s) → русский текст, в котором ${n} —
// n-я подстановка из сообщения.
type textTable struct {
	exact map[string]string
	rules []textRule
}

type textRule struct {
	format string
	re     *regexp.Regexp
	text   string
	weight int
}

// newTextTable собирает таблицу из пар {шаблон huma, русский текст}. Шаблон без подстановок сверяется целиком,
// остальные — по длине неизменной части, длинная впереди: «expected string to be RFC 4122 uuid: %v» раньше
// «expected string to be %s». Неподдерживаемая подстановка — panic: таблица — код, и сломанная падает на старте.
func newTextTable(pairs ...[2]string) textTable {
	t := textTable{exact: map[string]string{}}
	for _, pair := range pairs {
		format, text := pair[0], pair[1]
		if !strings.Contains(format, "%") {
			t.exact[format] = text
			continue
		}
		var re strings.Builder
		re.WriteString("(?s)^")
		weight := 0
		for rest := format; rest != ""; {
			i := strings.IndexByte(rest, '%')
			if i < 0 {
				re.WriteString(regexp.QuoteMeta(rest))
				weight += len(rest)
				break
			}
			re.WriteString(regexp.QuoteMeta(rest[:i]))
			weight += i
			switch rest[i+1] {
			case 'd':
				re.WriteString(`(-?\d+)`)
			case 'v', 's':
				re.WriteString(`(.*)`)
			default:
				panic("httpkit: text table: unsupported verb in " + format)
			}
			rest = rest[i+2:]
		}
		re.WriteString("$")
		t.rules = append(t.rules, textRule{format: format, re: regexp.MustCompile(re.String()), text: text, weight: weight})
	}
	slices.SortStableFunc(t.rules, func(a, b textRule) int { return b.weight - a.weight })
	return t
}

// translate — русский текст сообщения huma; сообщение, которого в таблице нет, возвращается как есть.
func (t textTable) translate(msg string) string {
	if text, ok := t.exact[msg]; ok {
		return text
	}
	for _, r := range t.rules {
		if r.re.MatchString(msg) {
			return r.re.ReplaceAllString(msg, r.text)
		}
	}
	return msg
}

// knows — шаблон huma есть в таблице.
func (t textTable) knows(format string) bool {
	if _, ok := t.exact[format]; ok {
		return true
	}
	return slices.ContainsFunc(t.rules, func(r textRule) bool { return r.format == format })
}

// fieldTexts — сообщения полей huma (errors[].message). Первые — validation.Msg* huma: таблица ссылается на них
// самих, а не на копии английского, и шаблон, который huma переписал, остаётся в таблице. Сообщения, которые
// цитируют ввод (после «: » значение поля), по-русски его не несут: в поле бывает пароль. Формат, а не слово
// «RFC», называет пример: читатель видит, как записать. Число в сообщении всегда с именем: «число знаков:
// не меньше 3» не требует склонения.
var fieldTexts = newTextTable(
	[2]string{validation.MsgUnexpectedProperty, "лишнее поле: такого поля в запросе нет"},
	[2]string{validation.MsgExpectedRFC3339DateTime, "нужны дата и время, например 2026-11-09T14:30:00+03:00"},
	[2]string{validation.MsgExpectedRFC1123DateTime, "нужны дата и время, например Mon, 09 Nov 2026 14:30:00 GMT"},
	[2]string{validation.MsgExpectedRFC3339Date, "нужна дата, например 2026-11-09"},
	[2]string{validation.MsgExpectedRFC3339Time, "нужно время, например 14:30:00+03:00"},
	[2]string{validation.MsgExpectedRFC5322Email, "нужен адрес электронной почты, например name@example.com"},
	[2]string{validation.MsgExpectedRFC5322EmailBare, "нужен только адрес электронной почты, без имени, например name@example.com"},
	[2]string{validation.MsgExpectedRFC5890Hostname, "нужно имя сайта или сервера, например example.com"},
	[2]string{validation.MsgExpectedRFC2673IPv4, "нужен сетевой адрес из четырех чисел через точку, например 192.168.0.1"},
	[2]string{validation.MsgExpectedRFC2373IPv6, "нужен сетевой адрес шестой версии, например 2001:db8::1"},
	[2]string{validation.MsgExpectedRFCIPAddr, "нужен сетевой адрес, например 192.168.0.1 или 2001:db8::1"},
	[2]string{validation.MsgExpectedRFC3986URI, "нужна ссылка, например https://example.com/page"},
	[2]string{validation.MsgExpectedRFC3986AbsoluteURI, "нужна полная ссылка, например https://example.com/page"},
	[2]string{validation.MsgExpectedRFC4122UUID, "нужен идентификатор вида 123e4567-e89b-12d3-a456-426614174000"},
	[2]string{validation.MsgExpectedRFC6570URITemplate, "нужен шаблон ссылки, например /objects/{id}"},
	[2]string{validation.MsgExpectedRFC6901JSONPointer, "нужен путь к полю данных, например /title/0"},
	[2]string{validation.MsgExpectedRFC6901RelativeJSONPointer, "нужен относительный путь к полю данных, например 1/title"},
	[2]string{validation.MsgExpectedRegexp, "нужно регулярное выражение, записанное без ошибок"},
	[2]string{validation.MsgExpectedMatchAtLeastOneSchema, "значение не подходит ни под один из допустимых вариантов"},
	[2]string{validation.MsgExpectedMatchExactlyOneSchema, "значение должно подходить ровно под один из допустимых вариантов"},
	[2]string{validation.MsgExpectedNotMatchSchema, "значение недопустимо: оно подходит под запрещенный вариант"},
	[2]string{validation.MsgExpectedPropertyNameInObject, "в данных нет поля, по которому выбирается вариант"},
	[2]string{validation.MsgExpectedBoolean, "нужно значение «да» или «нет»"},
	[2]string{validation.MsgExpectedDuration, "нужна длительность, например 1h30m (h – часы, m – минуты, s – секунды)"},
	[2]string{validation.MsgExpectedNumber, "нужно число"},
	[2]string{validation.MsgExpectedInteger, "нужно целое число"},
	[2]string{validation.MsgExpectedString, "нужен текст"},
	[2]string{validation.MsgExpectedBase64String, "нужны данные, закодированные в base64"},
	[2]string{validation.MsgExpectedArray, "нужен список"},
	[2]string{validation.MsgExpectedObject, "нужен набор полей"},
	[2]string{validation.MsgExpectedArrayItemsUnique, "в списке не должно быть повторяющихся элементов"},
	[2]string{validation.MsgExpectedOneOf, "значение должно быть одним из: ${1}"},
	[2]string{validation.MsgExpectedConst, "значение должно быть таким: ${1}"},
	[2]string{validation.MsgExpectedMinimumNumber, "число не меньше ${1}"},
	[2]string{validation.MsgExpectedExclusiveMinimumNumber, "число больше ${1}"},
	[2]string{validation.MsgExpectedMaximumNumber, "число не больше ${1}"},
	[2]string{validation.MsgExpectedExclusiveMaximumNumber, "число меньше ${1}"},
	[2]string{validation.MsgExpectedNumberBeMultipleOf, "число должно делиться на ${1} без остатка"},
	[2]string{validation.MsgExpectedMinLength, "число знаков: не меньше ${1}"},
	[2]string{validation.MsgExpectedMaxLength, "число знаков: не больше ${1}"},
	[2]string{validation.MsgExpectedBePattern, "значение должно соответствовать: ${1}"},
	[2]string{validation.MsgExpectedMatchPattern, "значение должно подходить под шаблон ${1}"},
	[2]string{validation.MsgExpectedMinItems, "число элементов в списке: не меньше ${1}"},
	[2]string{validation.MsgExpectedMaxItems, "число элементов в списке: не больше ${1}"},
	[2]string{validation.MsgExpectedMinProperties, "число полей: не меньше ${1}"},
	[2]string{validation.MsgExpectedMaxProperties, "число полей: не больше ${1}"},
	[2]string{validation.MsgExpectedRequiredProperty, "не заполнено обязательное поле ${1}"},
	[2]string{validation.MsgExpectedDependentRequiredProperty, "поле ${1} обязательно, если указано поле ${2}"},
	[2]string{validation.MsgExpectedResolvableSchemaRef, "ошибка в описании запроса на стороне системы: сообщите администратору системы"},

	// Сообщения huma без констант: разбор параметров и тела запроса.
	[2]string{"unknown query parameter", "лишний параметр запроса: такого параметра у запроса нет"},
	[2]string{"required query parameter is missing", "не указан обязательный параметр запроса"},
	[2]string{"required header parameter is missing", "не указан обязательный заголовок запроса"},
	[2]string{"required path parameter is missing", "в адресе не указан обязательный параметр"},
	[2]string{"required cookie parameter is missing", "не передан обязательный параметр в куки"},
	[2]string{"expected at most one value, but received multiple values", "нужно одно значение, а передано несколько"},
	[2]string{"invalid integer", "нужно целое число"},
	[2]string{"invalid float", "нужно число"},
	[2]string{"invalid floating value", "нужно число"},
	[2]string{"invalid boolean", "нужно значение «да» или «нет»"},
	[2]string{"invalid date/time for format %s", "дата и время указаны неверно"},
	[2]string{"invalid url.URL value", "ссылка указана неверно"},
	[2]string{"invalid value: %v", "значение указано неверно"},
	[2]string{"invalid JSON: %v", "значение записано с ошибкой"},
	[2]string{"unexpected end of JSON input", "данные запроса оборваны: запись не закончена"},
	[2]string{"invalid character %v", "данные запроса записаны с ошибкой: встретился недопустимый знак"},
	[2]string{"unknown content type", "формат данных не поддерживается"},
	[2]string{"unknown content type: %s", "формат данных не поддерживается: ${1}"},
)
