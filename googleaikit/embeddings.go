package googleaikit

import (
	"context"

	"google.golang.org/genai"
)

// EmbedRequest — нативный вход embedContent / batchEmbedContents. Texts с одним
// элементом → single embed, иначе batch. TaskType — RETRIEVAL_DOCUMENT/QUERY/…
type EmbedRequest struct {
	Model                string // "" → Config.DefaultModel
	Texts                []string
	TaskType             string
	Title                string
	OutputDimensionality *int32
}

// EmbedResponse — нормализованные эмбеддинги (по одному вектору на вход) + Usage.
// Usage.InputTokens на AI-Studio недоступен per-response (нет usageMetadata у
// embedContent) — заполняется billableCharacterCount на Vertex, иначе 0; домен
// нормализует под Modality: embedding.
type EmbedResponse struct {
	Model      string
	Embeddings [][]float32
	Usage      Usage
	Cost       USDMicros
}

// EmbedContent векторизует один или несколько входов. genai.EmbedContent сам
// батчит contents в одном вызове.
func (c *Client) EmbedContent(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	model := c.modelOr(req.Model)
	contents := make([]*genai.Content, 0, len(req.Texts))
	for _, t := range req.Texts {
		contents = append(contents, genai.NewContentFromText(t, genai.RoleUser))
	}

	var cfg *genai.EmbedContentConfig
	if req.TaskType != "" || req.Title != "" || req.OutputDimensionality != nil {
		cfg = &genai.EmbedContentConfig{
			TaskType:             req.TaskType,
			Title:                req.Title,
			OutputDimensionality: req.OutputDimensionality,
		}
	}

	start := c.clock.Now()
	resp, err := c.sdk.Models.EmbedContent(ctx, model, contents, cfg)
	if err != nil {
		c.instr.record(ctx, model, "embed", "error", c.clock.Now().Sub(start), Usage{}, 0)
		return nil, classify(err)
	}

	out := &EmbedResponse{Model: model}
	for _, e := range resp.Embeddings {
		if e != nil {
			out.Embeddings = append(out.Embeddings, e.Values)
		}
	}
	if resp.Metadata != nil {
		// Vertex даёт billable-characters; токенов нет — оставляем как грубую оценку входа.
		out.Usage.InputTokens = int64(resp.Metadata.BillableCharacterCount)
	}
	out.Cost = c.costOf(model, out.Usage)
	c.instr.record(ctx, model, "embed", "ok", c.clock.Now().Sub(start), out.Usage, out.Cost)
	return out, nil
}
