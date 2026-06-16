package googleaikit

import (
	"context"
	"strings"

	"google.golang.org/genai"
)

// ImageRequest — нативный вход генерации изображений. Модель решает механизм:
// imagen-* → Imagen (:predict, NumberOfImages/AspectRatio); иначе Nano Banana
// (generateContent с responseModalities:[TEXT,IMAGE], референс-картинки в Refs).
type ImageRequest struct {
	Model       string // "" → Config.DefaultModel
	Prompt      string
	Refs        []MediaBlock // референс-изображения (Nano Banana)
	SampleCount int32        // Imagen: 1–4 (NumberOfImages)
	AspectRatio string
	ImageSize   string
}

// GenImage — одно сгенерированное изображение (base64-байты + MIME; SynthID-watermark).
type GenImage struct {
	MIMEType string
	Data     []byte
}

type ImageResponse struct {
	Model  string
	Images []GenImage
	Usage  Usage
	Cost   USDMicros
}

// GenerateImages — Imagen (:predict) для imagen-* моделей, иначе Nano Banana
// (generateContent IMAGE-out). Usage.Images — кол-во вернувшихся картинок.
func (c *Client) GenerateImages(ctx context.Context, req ImageRequest) (*ImageResponse, error) {
	model := c.modelOr(req.Model)
	if strings.HasPrefix(model, "imagen-") {
		return c.imagenPredict(ctx, model, req)
	}
	return c.nanoBanana(ctx, model, req)
}

func (c *Client) imagenPredict(ctx context.Context, model string, req ImageRequest) (*ImageResponse, error) {
	cfg := &genai.GenerateImagesConfig{
		NumberOfImages: req.SampleCount,
		AspectRatio:    req.AspectRatio,
		ImageSize:      req.ImageSize,
	}
	start := c.clock.Now()
	resp, err := c.sdk.Models.GenerateImages(ctx, model, req.Prompt, cfg)
	if err != nil {
		c.instr.record(ctx, model, "image", "error", c.clock.Now().Sub(start), Usage{}, 0)
		return nil, classify(err)
	}
	out := &ImageResponse{Model: model}
	for _, gi := range resp.GeneratedImages {
		if gi != nil && gi.Image != nil {
			out.Images = append(out.Images, GenImage{MIMEType: gi.Image.MIMEType, Data: gi.Image.ImageBytes})
		}
	}
	out.Usage.Images = int64(len(out.Images))
	out.Cost = c.costOf(model, out.Usage)
	c.instr.record(ctx, model, "image", "ok", c.clock.Now().Sub(start), out.Usage, out.Cost)
	return out, nil
}

// nanoBanana генерирует изображение через generateContent с IMAGE-модальностью;
// картинки приходят как inlineData-части кандидата.
func (c *Client) nanoBanana(ctx context.Context, model string, req ImageRequest) (*ImageResponse, error) {
	parts := []*genai.Part{genai.NewPartFromText(req.Prompt)}
	for _, ref := range req.Refs {
		if ref.FileURI != "" {
			parts = append(parts, genai.NewPartFromURI(ref.FileURI, ref.MIMEType))
		} else {
			parts = append(parts, genai.NewPartFromBytes(ref.Data, ref.MIMEType))
		}
	}
	cfg := &genai.GenerateContentConfig{ResponseModalities: []string{"TEXT", "IMAGE"}}

	start := c.clock.Now()
	resp, err := c.sdk.Models.GenerateContent(ctx, model, []*genai.Content{genai.NewContentFromParts(parts, genai.RoleUser)}, cfg)
	if err != nil {
		c.instr.record(ctx, model, "image", "error", c.clock.Now().Sub(start), Usage{}, 0)
		return nil, classify(err)
	}

	out := &ImageResponse{Model: model, Usage: mapUsage(resp.UsageMetadata)}
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, p := range resp.Candidates[0].Content.Parts {
			if p != nil && p.InlineData != nil {
				out.Images = append(out.Images, GenImage{MIMEType: p.InlineData.MIMEType, Data: p.InlineData.Data})
			}
		}
	}
	out.Usage.Images = int64(len(out.Images))
	out.Cost = c.costOf(model, out.Usage)
	c.instr.record(ctx, model, "image", "ok", c.clock.Now().Sub(start), out.Usage, out.Cost)
	return out, nil
}
