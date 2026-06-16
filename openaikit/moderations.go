package openaikit

import (
	"context"
	"time"

	"github.com/openai/openai-go/v3"
)

// ModerationRequest — нативный вход модерации. Один или несколько текстов;
// мультимодальный (image_url) вход — через Raw().
type ModerationRequest struct {
	Model string // "" → "omni-moderation-latest"
	Input []string
}

const defaultModerationModel = "omni-moderation-latest"

// ModerationResult — итог по одному входу: флаг + полный SDK-результат (категории,
// скоры) для потребителей, которым нужна детализация.
type ModerationResult struct {
	Flagged bool
	Raw     openai.Moderation
}

// ModerationResponse — результаты по индексу входа. Модерация бесплатна (Cost=0,
// usage нет).
type ModerationResponse struct {
	ID      string
	Model   string
	Results []ModerationResult
}

// AnyFlagged — true, если хоть один вход помечен.
func (r *ModerationResponse) AnyFlagged() bool {
	for _, res := range r.Results {
		if res.Flagged {
			return true
		}
	}
	return false
}

// Moderate — классификация контента (free).
func (c *Client) Moderate(ctx context.Context, req ModerationRequest) (*ModerationResponse, error) {
	model := req.Model
	if model == "" {
		model = defaultModerationModel
	}
	params := openai.ModerationNewParams{
		Model: model,
		Input: openai.ModerationNewParamsInputUnion{OfStringArray: req.Input},
	}

	start := time.Now()
	resp, err := c.sdk.Moderations.New(ctx, params)
	if err != nil {
		c.instr.record(ctx, model, ModalityText, "error", time.Since(start), Usage{}, 0)
		return nil, classify(err)
	}

	out := &ModerationResponse{ID: resp.ID, Model: resp.Model}
	for _, m := range resp.Results {
		out.Results = append(out.Results, ModerationResult{Flagged: m.Flagged, Raw: m})
	}
	c.instr.record(ctx, model, ModalityText, "ok", time.Since(start), Usage{}, 0)
	return out, nil
}
