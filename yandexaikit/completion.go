package yandexaikit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const completionPath = "/foundationModels/v1/completion"

// Request — нативный вход completion (декаплинг потребителя от wire-структур).
// Model — bare-id ("yandexgpt") ИЛИ полный modelUri ("gpt://<folder>/yandexgpt/latest").
type Request struct {
	Model       string // "" → Config.DefaultModel
	Messages    []Message
	Temperature *float64
	MaxTokens   int64 // 0 → не задаём (модельный дефолт)
	Reasoning   ReasoningMode
	Tools       []Tool
	ToolChoice  *ToolChoice
	// oneof responseFormat: JSONObject ИЛИ JSONSchema (взаимоисключающие).
	JSONObject bool
	JSONSchema json.RawMessage
}

// Response — нативный ответ completion с нормализованным Usage и cost (RUB).
type Response struct {
	Alternatives []Alternative
	ModelVersion string
	Usage        Usage
	Cost         RUBMicros
}

// Text — конкатенация text всех альтернатив (обычно одна).
func (r *Response) Text() string {
	var b strings.Builder
	for _, a := range r.Alternatives {
		b.WriteString(a.Message.Text)
	}
	return b.String()
}

// IsContentFilter — ответ отфильтрован (CONTENT_FILTER). Не ошибка: успешный вызов.
func (r *Response) IsContentFilter() bool {
	for _, a := range r.Alternatives {
		if a.Status == StatusContentFilter {
			return true
		}
	}
	return false
}

// Completion — синхронный completion. На стрим — Stream.
func (c *Client) Completion(ctx context.Context, req Request) (*Response, error) {
	if err := req.validateResponseFormat(); err != nil {
		return nil, err
	}
	body, model := c.toCompletionRequest(req, false)
	var env CompletionEnvelope
	if err := c.doJSON(ctx, jsonRequest{
		method:   "POST",
		url:      c.cfg.BaseURL + completionPath,
		body:     body,
		endpoint: "completion",
		model:    model,
	}, &env); err != nil {
		return nil, err
	}
	resp := c.buildResponse(env.Result, model, false)
	c.instr.recordUsage(ctx, model, resp.Usage, resp.Cost)
	return resp, nil
}

// buildResponse нормализует wire CompletionResult → Response (+ Usage + Cost).
// async выбирает async-тариф для cost.
func (c *Client) buildResponse(res CompletionResult, model string, async bool) *Response {
	u := usageFromContent(res.Usage)
	cost := c.costOf(model, u)
	if async {
		cost = c.costOfAsync(model, u)
	}
	return &Response{
		Alternatives: res.Alternatives,
		ModelVersion: res.ModelVersion,
		Usage:        u,
		Cost:         cost,
	}
}

// toCompletionRequest строит wire CompletionRequest из нативного Request.
// Возвращает тело и resolved-модель (bare-id или modelUri) для метрик/cost.
func (c *Client) toCompletionRequest(req Request, stream bool) (CompletionRequest, string) {
	model := req.Model
	if model == "" {
		model = c.cfg.DefaultModel
	}
	out := CompletionRequest{
		ModelURI: c.modelURI("gpt", model),
		CompletionOptions: CompletionOptions{
			Stream:      stream,
			Temperature: req.Temperature,
		},
		Messages: req.Messages,
		Tools:    req.Tools,
	}
	if req.MaxTokens > 0 {
		mt := req.MaxTokens
		out.CompletionOptions.MaxTokens = &mt
	}
	if req.Reasoning != "" {
		out.CompletionOptions.ReasoningOptions = &ReasoningOptions{Mode: req.Reasoning}
	}
	if req.ToolChoice != nil {
		out.ToolChoice = req.ToolChoice
	}
	switch {
	case req.JSONSchema != nil:
		out.JSONSchema = &JSONSchema{Schema: req.JSONSchema}
	case req.JSONObject:
		b := true
		out.JSONObject = &b
	}
	return out, model
}

// validateResponseFormat — JSONObject и JSONSchema взаимоисключающие (oneof).
func (req Request) validateResponseFormat() error {
	if req.JSONObject && req.JSONSchema != nil {
		return fmt.Errorf("%w: jsonObject and jsonSchema are mutually exclusive", ErrInvalidRequest)
	}
	return nil
}
