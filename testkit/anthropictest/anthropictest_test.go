package anthropictest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"git.stit.tech/stit-core/golibinfra/anthropickit"
	"git.stit.tech/stit-core/golibinfra/testkit/anthropictest"
)

func drain(t *testing.T, s anthropickit.Stream) []anthropickit.StreamEvent {
	t.Helper()
	var events []anthropickit.StreamEvent
	for s.Next() {
		events = append(events, s.Event())
	}
	return events
}

func TestStub_Defaults(t *testing.T) {
	stub := &anthropictest.Stub{}

	resp, err := stub.Complete(t.Context(), anthropickit.Request{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "stub" || resp.Role != anthropickit.RoleAssistant {
		t.Fatalf("Complete = %+v", resp)
	}
	if resp.StopReason != anthropickit.StopEndTurn || !resp.CostKnown {
		t.Fatalf("defaults must be safe: %+v", resp)
	}

	n, err := stub.CountTokens(t.Context(), anthropickit.Request{})
	if n != 0 || err != nil {
		t.Fatalf("CountTokens = %d, %v", n, err)
	}

	stream := stub.Stream(t.Context(), anthropickit.Request{})
	defer func() { _ = stream.Close() }()
	events := drain(t, stream)
	if len(events) != 2 || events[0].Type != anthropickit.EventTextDelta || events[1].Type != anthropickit.EventDone {
		t.Fatalf("default stream events = %+v", events)
	}
	final, err := stream.Final()
	if err != nil || final.Text() != "stub" {
		t.Fatalf("Final = %+v, err = %v", final, err)
	}
}

func TestStub_FuncsOverrideDefaults(t *testing.T) {
	want := errors.New("boom")
	stub := &anthropictest.Stub{
		CompleteFunc: func(context.Context, anthropickit.Request) (*anthropickit.Response, error) {
			return nil, want
		},
		CountTokensFunc: func(context.Context, anthropickit.Request) (int64, error) {
			return 42, nil
		},
		StreamFunc: func(context.Context, anthropickit.Request) anthropickit.Stream {
			return anthropictest.ErrStream(want)
		},
	}

	if _, err := stub.Complete(t.Context(), anthropickit.Request{}); !errors.Is(err, want) {
		t.Errorf("Complete err = %v, want %v", err, want)
	}
	if n, _ := stub.CountTokens(t.Context(), anthropickit.Request{}); n != 42 {
		t.Errorf("CountTokens = %d, want 42", n)
	}
	if err := stub.Stream(t.Context(), anthropickit.Request{}).Err(); !errors.Is(err, want) {
		t.Errorf("Stream err = %v, want %v", err, want)
	}
}

func TestScriptStream_ReplaysEventsInOrder(t *testing.T) {
	final := &anthropickit.Response{
		Role:       anthropickit.RoleAssistant,
		StopReason: anthropickit.StopMaxTokens,
		Content:    []anthropickit.Block{anthropickit.TextBlock{Text: "hi there"}},
	}
	stream := anthropictest.ScriptStream(
		final,
		anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "hi "},
		anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "there"},
		anthropickit.StreamEvent{Type: anthropickit.EventDone},
	)

	events := drain(t, stream)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if events[0].Text != "hi " || events[1].Text != "there" || events[2].Type != anthropickit.EventDone {
		t.Fatalf("event order broken: %+v", events)
	}
	if stream.Next() {
		t.Fatal("Next must stay false after the script is exhausted")
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
	got, err := stream.Final()
	if err != nil || got != final {
		t.Fatalf("Final = %+v, err = %v", got, err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

func TestErrStream(t *testing.T) {
	want := errors.New("upstream down")
	stream := anthropictest.ErrStream(want)

	if stream.Next() {
		t.Fatal("Next must be false on an errored stream")
	}
	if !errors.Is(stream.Err(), want) {
		t.Fatalf("Err = %v, want %v", stream.Err(), want)
	}
	resp, err := stream.Final()
	if resp != nil || !errors.Is(err, want) {
		t.Fatalf("Final = %+v, %v", resp, err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

func TestStub_ImplementsAPI(t *testing.T) {
	var api anthropickit.API = &anthropictest.Stub{}
	if _, err := api.CountTokens(t.Context(), anthropickit.Request{}); err != nil {
		t.Fatalf("Stub must satisfy anthropickit.API: %v", err)
	}
	if n := len(api.Raw().Options); n != 0 {
		t.Fatalf("zero Stub must expose the zero anthropic.Client, got %d options", n)
	}

	wired := &anthropictest.Stub{RawClient: anthropic.NewClient(option.WithAPIKey("k"))}
	if len(wired.Raw().Options) == 0 {
		t.Fatal("Raw must return RawClient as is")
	}
}

func TestScriptStream_EventBeforeNextIsZero(t *testing.T) {
	stream := anthropictest.ScriptStream(nil,
		anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "hi"})

	if got := stream.Event(); got != (anthropickit.StreamEvent{}) {
		t.Fatalf("Event() before Next() = %+v, want the zero event", got)
	}
	if !stream.Next() {
		t.Fatal("Next() = false on a scripted event")
	}
	if stream.Event().Text != "hi" {
		t.Fatalf("Event() = %+v", stream.Event())
	}
}

func TestScriptStream_FinalBeforeDrain(t *testing.T) {
	stream := anthropictest.ScriptStream(
		&anthropickit.Response{},
		anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "hi"},
		anthropickit.StreamEvent{Type: anthropickit.EventDone},
	)

	if _, err := stream.Final(); !errors.Is(err, anthropickit.ErrStreamNotDrained) {
		t.Fatalf("Final() before drain = %v, want ErrStreamNotDrained", err)
	}
	drain(t, stream)
	if _, err := stream.Final(); err != nil {
		t.Fatalf("Final() after drain = %v", err)
	}
}

func TestScriptStreamErr_BreaksMidStream(t *testing.T) {
	want := errors.New("upstream reset")
	usage := anthropickit.Usage{InputTokens: 25, OutputTokens: 7, CacheReadTokens: 4}
	stream := anthropictest.ScriptStreamErr(
		want,
		anthropickit.StreamEvent{Type: anthropickit.EventMessageStart, Usage: &usage},
		anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "half "},
	)
	defer func() { _ = stream.Close() }()

	if err := stream.Err(); err != nil {
		t.Fatalf("Err() before the script is played = %v, want nil", err)
	}
	if events := drain(t, stream); len(events) != 2 {
		t.Fatalf("events = %d, want 2 before the break", len(events))
	}
	if !errors.Is(stream.Err(), want) {
		t.Fatalf("Err() = %v, want %v", stream.Err(), want)
	}
	if got := stream.Usage(); got != usage {
		t.Fatalf("Usage() = %+v, want the usage accumulated before the break %+v", got, usage)
	}
	if _, err := stream.Final(); !errors.Is(err, want) {
		t.Fatalf("Final() err = %v, want %v", err, want)
	}
}

func TestScriptStream_NextAfterCloseIsFalse(t *testing.T) {
	stream := anthropictest.ScriptStream(
		&anthropickit.Response{},
		anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "hi"},
		anthropickit.StreamEvent{Type: anthropickit.EventDone},
	)

	if !stream.Next() {
		t.Fatal("Next() = false on the first event")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if stream.Next() {
		t.Fatal("Next() must stay false after Close()")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close must be idempotent: %v", err)
	}
}

func TestStub_DefaultModel(t *testing.T) {
	if got := (&anthropictest.Stub{}).DefaultModel(); got != anthropictest.DefaultModel {
		t.Fatalf("DefaultModel() = %q, want %q", got, anthropictest.DefaultModel)
	}
	stub := &anthropictest.Stub{DefaultModelName: "claude-haiku-4-5"}
	if got := stub.DefaultModel(); got != "claude-haiku-4-5" {
		t.Fatalf("DefaultModel() = %q", got)
	}
}

// Скриптуемый стрим держит тот же контракт по горутинам, что и боевой: Close и
// Usage вызываются из любой горутины (гонку ловит -race).
func TestScriptStream_CloseFromAnotherGoroutine(t *testing.T) {
	st := anthropictest.ScriptStream(&anthropickit.Response{}, anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "a"},
		anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "b"})

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for st.Next() {
			_ = st.Event()
		}
	}()
	_ = st.Usage()
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	<-drained
}
