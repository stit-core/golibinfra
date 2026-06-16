package yandexaikit

import "context"

const embeddingPath = "/foundationModels/v1/textEmbedding"

// EmbeddingInput — нативный вход векторизации. Model — bare-id ("text-search-doc")
// ИЛИ полный modelUri ("emb://<folder>/text-search-doc/latest").
type EmbeddingInput struct {
	Model string // "" → text-search-doc
	Text  string
	Dim   *int64 // опц.: мульти-размерный выход (для fixed-256 моделей не задаём)
}

// EmbeddingResult — нормализованный результат: вектор + Usage + cost (RUB).
type EmbeddingResult struct {
	Embedding    []float64
	ModelVersion string
	Usage        Usage
	Cost         RUBMicros
}

// TextEmbedding — синхронная векторизация (embeddings — 10 RPS квота).
func (c *Client) TextEmbedding(ctx context.Context, in EmbeddingInput) (*EmbeddingResult, error) {
	model := in.Model
	if model == "" {
		model = "text-search-doc"
	}
	body := EmbeddingRequest{
		ModelURI: c.modelURI("emb", model),
		Text:     in.Text,
		Dim:      in.Dim,
	}
	var resp EmbeddingResponse
	if err := c.doJSON(ctx, jsonRequest{
		method:   "POST",
		url:      c.cfg.BaseURL + embeddingPath,
		body:     body,
		endpoint: "text-embedding",
		model:    model,
	}, &resp); err != nil {
		return nil, err
	}
	// Embeddings метерятся numTokens как вход (Modality: embedding у потребителя).
	u := Usage{InputTokens: resp.NumTokens}
	cost := c.costOf(model, u)
	c.instr.recordUsage(ctx, model, u, cost)
	return &EmbeddingResult{
		Embedding:    resp.Embedding,
		ModelVersion: resp.ModelVersion,
		Usage:        u,
		Cost:         cost,
	}, nil
}
