// Package googletest — тест-двойники googleaikit для потребителя: настраиваемый
// Stub реализации googleaikit.API и скриптуемый Stream, всё без сети.
package googletest

import (
	"context"

	"google.golang.org/genai"

	"git.stit.tech/stit-core/golibinfra/googleaikit"
)

// Stub — реализация googleaikit.API. Подменяй *Func-поля для нужного поведения;
// иначе отдаёт безопасные дефолты.
type Stub struct {
	GenerateContentFunc func(ctx context.Context, req googleaikit.Request) (*googleaikit.Response, error)
	StreamFunc          func(ctx context.Context, req googleaikit.Request) googleaikit.Stream
	CountTokensFunc     func(ctx context.Context, req googleaikit.Request) (int64, error)
	EmbedContentFunc    func(ctx context.Context, req googleaikit.EmbedRequest) (*googleaikit.EmbedResponse, error)
	GenerateImagesFunc  func(ctx context.Context, req googleaikit.ImageRequest) (*googleaikit.ImageResponse, error)
	SubmitVideoFunc     func(ctx context.Context, req googleaikit.VideoRequest) (*googleaikit.VideoOperation, error)
	PollVideoFunc       func(ctx context.Context, op *googleaikit.VideoOperation) (*googleaikit.VideoOperation, error)
	GenerateSpeechFunc  func(ctx context.Context, req googleaikit.SpeechRequest) (*googleaikit.SpeechResponse, error)
	RawClient           *genai.Client
}

var _ googleaikit.API = (*Stub)(nil)

func (s *Stub) GenerateContent(ctx context.Context, req googleaikit.Request) (*googleaikit.Response, error) {
	if s.GenerateContentFunc != nil {
		return s.GenerateContentFunc(ctx, req)
	}
	return &googleaikit.Response{
		StopReason: googleaikit.StopEndTurn,
		Content:    []googleaikit.Block{googleaikit.TextBlock{Text: "stub"}},
	}, nil
}

func (s *Stub) Stream(ctx context.Context, req googleaikit.Request) googleaikit.Stream {
	if s.StreamFunc != nil {
		return s.StreamFunc(ctx, req)
	}
	final := &googleaikit.Response{
		StopReason: googleaikit.StopEndTurn,
		Content:    []googleaikit.Block{googleaikit.TextBlock{Text: "stub"}},
	}
	return ScriptStream(
		final,
		googleaikit.StreamEvent{Type: googleaikit.EventTextDelta, Text: "stub"},
		googleaikit.StreamEvent{Type: googleaikit.EventDone},
	)
}

func (s *Stub) CountTokens(ctx context.Context, req googleaikit.Request) (int64, error) {
	if s.CountTokensFunc != nil {
		return s.CountTokensFunc(ctx, req)
	}
	return 0, nil
}

func (s *Stub) EmbedContent(ctx context.Context, req googleaikit.EmbedRequest) (*googleaikit.EmbedResponse, error) {
	if s.EmbedContentFunc != nil {
		return s.EmbedContentFunc(ctx, req)
	}
	emb := make([][]float32, len(req.Texts))
	for i := range emb {
		emb[i] = []float32{0, 0, 0}
	}
	return &googleaikit.EmbedResponse{Embeddings: emb}, nil
}

func (s *Stub) GenerateImages(ctx context.Context, req googleaikit.ImageRequest) (*googleaikit.ImageResponse, error) {
	if s.GenerateImagesFunc != nil {
		return s.GenerateImagesFunc(ctx, req)
	}
	return &googleaikit.ImageResponse{
		Images: []googleaikit.GenImage{{MIMEType: "image/png", Data: []byte("stub")}},
		Usage:  googleaikit.Usage{Images: 1},
	}, nil
}

func (s *Stub) SubmitVideo(ctx context.Context, req googleaikit.VideoRequest) (*googleaikit.VideoOperation, error) {
	if s.SubmitVideoFunc != nil {
		return s.SubmitVideoFunc(ctx, req)
	}
	return &googleaikit.VideoOperation{Name: "operations/stub", Done: false}, nil
}

func (s *Stub) PollVideo(ctx context.Context, op *googleaikit.VideoOperation) (*googleaikit.VideoOperation, error) {
	if s.PollVideoFunc != nil {
		return s.PollVideoFunc(ctx, op)
	}
	return &googleaikit.VideoOperation{
		Name:   "operations/stub",
		Done:   true,
		Videos: []googleaikit.GenVideo{{URI: "https://stub/video.mp4", MIMEType: "video/mp4"}},
	}, nil
}

func (s *Stub) GenerateSpeech(ctx context.Context, req googleaikit.SpeechRequest) (*googleaikit.SpeechResponse, error) {
	if s.GenerateSpeechFunc != nil {
		return s.GenerateSpeechFunc(ctx, req)
	}
	return &googleaikit.SpeechResponse{MIMEType: "audio/pcm", Audio: []byte("stub")}, nil
}

func (s *Stub) Raw() *genai.Client { return s.RawClient }

// scriptStream проигрывает заранее заданные события и финальный ответ.
type scriptStream struct {
	events []googleaikit.StreamEvent
	final  *googleaikit.Response
	err    error
	i      int
}

// ScriptStream — googleaikit.Stream из готового списка событий и финала.
func ScriptStream(final *googleaikit.Response, events ...googleaikit.StreamEvent) googleaikit.Stream {
	return &scriptStream{events: events, final: final}
}

// ErrStream — googleaikit.Stream, который сразу в ошибке.
func ErrStream(err error) googleaikit.Stream { return &scriptStream{err: err} }

func (s *scriptStream) Next() bool {
	if s.err != nil || s.i >= len(s.events) {
		return false
	}
	s.i++
	return true
}

func (s *scriptStream) Event() googleaikit.StreamEvent { return s.events[s.i-1] }

func (s *scriptStream) Err() error { return s.err }

func (s *scriptStream) Final() (*googleaikit.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.final, nil
}

func (s *scriptStream) Close() error { return nil }
