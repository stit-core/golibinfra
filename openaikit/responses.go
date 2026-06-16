package openaikit

import (
	"context"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// Effort — уровень reasoning (reasoning.effort). Только reasoning-модели.
type Effort string

const (
	EffortNone    Effort = "none"
	EffortMinimal Effort = "minimal"
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
)

// ResponsesRequest — нативный вход Responses API. Покрывает рекомендуемый путь
// (text in/out, instructions, reasoning, structured output, server-side chaining).
// Vision/file/audio-парты, built-in tools и кастомные function-tools — через Raw().
type ResponsesRequest struct {
	Model           string // "" → Config.DefaultModel
	Input           string // bare-string вход; для типизированных input-items — Raw()
	Instructions    string // system/developer-инструкции
	MaxOutputTokens int64  // 0 → без лимита (модель сама)
	Effort          Effort // reasoning-effort; "" → дефолт модели
	Temperature     *float64
	JSONSchema      *JSONSchema // structured output (text.format = json_schema)

	// Server-side chaining (не пересылать историю): требует Store=true у предыдущего.
	PreviousResponseID string
	Store              *bool
}

// JSONSchema — structured-output контракт (strict JSON Schema).
type JSONSchema struct {
	Name   string
	Schema map[string]any
	Strict bool
}

// ResponsesResponse использует общий нормализованный ChatResponse (текст + Usage +
// Cost): единая поверхность результата для Responses и Chat.
type ResponsesResponse = ChatResponse

// Respond — синхронный вызов Responses API.
func (c *Client) Respond(ctx context.Context, req ResponsesRequest) (*ResponsesResponse, error) {
	params, model := c.toResponsesParams(req)
	start := time.Now()
	resp, err := c.sdk.Responses.New(ctx, params)
	if err != nil {
		c.instr.record(ctx, model, ModalityText, "error", time.Since(start), Usage{}, 0)
		return nil, classify(err)
	}
	out := c.buildResponsesResponse(resp, model)
	c.instr.record(ctx, model, ModalityText, "ok", time.Since(start), out.Usage, out.Cost)
	return out, nil
}

// StreamResponse открывает стриминговый Responses-вызов. Терминатор —
// response.completed (НЕТ [DONE]); финальный Usage — из этого события.
func (c *Client) StreamResponse(ctx context.Context, req ResponsesRequest) Stream {
	params, model := c.toResponsesParams(req)
	return &responsesStream{
		s:      c.sdk.Responses.NewStreaming(ctx, params),
		client: c,
		ctx:    ctx,
		model:  model,
		start:  time.Now(),
	}
}

func (c *Client) buildResponsesResponse(r *responses.Response, model string) *ResponsesResponse {
	if r.Model != "" {
		model = r.Model
	}
	u := mapResponsesUsage(r.Usage)
	out := &ResponsesResponse{
		ID:      r.ID,
		Model:   model,
		Text:    r.OutputText(),
		Usage:   u,
		Cost:    c.costOf(model, u),
		Refusal: responsesRefusal(r),
	}
	return out
}

// responsesRefusal собирает refusal-текст из output-частей (refusal — не ошибка,
// успешный 200; потребитель проверяет Refusal до парсинга).
func responsesRefusal(r *responses.Response) string {
	var b strings.Builder
	for _, item := range r.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.AsMessage().Content {
			if part.Type == "refusal" {
				b.WriteString(part.AsRefusal().Refusal)
			}
		}
	}
	return b.String()
}

func (c *Client) toResponsesParams(req ResponsesRequest) (responses.ResponseNewParams, string) {
	model := c.modelOr(req.Model)
	params := responses.ResponseNewParams{
		Model: model,
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(req.Input)},
	}
	if req.Instructions != "" {
		params.Instructions = openai.String(req.Instructions)
	}
	if req.MaxOutputTokens > 0 {
		params.MaxOutputTokens = openai.Int(req.MaxOutputTokens)
	}
	if req.Effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(req.Effort)}
	}
	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}
	if req.PreviousResponseID != "" {
		params.PreviousResponseID = openai.String(req.PreviousResponseID)
	}
	if req.Store != nil {
		params.Store = openai.Bool(*req.Store)
	}
	if req.JSONSchema != nil {
		format := responses.ResponseFormatTextConfigParamOfJSONSchema(req.JSONSchema.Name, req.JSONSchema.Schema)
		if format.OfJSONSchema != nil {
			format.OfJSONSchema.Strict = openai.Bool(req.JSONSchema.Strict)
		}
		params.Text = responses.ResponseTextConfigParam{Format: format}
	}
	return params, model
}
