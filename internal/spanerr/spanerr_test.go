package spanerr_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.stit.tech/stit-core/golibinfra/internal/spanerr"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

const (
	email  = "ivan.petrov@example.com"
	bearer = "Bearer sk_live_51Mabcdef0123456789abcdef0123"
)

var errCharge = fmt.Errorf("charge customer %s: provider rejected Authorization: %s", email, bearer)

type foreignLogger struct{ logkit.Logger }

// Регрессия ROBS-03: текст ошибки в спане проходит те же маски, что запись лога этого логгера;
// свой набор масок действует и на спан, выключенный redact текст не трогает, а Nop и логгер не
// из logkit берут дефолтные маски.
func TestRecord_MasksErrorTextLikeTheLogger(t *testing.T) {
	logger := func(opts ...logkit.Option) logkit.Logger {
		l, err := logkit.New(logkit.Config{Level: "info"}, append(opts, logkit.WithWriter(io.Discard), logkit.WithoutOTel())...)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	for name, c := range map[string]struct {
		log    logkit.Logger
		hidden []string
		shown  []string
	}{
		"default masks":          {log: logger(), hidden: []string{email, "sk_live"}},
		"logger with attributes": {log: logger().With("kit", "x"), hidden: []string{email, "sk_live"}},
		"custom masks":           {log: logger(logkit.WithRedact(logkit.RedactConfig{Patterns: logkit.Patterns{`customer \S+`}})), hidden: []string{email}, shown: []string{"sk_live"}},
		"redact disabled":        {log: logger(logkit.WithRedact(logkit.RedactConfig{Disabled: true})), shown: []string{email, "sk_live"}},
		"nop logger":             {log: logkit.Nop(), hidden: []string{email, "sk_live"}},
		"foreign logger":         {log: foreignLogger{logkit.Nop()}, hidden: []string{email, "sk_live"}},
		"foreign slog handler":   {log: logkit.FromSlog(slog.New(slog.DiscardHandler)), shown: []string{email, "sk_live"}},
	} {
		t.Run(name, func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			_, span := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("t").Start(context.Background(), "job")
			spanerr.Record(span, errCharge, c.log)
			span.End()

			got := rec.Ended()[0]
			var typ, message string
			for _, e := range got.Events() {
				if e.Name != "exception" {
					continue
				}
				for _, kv := range e.Attributes {
					switch kv.Key {
					case "exception.type":
						typ = kv.Value.AsString()
					case "exception.message":
						message = kv.Value.AsString()
					}
				}
			}
			if got.Status().Code != codes.Error || got.Status().Description != message || message == "" {
				t.Fatalf("status %v %q, exception.message %q: want the Error status with the same text", got.Status().Code, got.Status().Description, message)
			}
			if typ != "*fmt.wrapError" && typ != "*errors.errorString" {
				t.Errorf("exception.type = %q, want the error type as span.RecordError names it", typ)
			}
			for _, secret := range c.hidden {
				if strings.Contains(message, secret) {
					t.Errorf("span text leaks %q: %s", secret, message)
				}
			}
			for _, plain := range c.shown {
				if !strings.Contains(message, plain) {
					t.Errorf("span text lost %q: %s", plain, message)
				}
			}
			if !strings.Contains(message, "provider rejected") {
				t.Errorf("span text lost the error itself: %s", message)
			}
		})
	}
}

func TestText_KeepsPlainErrors(t *testing.T) {
	if got := spanerr.Text(errors.New("order 42 not found"), logkit.Nop()); got != "order 42 not found" {
		t.Fatalf("Text = %q, want the error text unchanged", got)
	}
}
