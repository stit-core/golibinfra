package openaikit

import (
	"context"
	"time"

	"github.com/openai/openai-go/v3"
)

// ImageRequest — нативный вход генерации изображений (gpt-image-2). Вывод всегда
// base64 (GPT-image не отдаёт URL). Edits/variations и стриминг partial-картинок
// — через Raw().
type ImageRequest struct {
	Model        string // "" → "gpt-image-2"
	Prompt       string // до 32000 симв.
	N            int64  // 1–10; 0 → 1
	Size         string // auto|1024x1024|1536x1024|1024x1536
	Quality      string // auto|low|medium|high
	Background   string // transparent|opaque|auto
	OutputFormat string // png|jpeg|webp
}

const defaultImageModel = "gpt-image-2"

// GeneratedImage — одна сгенерированная картинка (base64) + revised prompt.
type GeneratedImage struct {
	B64JSON       string
	RevisedPrompt string
}

// ImageResponse — нормализованный результат: картинки + Usage (Images + токены) + Cost.
type ImageResponse struct {
	Model  string
	Images []GeneratedImage
	Usage  Usage
	Cost   USDMicros
}

// GenerateImage — text→image.
func (c *Client) GenerateImage(ctx context.Context, req ImageRequest) (*ImageResponse, error) {
	model := req.Model
	if model == "" {
		model = defaultImageModel
	}
	params := openai.ImageGenerateParams{
		Prompt: req.Prompt,
		Model:  model,
	}
	if req.N > 0 {
		params.N = openai.Int(req.N)
	}
	if req.Size != "" {
		params.Size = openai.ImageGenerateParamsSize(req.Size)
	}
	if req.Quality != "" {
		params.Quality = openai.ImageGenerateParamsQuality(req.Quality)
	}
	if req.Background != "" {
		params.Background = openai.ImageGenerateParamsBackground(req.Background)
	}
	if req.OutputFormat != "" {
		params.OutputFormat = openai.ImageGenerateParamsOutputFormat(req.OutputFormat)
	}

	start := time.Now()
	resp, err := c.sdk.Images.Generate(ctx, params)
	if err != nil {
		c.instr.record(ctx, model, ModalityImage, "error", time.Since(start), Usage{}, 0)
		return nil, classify(err)
	}

	u := mapImageUsage(resp.Usage, len(resp.Data))
	cost := c.costOf(model, u)
	out := &ImageResponse{Model: model, Usage: u, Cost: cost}
	for _, d := range resp.Data {
		out.Images = append(out.Images, GeneratedImage{B64JSON: d.B64JSON, RevisedPrompt: d.RevisedPrompt})
	}
	c.instr.record(ctx, model, ModalityImage, "ok", time.Since(start), u, cost)
	return out, nil
}
