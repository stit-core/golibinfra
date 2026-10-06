package httpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient — клиент на литерале теста: поля, которые тест не задал, получают тестовые
// значения (быстрые ретраи), остальное — как задано.
func newTestClient(t *testing.T, cfg Config, opts ...Option) *Client {
	t.Helper()
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.RetryWait == 0 {
		cfg.RetryWait = 5 * time.Millisecond
	}
	if cfg.RetryMaxWait == 0 {
		cfg.RetryMaxWait = max(5*time.Second, cfg.RetryWait)
	}
	if cfg.MaxRedirects == 0 {
		cfg.MaxRedirects = 10
	}
	if cfg.SSEMaxEventBytes == 0 {
		cfg.SSEMaxEventBytes = 1 << 20
	}
	if cfg.MaxHosts == 0 {
		cfg.MaxHosts = 1024
	}
	if cfg.Breaker.FailureThreshold > 0 && cfg.Breaker.RecoveryTimeout == 0 {
		cfg.Breaker.RecoveryTimeout = 30 * time.Second
	}
	c, err := New(cfg, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// rtFunc — транспорт-заглушка: ошибки и ответы без сети.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func statusServer(t *testing.T, calls *atomic.Int32, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_200NoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusOK)

	c := newTestClient(t, Config{MaxRetries: 3})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Errorf("expected 1 call, got %d", calls.Load())
	}
}

func TestClient_RetryableStatuses(t *testing.T) {
	cases := map[int]int32{
		http.StatusRequestTimeout:      3,
		http.StatusTooManyRequests:     3,
		http.StatusBadGateway:          3,
		http.StatusServiceUnavailable:  3,
		http.StatusGatewayTimeout:      3,
		http.StatusInternalServerError: 1, // детерминирован у большинства upstream'ов
		http.StatusNotImplemented:      1,
		http.StatusBadRequest:          1,
		http.StatusNotFound:            1,
	}
	for code, want := range cases {
		var calls atomic.Int32
		srv := statusServer(t, &calls, code)

		c := newTestClient(t, Config{MaxRetries: 2, Breaker: BreakerConfig{FailureThreshold: 0}})
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%d: unexpected error: %v", code, err)
		}
		resp.Body.Close()
		if calls.Load() != want {
			t.Errorf("status %d: got %d calls, want %d", code, calls.Load(), want)
		}
	}
}

func TestClient_ExhaustedRetries_ReturnsLastResponse(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream down")
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 2})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("5xx must be returned as response, got error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "upstream down" {
		t.Fatalf("body of last response must be readable, got %q", body)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 calls, got %d", calls.Load())
	}
}

func TestClient_PostWithBody_NotRetriedByDefault(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusServiceUnavailable)

	c := newTestClient(t, Config{MaxRetries: 3})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(`{"a":1}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Errorf("POST must not be retried by default, got %d calls", calls.Load())
	}
}

func TestClient_PostWithBody_RetriedWhenAllowed_BodyPreserved(t *testing.T) {
	var (
		calls  atomic.Int32
		bodies []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 3})
	ctx := AllowRetry(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(`{"a":1}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	if len(bodies) != 3 {
		t.Fatalf("calls: got %d", len(bodies))
	}
	for i, b := range bodies {
		if b != `{"a":1}` {
			t.Errorf("attempt %d: body %q, want full body on every attempt", i, b)
		}
	}
}

func TestClient_IdempotencyKeyHeader_AllowsRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 2})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader("x"))
	req.Header.Set("Idempotency-Key", "k1")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls.Load() != 2 {
		t.Errorf("expected retry with Idempotency-Key, got %d calls", calls.Load())
	}
}

func TestClient_UnrewindableBody_NotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusServiceUnavailable)

	c := newTestClient(t, Config{MaxRetries: 3, RetryNonIdempotent: true})
	pr, pw := io.Pipe()
	go func() { _, _ = pw.Write([]byte("stream")); pw.Close() }()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, srv.URL, pr)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Errorf("body without GetBody must not be retried, got %d calls", calls.Load())
	}
}

