package httpclient

import (
	"context"
	"fmt"
	"net/http"

	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/requestid"
)

// maxIdleConnsPerHost — глобальный http.DefaultTransport держит всего 2
// keep-alive соединения на хост; при параллельных вызовах к одному upstream'у
// остальные закрываются сразу после ответа.
const maxIdleConnsPerHost = 100

func defaultTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t := base.Clone()
	t.MaxIdleConnsPerHost = maxIdleConnsPerHost
	return t
}

type resendCountKey struct{}

func withResendCount(req *http.Request, attempt int) *http.Request {
	if attempt == 0 {
		return req
	}
	return req.WithContext(context.WithValue(req.Context(), resendCountKey{}, attempt))
}

// resendTransport ставит http.request.resend_count на спан попытки: otelhttp
// заводит спан на каждый RoundTrip, но номера попытки не знает.
type resendTransport struct{ next http.RoundTripper }

func (t *resendTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if n, _ := req.Context().Value(resendCountKey{}).(int); n > 0 {
		trace.SpanFromContext(req.Context()).SetAttributes(semconv.HTTPRequestResendCount(n))
	}
	return t.next.RoundTrip(req)
}

// checkRedirect — политика редиректов обоих http.Client'ов клиента.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= c.cfg.MaxRedirects {
		return fmt.Errorf("%w: limit %d", ErrTooManyRedirects, c.cfg.MaxRedirects)
	}
	if req.URL.Host == via[0].URL.Host {
		return nil
	}
	if !c.cfg.AllowCrossHostRedirects {
		return fmt.Errorf("%w: %s -> %s", ErrCrossHostRedirect, via[0].URL.Host, req.URL.Host)
	}
	stripUnsafeHeaders(req.Header)
	return nil
}

// safeRedirectHeaders — единственные заголовки, которые переезжают на чужой
// хост: любой другой может оказаться секретом (net/http сам снимает только
// Authorization, Cookie, WWW-Authenticate и Proxy-*).
var safeRedirectHeaders = map[string]bool{
	"Accept":          true,
	"Accept-Encoding": true,
	"Accept-Language": true,
	"Content-Length":  true,
	"Content-Type":    true,
	"Referer":         true,
	"User-Agent":      true,
	http.CanonicalHeaderKey(requestid.Header): true,
}

func stripUnsafeHeaders(h http.Header) {
	for k := range h {
		if !safeRedirectHeaders[http.CanonicalHeaderKey(k)] {
			delete(h, k)
		}
	}
}
