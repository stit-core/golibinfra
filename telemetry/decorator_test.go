package telemetry

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	apitrace "go.opentelemetry.io/otel/trace"
)

func TestSpan_RootWithoutParentUsesGlobalProvider(t *testing.T) {
	restoreGlobals(t)
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))

	ctx, end := Span(context.Background(), "job.tick")
	if !apitrace.SpanFromContext(ctx).IsRecording() {
		t.Fatal("root span must be recording under the global SDK provider")
	}
	err := errors.New("boom")
	end(&err)

	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "job.tick" {
		t.Fatalf("ended spans = %v", spans)
	}
	if spans[0].Parent().IsValid() {
		t.Error("root span must have no parent")
	}
	if st := spans[0].Status(); st.Code != codes.Error || st.Description != "boom" {
		t.Errorf("status = %+v", st)
	}
	if evs := spans[0].Events(); len(evs) != 1 || evs[0].Name != "exception" {
		t.Errorf("events = %v, want one exception event", evs)
	}
}

func TestSpan_ChildOfRecordingParent(t *testing.T) {
	restoreGlobals(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)

	ctx, parent := tp.Tracer("t").Start(context.Background(), "parent")
	_, end := Span(ctx, "child")
	end(nil)
	parent.End()

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	child := spans[0]
	if child.Name() != "child" || child.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("child parent = %s, want %s", child.Parent().SpanID(), parent.SpanContext().SpanID())
	}
	if st := child.Status(); st.Code != codes.Unset {
		t.Errorf("nil error must leave status unset, got %+v", st)
	}
}
