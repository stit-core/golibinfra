package amocrmkit

import (
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// newTestClient собирает Client поверх реального httpclient, направленный на
// httptest-сервер (srvURL) через hostOverride. RateRPS высокий — тесты не
// троттлятся (отдельный лимит-тест задаёт свой).
func newTestClient(t *testing.T, srvURL string) *Client {
	t.Helper()
	hc, _ := httpclient.New(httpclient.Config{
		Timeout:    5 * time.Second,
		MaxRetries: 0,
		RetryWait:  time.Millisecond,
		Breaker: httpclient.BreakerConfig{
			FailureThreshold: 100,
			RecoveryTimeout:  time.Second,
		},
	})
	c, err := New(Config{Timeout: 5 * time.Second, RateRPS: 1000}, hc, clockkit.Real{}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.hostOverride = srvURL
	return c
}

func testAccount() Account {
	return Account{Subdomain: "demo", TLD: "ru", Token: "test-token"}
}