func TestClient_RewindErrorAborts(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusServiceUnavailable)

	boom := errors.New("body gone")
	c := newTestClient(t, Config{MaxRetries: 2})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, srv.URL, strings.NewReader("x"))
	req.GetBody = func() (io.ReadCloser, error) { return nil, boom }
	if _, err := c.Do(req); !errors.Is(err, boom) {
		t.Fatalf("want rewind error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 call before the failed rewind, got %d", calls.Load())
	}
}

func TestClient_AttemptTimeoutRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{Timeout: 50 * time.Millisecond, MaxRetries: 1})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("attempt timeout must be retried, got error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 calls (timeout + retry), got %d", calls.Load())
	}
}

func TestClient_AttemptTimeoutExhausted_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{Timeout: 30 * time.Millisecond, MaxRetries: 1})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if _, err := c.Do(req); err == nil {
		t.Fatal("expected timeout error after all attempts")
	}
}

func TestClient_CallerContextCanceled_NotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	defer cancel()

	c := newTestClient(t, Config{MaxRetries: 3})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	_, err := c.Do(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("canceled request must not be retried, got %d calls", calls.Load())
	}
}

func TestClient_ContextCanceledDuringBackoff(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusServiceUnavailable)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := newTestClient(t, Config{MaxRetries: 3, RetryWait: time.Second, RetryMaxWait: 2 * time.Second})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := c.Do(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled during backoff, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 call before cancel, got %d", calls.Load())
	}
}

func TestClient_TransportErrorRetried(t *testing.T) {
	var calls atomic.Int32
	transport := rtFunc(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) < 3 {
			return nil, errors.New("connection reset by peer")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    req,
		}, nil
	})

	c := newTestClient(t, Config{MaxRetries: 3}, WithTransport(transport))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://upstream.invalid/x", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("transport error must be retried: %v", err)
	}
	resp.Body.Close()
	if calls.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", calls.Load())
	}
}

func TestClient_TransportErrorExhausted(t *testing.T) {
	boom := errors.New("connection refused")
	c := newTestClient(t, Config{MaxRetries: 1}, WithTransport(rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, boom
	})))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://upstream.invalid/x", nil)
	_, err := c.Do(req)
	if !errors.Is(err, boom) {
		t.Fatalf("want wrapped transport error, got %v", err)
	}
}

func TestClient_PermanentTransportError_NotRetried(t *testing.T) {
	cases := map[string]error{
		"tls verification": &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
		"unknown ca":       x509.UnknownAuthorityError{},
		"expired cert":     x509.CertificateInvalidError{Reason: x509.Expired, Detail: "expired"},
	}
	for name, cause := range cases {
		var calls atomic.Int32
		c := newTestClient(t, Config{MaxRetries: 3}, WithTransport(rtFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, cause
		})))
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://upstream.invalid/x", nil)
		if _, err := c.Do(req); err == nil {
			t.Fatalf("%s: expected error", name)
		}
		if calls.Load() != 1 {
			t.Errorf("%s: permanent error must not be retried, got %d attempts", name, calls.Load())
		}
	}
}

func TestClient_UnsupportedSchemeNotRetried(t *testing.T) {
	var attempts atomic.Int32
	c := newTestClient(t, Config{MaxRetries: 3}, WithBeforeAttempt(func(context.Context, int) error {
		attempts.Add(1)
		return nil
	}))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "ftp://upstream.invalid/x", nil)
	if _, err := c.Do(req); err == nil {
		t.Fatal("expected error on unsupported scheme")
	}
	if attempts.Load() != 1 {
		t.Errorf("unsupported scheme must not be retried, got %d attempts", attempts.Load())
	}
}

func TestPermanentErr(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"unsupported scheme": {&url.Error{Op: "Get", Err: errors.New(`unsupported protocol scheme "ftp"`)}, true},
		"no host":            {&url.Error{Op: "Get", Err: errors.New("http: no Host in request URL")}, true},
		"redirect limit":     {&url.Error{Op: "Get", Err: fmt.Errorf("%w: limit 10", ErrTooManyRedirects)}, true},
		"cross host":         {fmt.Errorf("%w: a -> b", ErrCrossHostRedirect), true},
		"tls":                {&url.Error{Op: "Get", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}, true},
		"connection reset":   {&url.Error{Op: "Get", Err: errors.New("connection reset by peer")}, false},
		"deadline":           {&url.Error{Op: "Get", Err: context.DeadlineExceeded}, false},
		"plain":              {errors.New("boom"), false},
	}
	for name, tc := range cases {
		if got := permanentErr(tc.err); got != tc.want {
			t.Errorf("%s: permanentErr = %v, want %v", name, got, tc.want)
		}
	}
}

func TestClient_BeforeAttemptCalledPerAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var attempts []int
	c := newTestClient(t, Config{MaxRetries: 3}, WithBeforeAttempt(func(_ context.Context, attempt int) error {
		attempts = append(attempts, attempt)
		return nil
	}))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if len(attempts) != 3 || attempts[0] != 0 || attempts[2] != 2 {
		t.Errorf("before-attempt calls: %v", attempts)
	}
}

func TestClient_BeforeAttemptErrorAborts(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusOK)

	limited := errors.New("rate limited")
	c := newTestClient(t, Config{MaxRetries: 0}, WithBeforeAttempt(func(context.Context, int) error { return limited }))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err := c.Do(req)
	if !errors.Is(err, limited) {
		t.Fatalf("want before-attempt error, got %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("request must not be sent, got %d calls", calls.Load())
	}
}

// Регрессия KX50: hook'и не заменяют друг друга — лимитер и подпись срабатывают оба, в
// порядке опций.
func TestClient_BeforeAttemptHooksChainInOrder(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusOK)

	var order []string
	hook := func(name string) Option {
		return WithBeforeAttempt(func(context.Context, int) error {
			order = append(order, name)
			return nil
		})
	}
	c := newTestClient(t, Config{}, hook("limiter"), hook("signer"))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if strings.Join(order, ",") != "limiter,signer" {
		t.Fatalf("hooks ran %v, want limiter,signer", order)
	}
}

// Транспорт у клиента один: второй WithTransport не теряется молча, а валит сборку.
func TestNew_SecondTransportIsErrConfig(t *testing.T) {
	rt := rtFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") })
	_, err := New(validConfig(), WithTransport(rt), WithTransport(rt))
	if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "WithTransport") {
		t.Fatalf("New with two transports = %v, want ErrConfig naming WithTransport", err)
	}
}

func TestNew_NilOptionsIgnored(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusOK)

	var hooked atomic.Int32
	c, err := New(validConfig(), nil, WithTransport(nil), WithBeforeAttempt(nil),
		WithBeforeAttempt(func(context.Context, int) error { hooked.Add(1); return nil }), WithBeforeAttempt(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := c.base.(*http.Transport); !ok {
		t.Fatalf("WithTransport(nil) must keep the default transport, got %T", c.base)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if hooked.Load() != 1 {
		t.Fatalf("hook calls = %d, want 1: WithBeforeAttempt(nil) must not drop the hook", hooked.Load())
	}
}

func TestClient_RequestIDForward(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-Request-ID"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 0})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	req.Header.Set("X-Request-ID", "explicit-id")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	ctx := contextWithRequestID(context.Background(), "from-ctx")
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if got[0] != "explicit-id" || got[1] != "from-ctx" {
		t.Errorf("X-Request-ID: got %v", got)
	}
}

func TestClient_RetryAfterHonored(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 1, RetryWait: time.Minute, RetryMaxWait: time.Minute})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	// Retry-After: 0 бьёт минутный backoff — иначе тест не уложился бы в секунду.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Retry-After ignored: retried after %v", elapsed)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", calls.Load())
	}
}

