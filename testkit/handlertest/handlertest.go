// Package handlertest — хелперы для тестов huma-хендлеров: in-memory API сервера httpkit с тем
// же контрактом ошибок, auth-гейтом и проверкой маршрутов, что у сервера, и разбор тела ошибки
// httpkit.APIError.
package handlertest

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// API — huma.API с записью ответов; алиас humatest.TestAPI.
type API = humatest.TestAPI

// Option — опция NewAPI: то, что сервер берёт из графа fx.
type Option func(*options)

type options struct {
	cfg     *httpkit.Config
	log     logkit.Logger
	extract httpkit.AuthExtractor
	schemes []httpkit.SecuritySchemeEntry
}

// WithConfig — конфиг сервера вместо httpkit.NewConfig(); невалидный — t.Fatalf.
func WithConfig(cfg httpkit.Config) Option {
	return func(o *options) { o.cfg = &cfg }
}

// WithLogger — логгер сервера: строка handler error с причиной 5xx и request_id запроса,
// отказы HumaAuth. Без опции — logkit.Nop().
func WithLogger(log logkit.Logger) Option {
	return func(o *options) { o.log = log }
}

// WithAuth — AuthExtractor сервера: защищённая операция без credentials отвечает 401 (или кодом
// extractor'а). Без опции защищённая операция отвечает 500 AUTH_NOT_CONFIGURED.
func WithAuth(extract httpkit.AuthExtractor) Option {
	return func(o *options) { o.extract = extract }
}

// WithSecuritySchemes — схемы безопасности сервера: с ними проверка маршрутов требует у каждой
// операции Security из components или httpkit.Public, как старт Module.
func WithSecuritySchemes(schemes ...httpkit.SecuritySchemeEntry) Option {
	return func(o *options) { o.schemes = append(o.schemes, schemes...) }
}

// NewAPI поднимает in-memory huma.API сервера httpkit и регистрирует на нём хендлеры register:
// HumaAuth стоит до маршрутов, ответы-ошибки — как у сервера (request_id, Retry-After, 404/405
// маршрутизатора, коды ErrorMap.Declare в OpenAPI), маршруты проходят проверку старта Module
// (httpkit.Server.CheckRoutes, иначе t.Fatalf). Конфиг по умолчанию — httpkit.NewConfig()
// (ENV HTTP_* процесса теста). Middleware цепочки, кроме RequestID, в нём нет; операции,
// добавленные на API после NewAPI, проверку маршрутов не проходят.
func NewAPI(t testing.TB, register func(api huma.API), opts ...Option) API {
	t.Helper()
	o := options{log: logkit.Nop()}
	for _, opt := range opts {
		opt(&o)
	}
	cfg, err := config(o.cfg)
	if err != nil {
		t.Fatalf("handlertest: %v", err)
	}
	s := httpkit.NewServer(cfg,
		httpkit.WithLogger(o.log),
		httpkit.WithSecuritySchemes(o.schemes...),
		httpkit.WithMiddleware(httpkit.RequestID()),
	)
	api := s.BindAPI()
	api.UseMiddleware(httpkit.HumaAuth(o.log, o.extract))
	register(api)
	if err := s.CheckRoutes(); err != nil {
		t.Fatalf("handlertest: %v", err)
	}
	return humatest.Wrap(t, api)
}

func config(cfg *httpkit.Config) (httpkit.Config, error) {
	if cfg == nil {
		return httpkit.NewConfig()
	}
	return *cfg, cfg.Validate()
}

// DecodeAPIError разбирает тело ответа как httpkit.APIError; невалидный JSON — t.Fatalf.
func DecodeAPIError(t testing.TB, resp *httptest.ResponseRecorder) httpkit.APIError {
	t.Helper()
	body := resp.Body.Bytes()
	var got httpkit.APIError
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("handlertest: decode api error: %v\nbody: %s", err, body)
	}
	return got
}

// AssertStatus проверяет код ответа.
func AssertStatus(t testing.TB, resp *httptest.ResponseRecorder, want int) {
	t.Helper()
	if resp.Code != want {
		t.Fatalf("handlertest: status = %d, want %d (body: %s)", resp.Code, want, resp.Body.String())
	}
}

// AssertAPIError проверяет ответ-ошибку по контракту httpkit: статус,
// application/problem+json, поле `code` и request_id, равный X-Request-ID ответа.
func AssertAPIError(t testing.TB, resp *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	AssertStatus(t, resp, status)
	if ct := resp.Header().Get("Content-Type"); ct != httpkit.ProblemContentType {
		t.Fatalf("handlertest: content-type = %q, want %q (body: %s)", ct, httpkit.ProblemContentType, resp.Body.String())
	}
	got := DecodeAPIError(t, resp)
	if got.Code != code {
		t.Fatalf("handlertest: api error code = %q, want %q (full: %+v)", got.Code, code, got)
	}
	if id := resp.Header().Get(httpkit.HeaderRequestID); got.RequestID != id {
		t.Fatalf("handlertest: request_id = %q, want X-Request-ID %q", got.RequestID, id)
	}
}
