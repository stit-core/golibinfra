// Package openaitest — тест-двойники openaikit для потребителя: настраиваемый
// Stub реализации openaikit.API и скриптуемый Stream, всё без сети.
package openaitest

import (
	"context"

	"github.com/openai/openai-go/v3"

	"git.stit.tech/stit-core/golibinfra/openaikit"
)

// Stub — реализация openaikit.API. Подменяй *Func-поля для нужного поведения;
// иначе отдаёт безопасные дефолты.
type Stub struct {
	RespondFunc        func(ctx context.Context, req openaikit.ResponsesRequest) (*openaikit.ResponsesResponse, error)
	StreamResponseFunc func(ctx context.Context, req openaikit.ResponsesRequest) openaikit.Stream
	ChatFunc           func(ctx context.Context, req openaikit.ChatRequest) (*openaikit.ChatResponse, error)
	StreamChatFunc     func(ctx context.Context, req openaikit.ChatRequest) openaikit.Stream
	EmbedFunc          func(ctx context.Context, req openaikit.EmbeddingsRequest) (*openaikit.EmbeddingsResponse, error)
	GenerateImageFunc  func(ctx context.Context, req openaikit.ImageRequest) (*openaikit.ImageResponse, error)
	TranscribeFunc     func(ctx context.Context, req openaikit.TranscribeRequest) (*openaikit.TranscribeResponse, error)
	SpeakFunc          func(ctx context.Context, req openaikit.SpeechRequest) (*openaikit.SpeechResponse, error)
	ModerateFunc       func(ctx context.Context, req openaikit.ModerationRequest) (*openaikit.ModerationResponse, error)
	RawClient          openai.Client
}

var _ openaikit.API = (*Stub)(nil)

func (s *Stub) Respond(ctx context.Context, req openaikit.ResponsesRequest) (*openaikit.ResponsesResponse, error) {
	if s.RespondFunc != nil {
		return s.RespondFunc(ctx, req)
	}
	return &openaikit.ResponsesResponse{Text: "stub", Usage: openaikit.Usage{Modality: openaikit.ModalityText}}, nil
}

func (s *Stub) StreamResponse(ctx context.Context, req openaikit.ResponsesRequest) openaikit.Stream {
	if s.StreamResponseFunc != nil {
		return s.StreamResponseFunc(ctx, req)
	}
	return ScriptStream(
		&openaikit.ChatResponse{Text: "stub"},
		openaikit.StreamEvent{Type: openaikit.EventTextDelta, Text: "stub"},
		openaikit.StreamEvent{Type: openaikit.EventDone},
	)
}

func (s *Stub) Chat(ctx context.Context, req openaikit.ChatRequest) (*openaikit.ChatResponse, error) {
	if s.ChatFunc != nil {
		return s.ChatFunc(ctx, req)
	}
	return &openaikit.ChatResponse{Text: "stub", Usage: openaikit.Usage{Modality: openaikit.ModalityText}}, nil
}

func (s *Stub) StreamChat(ctx context.Context, req openaikit.ChatRequest) openaikit.Stream {
	if s.StreamChatFunc != nil {
		return s.StreamChatFunc(ctx, req)
	}
	return ScriptStream(
		&openaikit.ChatResponse{Text: "stub"},
		openaikit.StreamEvent{Type: openaikit.EventTextDelta, Text: "stub"},
		openaikit.StreamEvent{Type: openaikit.EventDone},
	)
}

func (s *Stub) Embed(ctx context.Context, req openaikit.EmbeddingsRequest) (*openaikit.EmbeddingsResponse, error) {
	if s.EmbedFunc != nil {
		return s.EmbedFunc(ctx, req)
	}
	return &openaikit.EmbeddingsResponse{Usage: openaikit.Usage{Modality: openaikit.ModalityEmbedding}}, nil
}

func (s *Stub) GenerateImage(ctx context.Context, req openaikit.ImageRequest) (*openaikit.ImageResponse, error) {
	if s.GenerateImageFunc != nil {
		return s.GenerateImageFunc(ctx, req)
	}
	return &openaikit.ImageResponse{Usage: openaikit.Usage{Modality: openaikit.ModalityImage}}, nil
}

func (s *Stub) Transcribe(ctx context.Context, req openaikit.TranscribeRequest) (*openaikit.TranscribeResponse, error) {
	if s.TranscribeFunc != nil {
		return s.TranscribeFunc(ctx, req)
	}
	return &openaikit.TranscribeResponse{Text: "stub", Usage: openaikit.Usage{Modality: openaikit.ModalityAudio}}, nil
}

func (s *Stub) Speak(ctx context.Context, req openaikit.SpeechRequest) (*openaikit.SpeechResponse, error) {
	if s.SpeakFunc != nil {
		return s.SpeakFunc(ctx, req)
	}
	return &openaikit.SpeechResponse{Audio: []byte("stub"), Usage: openaikit.Usage{Modality: openaikit.ModalityAudio}}, nil
}

func (s *Stub) Moderate(ctx context.Context, req openaikit.ModerationRequest) (*openaikit.ModerationResponse, error) {
	if s.ModerateFunc != nil {
		return s.ModerateFunc(ctx, req)
	}
	return &openaikit.ModerationResponse{Model: "stub"}, nil
}

func (s *Stub) Raw() openai.Client { return s.RawClient }

// scriptStream проигрывает заранее заданные события и финальный ответ.
type scriptStream struct {
	events []openaikit.StreamEvent
	final  *openaikit.ChatResponse
	err    error
	i      int
}

// ScriptStream — openaikit.Stream из готового списка событий и финала.
func ScriptStream(final *openaikit.ChatResponse, events ...openaikit.StreamEvent) openaikit.Stream {
	return &scriptStream{events: events, final: final}
}

// ErrStream — openaikit.Stream, который сразу в ошибке.
func ErrStream(err error) openaikit.Stream { return &scriptStream{err: err} }

func (s *scriptStream) Next() bool {
	if s.err != nil || s.i >= len(s.events) {
		return false
	}
	s.i++
	return true
}

func (s *scriptStream) Event() openaikit.StreamEvent { return s.events[s.i-1] }

func (s *scriptStream) Err() error { return s.err }

func (s *scriptStream) Final() (*openaikit.ChatResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.final, nil
}

func (s *scriptStream) Close() error { return nil }