func TestRetryAfter(t *testing.T) {
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	cases := []struct {
		name  string
		value string
		ok    bool
		min   time.Duration
		max   time.Duration
	}{
		{name: "empty", value: "", ok: false},
		{name: "seconds", value: "3", ok: true, min: 3 * time.Second, max: 3 * time.Second},
		{name: "zero", value: "0", ok: true},
		{name: "negative", value: "-5", ok: false},
		{name: "garbage", value: "soon", ok: false},
		{name: "http date", value: future, ok: true, min: time.Second, max: 3 * time.Second},
		{name: "http date in the past", value: past, ok: true},
		{name: "absurd seconds capped", value: "99999999999", ok: true, min: maxRetryAfter, max: maxRetryAfter},
		// Регрессия (NF6): секунды, не влезающие в time.Duration, заворачивались
		// в отрицательную паузу — повтор уходил мгновенно, backoff исчезал.
		{name: "seconds just over Duration", value: "9223372037", ok: true, min: maxRetryAfter, max: maxRetryAfter},
		{name: "seconds far over Duration", value: "10000000000", ok: true, min: maxRetryAfter, max: maxRetryAfter},
		{name: "seconds over uint64", value: "184467440737095516160", ok: true, min: maxRetryAfter, max: maxRetryAfter},
	}
	for _, tc := range cases {
		got, ok := retryAfter(tc.value)
		if ok != tc.ok {
			t.Errorf("%s: ok=%v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if got < 0 {
			t.Errorf("%s: negative delay %v", tc.name, got)
		}
		if got < tc.min || got > tc.max {
			t.Errorf("%s: got %v, want [%v, %v]", tc.name, got, tc.min, tc.max)
		}
	}
}

func TestBackoff_CappedAndJittered(t *testing.T) {
	c := newTestClient(t, Config{MaxRetries: 5, RetryWait: 100 * time.Millisecond, RetryMaxWait: 250 * time.Millisecond})
	for attempt := 1; attempt <= 40; attempt++ {
		d := c.backoff(attempt, nil)
		if d < 100*time.Millisecond || d > 250*time.Millisecond {
			t.Fatalf("attempt %d: backoff %v out of [100ms, 250ms]", attempt, d)
		}
	}
}

func TestBackoff_NoOverflowOnLargeBase(t *testing.T) {
	c := newTestClient(t, Config{MaxRetries: 5, RetryWait: 20 * time.Second, RetryMaxWait: time.Minute})
	for _, attempt := range []int{1, 2, 30, 40, 62, 63, 100} {
		if d := c.backoff(attempt, nil); d <= 0 || d > time.Minute {
			t.Fatalf("attempt %d: backoff %v out of (0, 1m]", attempt, d)
		}
	}
}

func TestBackoff_RetryAfterCappedByMaxWait(t *testing.T) {
	c := newTestClient(t, Config{MaxRetries: 1, RetryWait: time.Millisecond, RetryMaxWait: 2 * time.Second})
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"600"}}}
	if d := c.backoff(1, resp); d != 2*time.Second {
		t.Fatalf("Retry-After must be capped by RetryMaxWait, got %v", d)
	}
	absurd := &http.Response{Header: http.Header{"Retry-After": []string{"9223372037"}}}
	if d := c.backoff(1, absurd); d != 2*time.Second {
		t.Fatalf("overflowing Retry-After must stay capped, got %v", d)
	}
}

func validConfig() Config {
	return Config{
		Timeout: 10 * time.Second, MaxRetries: 3, RetryWait: 200 * time.Millisecond, RetryMaxWait: 5 * time.Second,
		MaxRedirects: 10, SSEMaxEventBytes: 1 << 20, MaxHosts: 1024,
		Breaker: BreakerConfig{FailureThreshold: 5, RecoveryTimeout: 30 * time.Second},
	}
}

func TestConfig_ValidateRejects(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := map[string]func(*Config){
		"zero timeout":         func(c *Config) { c.Timeout = 0 },
		"negative timeout":     func(c *Config) { c.Timeout = -time.Second },
		"negative retries":     func(c *Config) { c.MaxRetries = -1 },
		"zero retry wait":      func(c *Config) { c.RetryWait = 0 },
		"max wait below base":  func(c *Config) { c.RetryWait, c.RetryMaxWait = 10*time.Second, time.Millisecond },
		"negative redirects":   func(c *Config) { c.MaxRedirects = -1 },
		"zero sse limit":       func(c *Config) { c.SSEMaxEventBytes = 0 },
		"negative threshold":   func(c *Config) { c.Breaker.FailureThreshold = -1 },
		"negative recovery":    func(c *Config) { c.Breaker.RecoveryTimeout = -time.Second },
		"zero recovery on":     func(c *Config) { c.Breaker.RecoveryTimeout = 0 },
		"negative max backoff": func(c *Config) { c.RetryMaxWait = -time.Second },
		"zero max hosts":       func(c *Config) { c.MaxHosts = 0 },
		"max hosts below -1":   func(c *Config) { c.MaxHosts = -2 },
	}
	for name, mutate := range cases {
		cfg := validConfig()
		mutate(&cfg)
		if _, err := New(cfg); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: want ErrConfig, got %v", name, err)
		}
	}
}

