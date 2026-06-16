// Package yandextest — тест-двойники yandexaikit для потребителя: настраиваемый
// Stub реализации yandexaikit.API и скриптуемый Stream, всё без сети.
package yandextest

import (
	"context"
	"net/http"

	"git.stit.tech/stit-core/golibinfra/yandexaikit"
)

// Stub — реализация yandexaikit.API. Подменяй *Func-поля для нужного поведения;
// иначе отдаёт безопасные дефолты.
type Stub struct {
	CompletionFunc           func(ctx context.Context, req yandexaikit.Request) (*yandexaikit.Response, error)
	StreamFunc               func(ctx context.Context, req yandexaikit.Request) yandexaikit.Stream
	CompletionAsyncFunc      func(ctx context.Context, req yandexaikit.Request) (string, error)
	AwaitCompletionFunc      func(ctx context.Context, operationID string) (*yandexaikit.Response, error)
	TextEmbeddingFunc        func(ctx context.Context, in yandexaikit.EmbeddingInput) (*yandexaikit.EmbeddingResult, error)
	ImageGenerationAsyncFunc func(ctx context.Context, in yandexaikit.ImageInput) (string, error)
	AwaitImageFunc           func(ctx context.Context, operationID string) (*yandexaikit.ImageResult, error)
	PollOperationFunc        func(ctx context.Context, operationID string) (*yandexaikit.Operation, error)
	RawFunc                  func(ctx context.Context, method, path string, body []byte) (*http.Response, error)
}

var _ yandexaikit.API = (*Stub)(nil)

func (s *Stub) Completion(ctx context.Context, req yandexaikit.Request) (*yandexaikit.Response, error) {
	if s.CompletionFunc != nil {
		return s.CompletionFunc(ctx, req)
	}
	return &yandexaikit.Response{
		Alternatives: []yandexaikit.Alternative{{
			Message: yandexaikit.Message{Role: yandexaikit.RoleAssistant, Text: "stub"},
			Status:  yandexaikit.StatusFinal,
		}},
	}, nil
}

func (s *Stub) Stream(ctx context.Context, req yandexaikit.Request) yandexaikit.Stream {
	if s.StreamFunc != nil {
		return s.StreamFunc(ctx, req)
	}
	final := &yandexaikit.Response{
		Alternatives: []yandexaikit.Alternative{{
			Message: yandexaikit.Message{Role: yandexaikit.RoleAssistant, Text: "stub"},
			Status:  yandexaikit.StatusFinal,
		}},
	}
	return ScriptStream(
		final,
		yandexaikit.StreamEvent{Text: "stub", Status: yandexaikit.StatusFinal},
	)
}

func (s *Stub) CompletionAsync(ctx context.Context, req yandexaikit.Request) (string, error) {
	if s.CompletionAsyncFunc != nil {
		return s.CompletionAsyncFunc(ctx, req)
	}
	return "op-stub", nil
}

func (s *Stub) AwaitCompletion(ctx context.Context, operationID string) (*yandexaikit.Response, error) {
	if s.AwaitCompletionFunc != nil {
		return s.AwaitCompletionFunc(ctx, operationID)
	}
	return s.Completion(ctx, yandexaikit.Request{})
}

func (s *Stub) TextEmbedding(ctx context.Context, in yandexaikit.EmbeddingInput) (*yandexaikit.EmbeddingResult, error) {
	if s.TextEmbeddingFunc != nil {
		return s.TextEmbeddingFunc(ctx, in)
	}
	return &yandexaikit.EmbeddingResult{Embedding: []float64{0}}, nil
}

func (s *Stub) ImageGenerationAsync(ctx context.Context, in yandexaikit.ImageInput) (string, error) {
	if s.ImageGenerationAsyncFunc != nil {
		return s.ImageGenerationAsyncFunc(ctx, in)
	}
	return "op-stub", nil
}

func (s *Stub) AwaitImage(ctx context.Context, operationID string) (*yandexaikit.ImageResult, error) {
	if s.AwaitImageFunc != nil {
		return s.AwaitImageFunc(ctx, operationID)
	}
	return &yandexaikit.ImageResult{Image: []byte("stub"), MimeType: "image/jpeg", Usage: yandexaikit.Usage{Images: 1}}, nil
}

func (s *Stub) PollOperation(ctx context.Context, operationID string) (*yandexaikit.Operation, error) {
	if s.PollOperationFunc != nil {
		return s.PollOperationFunc(ctx, operationID)
	}
	return &yandexaikit.Operation{ID: operationID, Done: true}, nil
}

func (s *Stub) Raw(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	if s.RawFunc != nil {
		return s.RawFunc(ctx, method, path, body)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

// scriptStream проигрывает заранее заданные события и финальный ответ.
type scriptStream struct {
	events []yandexaikit.StreamEvent
	final  *yandexaikit.Response
	err    error
	i      int
}

// ScriptStream — yandexaikit.Stream из готового списка событий и финала.
func ScriptStream(final *yandexaikit.Response, events ...yandexaikit.StreamEvent) yandexaikit.Stream {
	return &scriptStream{events: events, final: final}
}

// ErrStream — yandexaikit.Stream, который сразу в ошибке.
func ErrStream(err error) yandexaikit.Stream { return &scriptStream{err: err} }

func (s *scriptStream) Next() bool {
	if s.err != nil || s.i >= len(s.events) {
		return false
	}
	s.i++
	return true
}

func (s *scriptStream) Event() yandexaikit.StreamEvent { return s.events[s.i-1] }

func (s *scriptStream) Err() error { return s.err }

func (s *scriptStream) Final() (*yandexaikit.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.final, nil
}

func (s *scriptStream) Close() error { return nil }
