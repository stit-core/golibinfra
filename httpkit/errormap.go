package httpkit

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// ErrorRule — правило ErrorMap: доменная ошибка (Err по errors.Is или предикат
// Match — ровно одно из двух) → Status, Code и Detail ответа; Field — location
// поля запроса (`body.password`), которое ответ перечислит в errors[].
type ErrorRule struct {
	Err    error
	Match  func(error) bool
	Status int
	Code   string
	Detail string
	Field  string
}

// ErrorMap — таблица доменных ошибок домена или операции: Map — в хендлере,
// Declare — в операции для OpenAPI, Unmapped — в тесте полноты. Первое
// совпавшее правило выигрывает.
type ErrorMap struct{ rules []ErrorRule }

// errorCodesKey — ключ Operation.Metadata, где Declare оставляет коды по статусам (RegisterHealth — статусы
// тела 503 /ready).
const errorCodesKey = "httpkit.errorCodes"

// NewErrorMap собирает таблицу. Правило без ровно одного из Err и Match, без Code,
// со статусом вне 400–599 или с Err, который уже ловит правило выше, — panic:
// таблица — код, и сломанная падает на старте.
func NewErrorMap(rules ...ErrorRule) ErrorMap {
	for i, r := range rules {
		switch {
		case (r.Err == nil) == (r.Match == nil):
			panic(fmt.Sprintf("httpkit: ErrorMap rule %d (%s): set exactly one of Err and Match", i, r.Code))
		case r.Code == "":
			panic(fmt.Sprintf("httpkit: ErrorMap rule %d: Code is required", i))
		case r.Status < http.StatusBadRequest || r.Status > 599:
			panic(fmt.Sprintf("httpkit: ErrorMap rule %d (%s): status %d is not an error status", i, r.Code, r.Status))
		}
		if r.Err == nil {
			continue
		}
		for j, prev := range rules[:i] {
			if prev.matches(r.Err) {
				panic(fmt.Sprintf("httpkit: ErrorMap rule %d (%s) is shadowed by rule %d (%s)", i, r.Code, j, prev.Code))
			}
		}
	}
	return ErrorMap{rules: slices.Clone(rules)}
}

func (r ErrorRule) matches(err error) bool {
	if r.Match != nil {
		return r.Match(err)
	}
	return errors.Is(err, r.Err)
}

func (m ErrorMap) match(err error) (ErrorRule, bool) {
	for _, r := range m.rules {
		if r.matches(err) {
			return r, true
		}
	}
	return ErrorRule{}, false
}

// Map переводит err по первому совпавшему правилу в *APIError; Retry-After ответа
// берётся из RetryAfter() в цепочке err. Несовпавшую ошибку возвращает как есть:
// кит ответит на неё 500 и строкой handler error с operation в логе. nil → nil.
func (m ErrorMap) Map(err error) error {
	if err == nil {
		return nil
	}
	r, ok := m.match(err)
	if !ok {
		return err
	}
	e := Error(r.Code, r.Status, r.Detail)
	if r.Field != "" {
		e.Errors = []FieldError{{Location: r.Field, Code: r.Code, Message: r.Detail}}
	}
	e.cause = err
	return e
}

// Unmapped — ошибки из errs, которых не сопоставляет ни одно правило: тест
// полноты передаёт sentinel'ы домена и ждёт пустой результат.
func (m ErrorMap) Unmapped(errs ...error) []error {
	var out []error
	for _, err := range errs {
		if _, ok := m.match(err); !ok {
			out = append(out, err)
		}
	}
	return out
}

// Declare объявляет в операции статусы и коды таблицы: huma добавляет ответы
// этих статусов (и 422, 500) в OpenAPI, сервер httpkit пишет коды в их описание.
func (m ErrorMap) Declare(op huma.Operation) huma.Operation {
	prev, _ := op.Metadata[errorCodesKey].(map[int][]string)
	codes := make(map[int][]string, len(prev)+len(m.rules))
	for status, list := range prev {
		codes[status] = slices.Clone(list)
	}
	op.Errors = slices.Clone(op.Errors)
	for _, r := range m.rules {
		if !slices.Contains(codes[r.Status], r.Code) {
			codes[r.Status] = append(codes[r.Status], r.Code)
		}
		if !slices.Contains(op.Errors, r.Status) {
			op.Errors = append(op.Errors, r.Status)
		}
	}
	op.Metadata = maps.Clone(op.Metadata)
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[errorCodesKey] = codes
	return op
}

