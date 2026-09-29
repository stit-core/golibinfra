package httpkit_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// Регрессия (NF1): нейтрализация 5xx — свойство пакета, а не опции WithLogger:
// документированный путь «Без fx» не отдаёт клиенту текст ошибки с DSN.
func TestServer_NeutralizesUnmappedErrorWithoutLogger(t *testing.T) {
	// Ровно проводка из docs/usage.md § Прямой запуск без fx, но без WithLogger.
	s := httpkit.NewServer(testConfig(t, "127.0.0.1:0"))
	huma.Get(s.BindAPI(), "/boom", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, errors.New("pq: dial tcp 10.0.0.5:5432 user=admin password=s3cret")
	})
	if err := s.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(stopCtx)
	})

	resp := get(t, s, "/boom")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if strings.Contains(body, "password") || strings.Contains(body, "10.0.0.5") {
		t.Fatalf("handler error text leaked to the client: %s", body)
	}
	if !strings.Contains(body, `"code":"INTERNAL"`) {
		t.Fatalf("500 body must carry a code: %s", body)
	}
}

// humatest в тестах потребителя — та же форма ошибок: ошибки huma подменяет импорт httpkit.
func TestHumatest_NeutralizesUnmappedError(t *testing.T) {
	_, api := humatest.New(t, huma.DefaultConfig("t", "0"))
	huma.Get(api, "/boom", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, errors.New("pq: password=s3cret")
	})
	resp := api.Get("/boom")
	if resp.Code != http.StatusInternalServerError || resp.Header().Get("Content-Type") != httpkit.ProblemContentType {
		t.Fatalf("want 500 problem+json, got %d %v", resp.Code, resp.Header())
	}
	if body := resp.Body.String(); strings.Contains(body, "s3cret") || !strings.Contains(body, `"code":"INTERNAL"`) {
		t.Fatalf("humatest must get the kit's neutral 500: %s", body)
	}
}
