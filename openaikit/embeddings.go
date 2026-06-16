package openaikit

import (
	"context"
	"time"

	"github.com/openai/openai-go/v3"
)

// EmbeddingsRequest — нативный вход embeddings. Один или несколько текстов;
// dimensions усекает вектор (только text-embedding-3-*).
type EmbeddingsRequest struct {
	Model      string // "" → "text-embedding-3-small"
	Input      []string
	Dimensions int64 // 0 → дефолтная размерность модели
}

const defaultEmbeddingModel = "text-embedding-3-small"

// EmbeddingsResponse — нормализованный результат: векторы по индексу + Usage/Cost.
type EmbeddingsResponse struct {
	Model      string
	Embeddings [][]float64 // по индексу входа
	Usage      Usage
	Cost       USDMicros
}

// Embed — векторизация текстов.
func (c *Client) Embed(ctx context.Context, req EmbeddingsRequest) (*EmbeddingsResponse, error) {
	model := req.Model
	if model == "" {
		model = defaultEmbeddingModel
	}
	params := openai.EmbeddingNewParams{
		Model: model,
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: req.Input},
	}
	if req.Dimensions > 0 {
		params.Dimensions = openai.Int(req.Dimensions)
	}

	start := time.Now()
	resp, err := c.sdk.Embeddings.New(ctx, params)
	if err != nil {
		c.instr.record(ctx, model, ModalityEmbedding, "error", time.Since(start), Usage{}, 0)
		return nil, classify(err)
	}

	u := mapEmbeddingUsage(resp.Usage)
	cost := c.costOf(model, u)
	out := &EmbeddingsResponse{
		Model:      model,
		Embeddings: make([][]float64, len(resp.Data)),
		Usage:      u,
		Cost:       cost,
	}
	for _, d := range resp.Data {
		if int(d.Index) < len(out.Embeddings) {
			out.Embeddings[d.Index] = d.Embedding
		}
	}
	c.instr.record(ctx, model, ModalityEmbedding, "ok", time.Since(start), u, cost)
	return out, nil
}
