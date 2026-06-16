package sberaikit

import (
	"context"
	"strings"
)

// EmbeddingsRequest — вход /embeddings. Input — текст(ы); модель — из набора
// Embeddings/Embeddings-2/EmbeddingsGigaR/GigaEmbeddings-3B-…
type EmbeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// Embedding — один вектор с индексом и per-item usage.
type Embedding struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
	Usage     struct {
		PromptTokens int64 `json:"prompt_tokens"`
	} `json:"usage"`
}

// EmbeddingsResponse — нормализованный ответ: Usage суммирует prompt_tokens по
// data[] (research §7), Cost — по той же таблице (модальность embedding).
type EmbeddingsResponse struct {
	Object string      `json:"object"`
	Model  string      `json:"model"`
	Data   []Embedding `json:"data"`
	Usage  Usage       `json:"-"`
	Cost   RUBMicros   `json:"-"`
}

// Embeddings — POST /embeddings. Суммирует per-item prompt_tokens в InputTokens.
func (c *Client) Embeddings(ctx context.Context, req EmbeddingsRequest) (*EmbeddingsResponse, error) {
	model := c.model(req.Model)
	req.Model = model
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/embeddings"

	start := c.clock.Now()
	var resp EmbeddingsResponse
	if err := c.doJSON(ctx, "embeddings", url, req, &resp); err != nil {
		c.instr.record(ctx, "embeddings", model, "error", c.clock.Now().Sub(start), Usage{}, 0)
		return nil, err
	}

	var sum int64
	for _, e := range resp.Data {
		sum += e.Usage.PromptTokens
	}
	resp.Usage = Usage{InputTokens: sum, TotalTokens: sum}
	if resp.Model == "" {
		resp.Model = model
	}
	resp.Cost = c.costOf(resp.Model, resp.Usage)
	c.instr.record(ctx, "embeddings", resp.Model, "ok", c.clock.Now().Sub(start), resp.Usage, resp.Cost)
	return &resp, nil
}