// Ноль — значение, а не дефолт: литерал Config{} — ErrConfig (клиент без таймаута); MaxRetries
// и MaxRedirects 0 и выключенный breaker без RecoveryTimeout — допустимы.
func TestConfig_ZeroIsNotDefault(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "HTTPCLIENT_TIMEOUT") {
		t.Fatalf("New(Config{}) = %v, want ErrConfig naming HTTPCLIENT_TIMEOUT", err)
	}
	cfg := validConfig()
	cfg.MaxRetries, cfg.MaxRedirects, cfg.Breaker = 0, 0, BreakerConfig{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("zero retries, redirects and breaker off: %v", err)
	}
}

func TestClient_Close_ClosesIdleConnections(t *testing.T) {
	var closed atomic.Bool
	c := newTestClient(t, Config{}, WithTransport(closableTransport{closed: &closed}))
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !closed.Load() {
		t.Error("Close must close idle connections of the base transport")
	}
}

type closableTransport struct{ closed *atomic.Bool }

func (t closableTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func (t closableTransport) CloseIdleConnections() { t.closed.Store(true) }

func TestClient_DefaultTransportIsOwnClone(t *testing.T) {
	c := newTestClient(t, Config{})
	tr, ok := c.base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport: got %T", c.base)
	}
	if tr == http.DefaultTransport {
		t.Error("client must not share the process-wide http.DefaultTransport")
	}
	if tr.MaxIdleConnsPerHost != maxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost: got %d, want %d", tr.MaxIdleConnsPerHost, maxIdleConnsPerHost)
	}
}

func TestClient_SameHostRedirectFollowed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 0})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("same-host redirect must be followed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || hits.Load() != 2 {
		t.Fatalf("status %d after %d hits", resp.StatusCode, hits.Load())
	}
}

// MaxRedirects 0 — значение, а не дефолт: первый же редирект — ErrTooManyRedirects.
func TestClient_ZeroMaxRedirectsFollowsNone(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	cfg := validConfig()
	cfg.MaxRetries, cfg.MaxRedirects = 0, 0
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if _, err := c.Do(req); !errors.Is(err, ErrTooManyRedirects) || hits.Load() != 1 {
		t.Fatalf("err = %v after %d hits, want ErrTooManyRedirects after 1", err, hits.Load())
	}
}

func TestClient_TooManyRedirects(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, fmt.Sprintf("/next/%d", hits.Load()), http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 2, MaxRedirects: 3})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err := c.Do(req)
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("want ErrTooManyRedirects, got %v", err)
	}
	if hits.Load() != 3 {
		t.Errorf("permanent redirect loop must not be retried, got %d hits", hits.Load())
	}
}

func TestClient_CrossHostRedirectBlocked(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 0})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if _, err := c.Do(req); !errors.Is(err, ErrCrossHostRedirect) {
		t.Fatalf("want ErrCrossHostRedirect, got %v", err)
	}
}

func TestClient_CrossHostRedirectStripsSecrets(t *testing.T) {
	var got http.Header
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/next", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 0, AllowCrossHostRedirects: true})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Api-Key", "secret-key")
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("cross-host redirect must be allowed: %v", err)
	}
	resp.Body.Close()
	for _, h := range []string{"Authorization", "X-Api-Key"} {
		if v := got.Get(h); v != "" {
			t.Errorf("%s leaked to another host: %q", h, v)
		}
	}
	if got.Get("Accept") != "application/json" {
		t.Errorf("Accept must survive the redirect, got %q", got.Get("Accept"))
	}
}
