package amocrmkit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// maxRetryAfterBody — сколько тела 429 читает транспорт ради retry_after.
const maxRetryAfterBody = 64 << 10

// rateLimitTransport поднимает retry_after из ТЕЛА ответа 429 в заголовок
// Retry-After (httpclient считает backoff только по заголовку) и штрафует
// лимитер аккаунта, чтобы ретраи и параллельные вызовы отступили вместе.
type rateLimitTransport struct {
	next http.RoundTripper
	rl   *accountLimiter
}

func (t *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusTooManyRequests {
		return resp, err
	}

	seconds := retryAfterSeconds(resp.Header, t.rl.clock.Now())
	if seconds == 0 {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRetryAfterBody))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		if readErr == nil {
			var b apiErrorBody
			if json.Unmarshal(raw, &b) == nil && b.RetryAfter > 0 {
				seconds = min(b.RetryAfter, maxRetryAfterSeconds)
				resp.Header.Set("Retry-After", strconv.Itoa(seconds))
			}
		}
	}
	t.rl.penalize(accountFrom(req.Context()).key(), time.Duration(seconds)*time.Second)
	return resp, nil
}
