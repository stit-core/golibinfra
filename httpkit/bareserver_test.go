package httpkit_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

const bareServerEnv = "HTTPKIT_TEST_BARE_SERVER"

// Регрессия (NF1): нейтрализация 5xx — свойство NewServer, а не опции WithLogger;
// документированный путь «Без fx» не должен отдавать клиенту текст ошибки с DSN.
// Хук huma ставится один раз на процесс, поэтому сценарий гоняется в чистом
// процессе: дочерний прогон этого же теста собирает только голый сервер.
func TestServer_NeutralizesUnmappedErrorWithoutLogger(t *testing.T) {
	if os.Getenv(bareServerEnv) == "1" {
		bareServerScenario(t)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), bareServerEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bare NewServer(cfg) scenario failed: %v\n%s", err, out)
	}
}

func bareServerScenario(t *testing.T) {
	t.Helper()
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