// describeErrors — OnAddOperation сервера: коды Declare — в описание ответа своего статуса (huma
// создаёт эти ответы раньше хука), и ответы, которые кит отдаёт сам, — там, где он их отдаёт: 429
// лимитера с Retry-After — у каждой операции, 503 дедлайна запроса — у каждой, кроме потока SSE; у
// операции huma (registered, а не маршрута мимо huma) — 401 HumaAuth с WWW-Authenticate при
// обязательной аутентификации и 413 при теле под лимитом.
func describeErrors(cfg Config, registered func(*huma.Operation) bool) huma.AddOpFunc {
	return func(oapi *huma.OpenAPI, op *huma.Operation) {
		declared, _ := op.Metadata[errorCodesKey].(map[int][]string)
		codes := maps.Clone(declared)
		if codes == nil {
			codes = map[int][]string{}
		}
		viaHuma := registered(op)
		authRequired := viaHuma && len(op.Security) > 0 &&
			!slices.ContainsFunc(op.Security, func(alt map[string][]string) bool { return len(alt) == 0 })
		if authRequired {
			kitResponse(oapi, op, codes, http.StatusUnauthorized, codeUnauthorized)
		}
		if cfg.RateLimit.RPS > 0 {
			kitResponse(oapi, op, codes, http.StatusTooManyRequests, codeRateLimited)
		}
		if cfg.RequestTimeout > 0 && !eventStream(op) {
			kitResponse(oapi, op, codes, http.StatusServiceUnavailable, codeRequestTimeout)
		}
		if viaHuma && readsLimitedBody(op) {
			kitResponse(oapi, op, codes, http.StatusRequestEntityTooLarge, codeBodyTooLarge)
		}
		for status, list := range codes {
			if resp := op.Responses[strconv.Itoa(status)]; resp != nil {
				resp.Description = http.StatusText(status) + ": " + strings.Join(list, ", ")
			}
		}
		if resp := op.Responses[strconv.Itoa(http.StatusTooManyRequests)]; resp != nil {
			setHeader(resp, "Retry-After", &huma.Param{
				Description: "Seconds to wait before repeating the request",
				Required:    true,
				Schema:      &huma.Schema{Type: huma.TypeInteger, Minimum: new(1.0)},
			})
		}
		if resp := op.Responses[strconv.Itoa(http.StatusUnauthorized)]; resp != nil && authRequired {
			setHeader(resp, "WWW-Authenticate", &huma.Param{
				Description: "Challenge of the operation's security schemes, sent with the 401 of the server authentication",
				Schema:      &huma.Schema{Type: huma.TypeString},
				Example:     challenge(oapi.Components.SecuritySchemes, op.Security),
			})
		}
	}
}

// readsBody — тело операции huma читает целиком, по лимиту операции. Multipart-форму huma разбирает сама, и тело
// сверх лимита у неё — 422 валидации.
func readsBody(op *huma.Operation) bool {
	return op.RequestBody != nil && op.RequestBody.Content["multipart/form-data"] == nil
}

// readsLimitedBody — тело операции читается под лимитом (MaxBodyBytes): превышение — 413.
func readsLimitedBody(op *huma.Operation) bool {
	return readsBody(op) && op.MaxBodyBytes > 0
}

// setHeader описывает заголовок ответа: карту заголовков huma у ответа-ошибки не создаёт.
func setHeader(resp *huma.Response, name string, h *huma.Param) {
	if resp.Headers == nil {
		resp.Headers = map[string]*huma.Param{}
	}
	resp.Headers[name] = h
}

// kitResponse объявляет у операции ответ status с кодом code, который кит отдаёт сам: код — после кодов
// Declare и без повтора, тело — APIError, и у ответа операции со своим телом (503 /ready) — рядом с ним.
func kitResponse(oapi *huma.OpenAPI, op *huma.Operation, codes map[int][]string, status int, code string) {
	if !slices.Contains(codes[status], code) {
		codes[status] = append(slices.Clone(codes[status]), code)
	}
	if op.Responses == nil {
		op.Responses = map[string]*huma.Response{}
	}
	resp := op.Responses[strconv.Itoa(status)]
	if resp == nil {
		resp = &huma.Response{}
		op.Responses[strconv.Itoa(status)] = resp
	}
	if resp.Content[ProblemContentType] == nil {
		if resp.Content == nil {
			resp.Content = map[string]*huma.MediaType{}
		}
		resp.Content[ProblemContentType] = &huma.MediaType{
			Schema: oapi.Components.Schemas.Schema(reflect.TypeFor[APIError](), true, "APIError"),
		}
	}
}
