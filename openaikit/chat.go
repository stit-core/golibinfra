package openaikit

import (
	"context"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ChatMessage — текстовое сообщение чата. Vision/audio/file-парты и tool-роли —
// через Raw() (Chat Completions union-поверхность шире нативной).
type ChatMessage struct {
	Role Role
	Text string
}

// ChatRequest — нативный вход Chat Completions (stateless: полная история в
// Messages). Tools/tool_choice, мультимодальные парты — через Raw().
type ChatRequest struct {
	Model               string // "" → Config.DefaultModel
	Messages            []ChatMessage
	MaxCompletionTokens int64  // 0 → без лимита
	Effort              Effort // reasoning_effort; "" → дефолт
	Temperature         *float64
	JSONSchema          *JSONSchema // structured output (response_format = json_schema)
}

// ChatResponse — нормализованный результат текстового вызова (Chat и Responses).
// Refusal — успешный 200 с отказом классификатора (не ошибка); проверяй до парсинга.
type ChatResponse struct {
	ID           string
	Model        string
	Text         string
	Refusal      string
	FinishReason string
	Usage        Usage
	Cost         USDMicros
}

// IsRefusal — модель вернула отказ (refusal-поле непусто).
func (r *ChatResponse) IsRefusal() bool { return r.Refusal != "" }

// Chat — синхронный Chat Completions-вызов.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	params, model := c.toChatParams(req, false)
	start := time.Now()
	resp, err := c.sdk.Chat.Completions.New(ctx, params)
	if err != nil {
		c.instr.record(ctx, model, ModalityText, "error", time.Since(start), Usage{}, 0)
		return nil, classify(err)
	}
	out := c.buildChatResponse(resp, model)
	c.instr.record(ctx, model, ModalityText, "ok", time.Since(start), out.Usage, out.Cost)
	return out, nil
}

// StreamChat открывает стриминговый Chat-вызов. Терминатор — [DONE]; usage в
// стриме требует stream_options.include_usage (кит выставляет его сам).
func (c *Client) StreamChat(ctx context.Context, req ChatRequest) Stream {
	params, model := c.toChatParams(req, true)
	return &chatStream{
		s:      c.sdk.Chat.Completions.NewStreaming(ctx, params),
		client: c,
		ctx:    ctx,
		model:  model,
		start:  time.Now(),
	}
}

func (c *Client) buildChatResponse(r *openai.ChatCompletion, model string) *ChatResponse {
	if r.Model != "" {
		model = r.Model
	}
	u := mapChatUsage(r.Usage)
	out := &ChatResponse{ID: r.ID, Model: model, Usage: u, Cost: c.costOf(model, u)}
	if len(r.Choices) > 0 {
		ch := r.Choices[0]
		out.Text = ch.Message.Content
		out.Refusal = ch.Message.Refusal
		out.FinishReason = ch.FinishReason
	}
	return out
}

func (c *Client) toChatParams(req ChatRequest, stream bool) (openai.ChatCompletionNewParams, string) {
	model := c.modelOr(req.Model)
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, chatMessageParam(m))
	}
	params := openai.ChatCompletionNewParams{
		Model:    model,
		Messages: msgs,
	}
	if req.MaxCompletionTokens > 0 {
		params.MaxCompletionTokens = openai.Int(req.MaxCompletionTokens)
	}
	if req.Effort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(req.Effort)
	}
	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}
	if req.JSONSchema != nil {
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   req.JSONSchema.Name,
					Schema: req.JSONSchema.Schema,
					Strict: openai.Bool(req.JSONSchema.Strict),
				},
			},
		}
	}
	if stream {
		// include_usage: иначе финальный chunk не несёт usage (нечем биллить стрим).
		params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}
	}
	return params, model
}

func chatMessageParam(m ChatMessage) openai.ChatCompletionMessageParamUnion {
	switch m.Role {
	case RoleSystem:
		return openai.SystemMessage(m.Text)
	case RoleDeveloper:
		return openai.DeveloperMessage(m.Text)
	case RoleAssistant:
		return openai.AssistantMessage(m.Text)
	default:
		return openai.UserMessage(m.Text)
	}
}
