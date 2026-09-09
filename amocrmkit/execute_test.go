package amocrmkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// 400 с validation-errors: потребитель узнаёт, какое поле какого элемента батча
// отвергнуто.
func TestValidationErrorsDecoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{
			"validation-errors":[
				{"request_id":"0","errors":[{"code":"NotSupportedChoice","path":"custom_fields_values.0.values.0.enum_id","detail":"Значение недопустимо"}]}
			],
			"title":"Bad Request","type":"https://developers.amocrm.ru/v3/errors","status":400,"detail":"Ошибка валидации"
		}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	_, err := c.CreateLeads(context.Background(), testAccount(), []LeadInput{{Name: "x"}})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("APIError not attached: %v", err)
	}
	if len(apiErr.ValidationErrors) != 1 || apiErr.ValidationErrors[0].RequestID != "0" {
		t.Fatalf("validation errors = %+v", apiErr.ValidationErrors)
	}
	fe := apiErr.ValidationErrors[0].Errors
	if len(fe) != 1 || fe[0].Code != "NotSupportedChoice" || fe[0].Path != "custom_fields_values.0.values.0.enum_id" {
		t.Errorf("field errors = %+v", fe)
	}
}

// 402 — состояние аккаунта (не оплачен), а не ошибка данных запроса.
func TestPaymentRequiredClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"title":"Payment Required","status":402}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	_, err := c.ListLeads(context.Background(), testAccount(), ListParams{})
	if !errors.Is(err, ErrPaymentRequired) {
		t.Fatalf("err = %v, want ErrPaymentRequired", err)
	}
	if errors.Is(err, ErrValidation) {
		t.Error("402 must not be classified as a request-data error")
	}
}

// Транспортная ошибка: ответа нет — метрика status="error" и лог о сбое.
func TestTransportErrorLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // соединение будет отвергнуто

	log := &captureLogger{}
	c, err := New(Config{RateRPS: 1000, HostOverride: srv.URL}, testHTTPConfig(), clockkit.Real{}, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err == nil {
		t.Fatal("expected a transport error")
	}
	if e, ok := log.find("warn", "amocrmkit: request failed"); !ok || e.kv["endpoint"] != "leads.list" {
		t.Errorf("request-failed warn = %+v, ok=%v", e, ok)
	}
}

// Одиночная сущность: 204 неотличим от «не найдено», поэтому ErrNotFound.
func TestGetOneEmptyBodyIsNotFound(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"204":        func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		"empty body": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			c := newTestClient(t, srv.URL)
			ctx := context.Background()

			if _, err := c.GetTalk(ctx, testAccount(), 7); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetTalk: err = %v, want ErrNotFound", err)
			}
			if _, err := c.GetAmojoID(ctx, testAccount()); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetAmojoID: err = %v, want ErrNotFound", err)
			}
			if _, err := c.GetLead(ctx, testAccount(), 1); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetLead: err = %v, want ErrNotFound", err)
			}
			// Списки и батчи: пусто — это не ошибка.
			if _, err := c.ListLeads(ctx, testAccount(), ListParams{}); err != nil {
				t.Errorf("ListLeads: %v", err)
			}
			if err := c.CloseTalk(ctx, testAccount(), 7, true); err != nil {
				t.Errorf("CloseTalk: %v", err)
			}
		})
	}
}

// Тело успешного ответа ограничено: HostOverride может смотреть в прокси, а
// drive_url приходит от сервера.
func TestResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"_embedded":{"leads":[`))
		chunk := strings.Repeat(`{"id":1},`, 4096) // ~36 KiB на итерацию
		for range (maxResponseBody / len(chunk)) + 2 {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	_, err := c.ListLeads(context.Background(), testAccount(), ListParams{})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
}

// Тело, которое не маршалится, — ошибка кита, а не сети.
func TestUnmarshalableBodyIsPermanent(t *testing.T) {
	c := newTestClient(t, "http://unused")
	_, err := c.SubmitTemplateReview(context.Background(), testAccount(), 1, map[string]any{"bad": make(chan int)})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

// Битый JSON успешного ответа — ошибка декода, а не молчаливый нулевой объект.
func TestBrokenJSONIsDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"_embedded":{"leads":[`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err == nil {
		t.Fatal("expected a decode error")
	}
}

// _embedded с неожиданной формой значения — ошибка на уровне страницы.
func TestEmbeddedWrongShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"_embedded":{"leads":{"id":1}}}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err == nil {
		t.Fatal("expected a decode error on _embedded.leads")
	}
	if _, err := c.CreateLeads(context.Background(), testAccount(), []LeadInput{{Name: "x"}}); err == nil {
		t.Fatal("expected a decode error on _embedded.leads in a batch")
	}
}
