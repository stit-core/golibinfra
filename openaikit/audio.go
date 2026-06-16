package openaikit

import (
	"context"
	"io"
	"time"

	"github.com/openai/openai-go/v3"
)

// TranscribeRequest — нативный вход STT (transcriptions). File — поток аудио
// (≤25 MB); под os.File имя берётся автоматически, иначе оберни openai.File(r,name,ct).
type TranscribeRequest struct {
	Model          string // "" → "whisper-1"
	File           io.Reader
	Language       string
	Prompt         string
	ResponseFormat string // json|text|verbose_json|… ; "" → json
}

const defaultTranscribeModel = "whisper-1"

// TranscribeResponse — транскрипт + нормализованный Usage (токены ИЛИ AudioSeconds).
type TranscribeResponse struct {
	Model    string
	Text     string
	Language string
	Usage    Usage
	Cost     USDMicros
}

// Transcribe — audio→text (STT).
func (c *Client) Transcribe(ctx context.Context, req TranscribeRequest) (*TranscribeResponse, error) {
	model := req.Model
	if model == "" {
		model = defaultTranscribeModel
	}
	params := openai.AudioTranscriptionNewParams{
		File:  req.File,
		Model: model,
	}
	if req.Language != "" {
		params.Language = openai.String(req.Language)
	}
	if req.Prompt != "" {
		params.Prompt = openai.String(req.Prompt)
	}
	if req.ResponseFormat != "" {
		params.ResponseFormat = openai.AudioResponseFormat(req.ResponseFormat)
	}

	start := time.Now()
	resp, err := c.sdk.Audio.Transcriptions.New(ctx, params)
	if err != nil {
		c.instr.record(ctx, model, ModalityAudio, "error", time.Since(start), Usage{}, 0)
		return nil, classify(err)
	}

	u := transcribeUsage(resp)
	cost := c.costOf(model, u)
	c.instr.record(ctx, model, ModalityAudio, "ok", time.Since(start), u, cost)
	return &TranscribeResponse{
		Model:    model,
		Text:     resp.Text,
		Language: resp.Language,
		Usage:    u,
		Cost:     cost,
	}, nil
}

// transcribeUsage нормализует обе формы STT-usage: token-billed модели отдают
// input/output_tokens; duration-billed (whisper-1/verbose_json) — seconds. Если
// usage отсутствует, но есть Duration (verbose_json) — берём её как AudioSeconds.
func transcribeUsage(r *openai.AudioTranscriptionNewResponseUnion) Usage {
	u := Usage{Modality: ModalityAudio}
	switch r.Usage.Type {
	case "tokens":
		u.InputTokens = r.Usage.InputTokens
		u.OutputTokens = r.Usage.OutputTokens
	case "duration":
		u.AudioSeconds = r.Usage.Seconds
	}
	if u.AudioSeconds == 0 && r.Duration > 0 {
		u.AudioSeconds = r.Duration
	}
	return u
}

// SpeechRequest — нативный вход TTS. Output — сырые аудио-байты (response_format).
type SpeechRequest struct {
	Model          string // "" → "gpt-4o-mini-tts"
	Input          string // текст ≤4096 симв.
	Voice          string // alloy|nova|…
	ResponseFormat string // mp3|opus|aac|flac|wav|pcm ; "" → mp3
	Instructions   string // только gpt-4o-mini-tts
	Speed          float64
}

const defaultSpeechModel = "gpt-4o-mini-tts"

// SpeechResponse — синтезированное аудио (байты) + Usage (символы входа → InputTokens
// для прайсинга tts-1*; gpt-4o-mini-tts метерится токенами, но REST не отдаёт usage —
// InputTokens приближается длиной входа в символах).
type SpeechResponse struct {
	Model string
	Audio []byte
	Usage Usage
	Cost  USDMicros
}

// Speak — text→audio (TTS).
func (c *Client) Speak(ctx context.Context, req SpeechRequest) (*SpeechResponse, error) {
	model := req.Model
	if model == "" {
		model = defaultSpeechModel
	}
	params := openai.AudioSpeechNewParams{
		Input: req.Input,
		Model: model,
		Voice: openai.AudioSpeechNewParamsVoiceUnion{OfString: openai.String(req.Voice)},
	}
	if req.ResponseFormat != "" {
		params.ResponseFormat = openai.AudioSpeechNewParamsResponseFormat(req.ResponseFormat)
	}
	if req.Instructions != "" {
		params.Instructions = openai.String(req.Instructions)
	}
	if req.Speed > 0 {
		params.Speed = openai.Float(req.Speed)
	}

	start := time.Now()
	httpResp, err := c.sdk.Audio.Speech.New(ctx, params)
	if err != nil {
		c.instr.record(ctx, model, ModalityAudio, "error", time.Since(start), Usage{}, 0)
		return nil, classify(err)
	}
	defer httpResp.Body.Close()

	audio, err := io.ReadAll(httpResp.Body)
	if err != nil {
		c.instr.record(ctx, model, ModalityAudio, "error", time.Since(start), Usage{}, 0)
		return nil, err
	}

	// TTS-прайсинг — по символам входа (tts-1/-hd: $/1M chars). REST usage не
	// возвращает, поэтому InputTokens приближается длиной input в символах.
	u := Usage{Modality: ModalityAudio, InputTokens: int64(len([]rune(req.Input)))}
	cost := c.costOf(model, u)
	c.instr.record(ctx, model, ModalityAudio, "ok", time.Since(start), u, cost)
	return &SpeechResponse{Model: model, Audio: audio, Usage: u, Cost: cost}, nil
}
