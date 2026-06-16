package googleaikit

import (
	"context"

	"google.golang.org/genai"
)

// SpeechRequest — нативный вход TTS (generateContent с responseModalities:[AUDIO]).
// VoiceName — один из prebuilt-голосов (Zephyr/Puck/Kore/…); пусто → дефолт модели.
type SpeechRequest struct {
	Model     string // "" → Config.DefaultModel; ожидается *-tts
	Text      string
	VoiceName string
	Language  string
}

// SpeechResponse — синтезированное аудио (PCM 24kHz 16-bit mono, inlineData) + Usage.
type SpeechResponse struct {
	Model    string
	MIMEType string
	Audio    []byte
	Usage    Usage
	Cost     USDMicros
}

// GenerateSpeech синтезирует речь. Аудио приходит inlineData-частью кандидата;
// биллинг — по выходным audio-токенам (usageMetadata.candidatesTokenCount).
func (c *Client) GenerateSpeech(ctx context.Context, req SpeechRequest) (*SpeechResponse, error) {
	model := c.modelOr(req.Model)
	cfg := &genai.GenerateContentConfig{ResponseModalities: []string{"AUDIO"}}
	if req.VoiceName != "" || req.Language != "" {
		sc := &genai.SpeechConfig{LanguageCode: req.Language}
		if req.VoiceName != "" {
			sc.VoiceConfig = &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: req.VoiceName},
			}
		}
		cfg.SpeechConfig = sc
	}

	contents := []*genai.Content{genai.NewContentFromText(req.Text, genai.RoleUser)}
	start := c.clock.Now()
	resp, err := c.sdk.Models.GenerateContent(ctx, model, contents, cfg)
	if err != nil {
		c.instr.record(ctx, model, "audio", "error", c.clock.Now().Sub(start), Usage{}, 0)
		return nil, classify(err)
	}

	out := &SpeechResponse{Model: model, Usage: mapUsage(resp.UsageMetadata)}
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, p := range resp.Candidates[0].Content.Parts {
			if p != nil && p.InlineData != nil {
				out.MIMEType = p.InlineData.MIMEType
				out.Audio = p.InlineData.Data
				break
			}
		}
	}
	out.Cost = c.costOf(model, out.Usage)
	c.instr.record(ctx, model, "audio", "ok", c.clock.Now().Sub(start), out.Usage, out.Cost)
	return out, nil
}
