package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/redact"
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

// attemptTransport — спан попытки (каждый RoundTrip, в том числе шаг редиректа) до заголовков
// ответа и W3C trace-context в upstream; url.full — без секретов: спан otelhttp писал бы URL с
// query целиком.
type attemptTransport struct {
	next   http.RoundTripper
	tracer trace.Tracer
}

func (t *attemptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(req.Method),
		semconv.ServerAddress(req.URL.Hostname()),
		semconv.URLFull(redact.URL(req.URL.String())),
	}
	if port, err := strconv.Atoi(req.URL.Port()); err == nil {
		attrs = append(attrs, semconv.ServerPort(port))
	}
	if n, _ := req.Context().Value(resendCountKey{}).(int); n > 0 {
		attrs = append(attrs, semconv.HTTPRequestResendCount(n))
	}
	ctx, span := t.tracer.Start(req.Context(), "HTTP "+req.Method, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()
	// RoundTripper не меняет запрос вызывающего: trace-context — в заголовки копии.
	req = req.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := t.next.RoundTrip(req)
	switch {
	case err != nil:
		span.SetAttributes(semconv.ErrorTypeKey.String(fmt.Sprintf("%T", err)))
		span.SetStatus(codes.Error, err.Error())
	case resp.StatusCode >= http.StatusBadRequest:
		span.SetAttributes(semconv.HTTPResponseStatusCode(resp.StatusCode))
		span.SetStatus(codes.Error, http.StatusText(resp.StatusCode))
	default:
		span.SetAttributes(semconv.HTTPResponseStatusCode(resp.StatusCode))
	}
	return resp, err
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
