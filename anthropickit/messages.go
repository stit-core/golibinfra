package anthropickit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Дефолты max_tokens: sync держим под потолком non-streaming-режима, stream — щедро.
const (
	defaultMaxTokensSync   = 16_000
	defaultMaxTokensStream = 64_000
)

// maxSyncTokens — потолок max_tokens для non-streaming: API отдаёт ответ темпом
// ~128K токенов в час, больше не влезает в 10 минут — практический предел одного
// HTTP-ответа. Потолок фиксированный: ANTHROPIC_TIMEOUT его не поднимает.
const maxSyncTokens = 21_333

// Role — автор сообщения.
type Role string

// Роли сообщений Messages API.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ThinkingMode — конфигурация extended thinking у запроса.
type ThinkingMode int

// Режимы thinking. Adaptive — дефолт: модель сама решает глубину.
const (
	ThinkingAdaptive ThinkingMode = iota
	ThinkingDisabled
)

// ThinkingDisplay — возвращать ли содержимое размышлений. Пустая строка: поле
// не шлётся, действует дефолт API (у актуальных моделей — omitted).
type ThinkingDisplay string

// Режимы display.
const (
	ThinkingDisplaySummarized ThinkingDisplay = "summarized"
	ThinkingDisplayOmitted    ThinkingDisplay = "omitted"
)

// Effort — output_config.effort: глубина размышлений и расход токенов.
type Effort string

// Уровни effort.
const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// StopReason — почему модель прекратила генерацию.
type StopReason string

// Причины остановки, которые возвращает Messages API.
const (
	StopEndTurn         StopReason = "end_turn"
	StopMaxTokens       StopReason = "max_tokens"
	StopToolUse         StopReason = "tool_use"
	StopStopSequence    StopReason = "stop_sequence"
	StopPauseTurn       StopReason = "pause_turn"
	StopRefusal         StopReason = "refusal"
	StopContextExceeded StopReason = "model_context_window_exceeded"
)

// CacheTTL — время жизни точки кэша промпта.
type CacheTTL string

// Значения TTL. Пустой TTL — дефолт API (5 минут).
const (
	CacheTTL5m CacheTTL = "5m"
	CacheTTL1h CacheTTL = "1h"
)

// CacheControl помечает блок как точку кэша промпта.
type CacheControl struct{ TTL CacheTTL }

// ToolChoiceType — как модель выбирает инструменты.
type ToolChoiceType string

// Режимы выбора инструмента.
const (
	ToolChoiceAuto ToolChoiceType = "auto"
	ToolChoiceAny  ToolChoiceType = "any"
	ToolChoiceTool ToolChoiceType = "tool"
	ToolChoiceNone ToolChoiceType = "none"
)

// Block — sealed-интерфейс контент-блока (исчерпывается типами ниже).
type Block interface{ isBlock() }

// TextBlock — текст, при непустом Cache завершает кэшируемый префикс.
type TextBlock struct {
	Text  string
	Cache *CacheControl
}

// ImageBlock — картинка запроса: URL != "" → image по ссылке, иначе base64
// (MediaType + Data).
type ImageBlock struct {
	MediaType string
	Data      string
	URL       string
	Cache     *CacheControl
}

// DocumentBlock — документ запроса: PDFBase64 != "" → PDF, иначе Text как
// plain-text документ.
type DocumentBlock struct {
	PDFBase64 string
	Text      string
	Cache     *CacheControl
}

// ThinkingBlock — блок размышлений из ответа; эхо обратно без изменений на той
// же модели.
type ThinkingBlock struct {
	Thinking  string
	Signature string
}

// RedactedThinkingBlock — засекреченные размышления: непрозрачный блок, который
// реплеится без изменений; выбросить его — сломать ход thinking+tool_use.
type RedactedThinkingBlock struct {
	Data string
}

// ToolUseBlock — вызов инструмента, запрошенный моделью.
type ToolUseBlock struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResultBlock — результат вызова инструмента для модели.
type ToolResultBlock struct {
	ToolUseID string
	Content   string
	IsError   bool
}

func (TextBlock) isBlock()             {}
func (ImageBlock) isBlock()            {}
func (DocumentBlock) isBlock()         {}
func (ThinkingBlock) isBlock()         {}
func (RedactedThinkingBlock) isBlock() {}
func (ToolUseBlock) isBlock()          {}
func (ToolResultBlock) isBlock()       {}

// Message — один ход диалога.
type Message struct {
	Role    Role
	Content []Block
}

// ToolDef — объявление инструмента. Strict заставляет API гарантировать, что
// ToolUseBlock.Input валиден по Schema (кит шлёт с ним additionalProperties:false).
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // properties-объект JSON Schema
	Required    []string
	Strict      bool
}

// ToolChoice ограничивает выбор инструмента; Name — только для ToolChoiceTool.
type ToolChoice struct {
	Type ToolChoiceType
	Name string
}

// Request — нативный вход anthropickit (декаплинг потребителя от SDK).
type Request struct {
	Model           string // "" → Config.DefaultModel
	System          []TextBlock
	Messages        []Message
	MaxTokens       int64 // 0 → дефолт (sync/stream)
	Tools           []ToolDef
	ToolChoice      *ToolChoice
	Thinking        ThinkingMode
	ThinkingDisplay ThinkingDisplay // "" → поле не шлётся
	Effort          Effort
	StopSeqs        []string
	JSONSchema      map[string]any // structured output → output_config.format; nil = свободный вывод
	AutoCache       *CacheControl  // top-level cache_control: точку кэша расставляет API
	UserID          string         // metadata.user_id: непрозрачный id для abuse-детекта
}

// StopDetails — детали отказа; заполнены только при StopRefusal.
type StopDetails struct {
	Category    string
	Explanation string
}

// Response — нативный ответ с нормализованным Usage и посчитанной стоимостью.
// CostKnown=false — модели нет в прайс-таблице: Cost равен 0, fallback решает
// потребитель.
type Response struct {
	ID          string
	Model       string
	Role        Role
	Content     []Block
	StopReason  StopReason
	StopSeq     string
	StopDetails *StopDetails
	Usage       Usage
	Cost        USDMicros
	CostKnown   bool
}

// Text склеивает все TextBlock ответа.
func (r *Response) Text() string {
	var b strings.Builder
	for _, blk := range r.Content {
		if t, ok := blk.(TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// IsRefusal — отказ safety-классификатора: успешный 200, а не ошибка.
func (r *Response) IsRefusal() bool { return r.StopReason == StopRefusal }

// SystemText — удобный системный промпт одним блоком без кэша.
func SystemText(s string) []TextBlock { return []TextBlock{{Text: s}} }

// Complete — синхронный Messages-вызов. MaxTokens выше maxSyncTokens отвергается
// как ErrMaxTokensRequiresStreaming до HTTP-вызова.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	params, model, err := c.toParams(req, defaultMaxTokensSync)
	if err != nil {
		return nil, err
	}
	if params.MaxTokens > maxSyncTokens {
		return nil, fmt.Errorf("%w: MaxTokens %d exceeds the non-streaming limit %d, use Stream",
			ErrMaxTokensRequiresStreaming, params.MaxTokens, maxSyncTokens)
	}

	ctx, span := c.startSpan(ctx, opComplete, model, params.MaxTokens)
	defer span.End()

	start := time.Now()
	msg, err := c.sdk.Messages.New(ctx, params)
	if err != nil {
		err = classify(err)
		recordSpanError(span, err)
		c.observe(ctx, start, callResult{op: opComplete, model: model, priced: c.priced(model)}, err)
		return nil, err
	}
	resp := c.buildResponse(msg, model)
	annotateSpan(span, resp)
	c.observe(ctx, start, callResult{
		op: opComplete, model: model, status: statusOK,
		priced: resp.CostKnown, usage: resp.Usage, cost: resp.Cost,
	}, nil)
	return resp, nil
}

// CountTokens — предполётный подсчёт входных токенов под бюджетирование. Токены
// подсчёта не тарифицируются и в anthropic_api_tokens_total не попадают.
func (c *Client) CountTokens(ctx context.Context, req Request) (int64, error) {
	params, model, err := c.toParams(req, defaultMaxTokensSync)
	if err != nil {
		return 0, err
	}

	ctx, span := c.startSpan(ctx, opCountTokens, model, 0)
	defer span.End()

	start := time.Now()
	res, err := c.sdk.Messages.CountTokens(ctx, anthropic.MessageCountTokensParams{
		Model:      params.Model,
		Messages:   params.Messages,
		System:     anthropic.MessageCountTokensParamsSystemUnion{OfTextBlockArray: params.System},
		Tools:      toCountTokensTools(req),
		Thinking:   params.Thinking,
		ToolChoice: params.ToolChoice,
	})
	if err != nil {
		err = classify(err)
		recordSpanError(span, err)
		c.observe(ctx, start, callResult{op: opCountTokens, model: model, priced: c.priced(model)}, err)
		return 0, err
	}
	span.SetAttributes(genAIInputTokens.Int64(res.InputTokens))
	c.observe(ctx, start, callResult{
		op: opCountTokens, model: model, status: statusOK, priced: c.priced(model),
	}, nil)
	return res.InputTokens, nil
}

func (c *Client) buildResponse(m *anthropic.Message, model string) *Response {
	r := &Response{
		ID:         m.ID,
		Model:      m.Model,
		Role:       RoleAssistant,
		StopReason: StopReason(m.StopReason),
		StopSeq:    m.StopSequence,
		Usage:      mapUsage(m.Usage),
	}
	if r.Model == "" {
		r.Model = model
	}
	if r.StopReason == StopRefusal || m.StopDetails.Category != "" || m.StopDetails.Explanation != "" {
		r.StopDetails = &StopDetails{
			Category:    string(m.StopDetails.Category),
			Explanation: m.StopDetails.Explanation,
		}
	}
	for _, blk := range m.Content {
		switch b := blk.AsAny().(type) {
		case anthropic.TextBlock:
			r.Content = append(r.Content, TextBlock{Text: b.Text})
		case anthropic.ToolUseBlock:
			r.Content = append(r.Content, ToolUseBlock{ID: b.ID, Name: b.Name, Input: b.Input})
		case anthropic.ThinkingBlock:
			r.Content = append(r.Content, ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature})
		case anthropic.RedactedThinkingBlock:
			r.Content = append(r.Content, RedactedThinkingBlock{Data: b.Data})
		}
	}
	r.Cost, r.CostKnown = c.costOf(r.Model, r.Usage)
	return r
}

func (c *Client) toParams(req Request, defaultMax int64) (anthropic.MessageNewParams, string, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.DefaultModel
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = defaultMax
	}

	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Content))
		for _, b := range m.Content {
			cb, err := blockToParam(b)
			if err != nil {
				return anthropic.MessageNewParams{}, model, err
			}
			blocks = append(blocks, cb)
		}
		switch m.Role {
		case RoleAssistant:
			msgs = append(msgs, anthropic.NewAssistantMessage(blocks...))
		default:
			msgs = append(msgs, anthropic.NewUserMessage(blocks...))
		}
	}

	params := anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxTok,
		Messages:  msgs,
	}

	if len(req.System) > 0 {
		sys := make([]anthropic.TextBlockParam, 0, len(req.System))
		for _, tb := range req.System {
			p := anthropic.TextBlockParam{Text: tb.Text}
			if tb.Cache != nil {
				p.CacheControl = cacheControl(tb.Cache)
			}
			sys = append(sys, p)
		}
		params.System = sys
	}

	if len(req.StopSeqs) > 0 {
		params.StopSequences = req.StopSeqs
	}
	if req.AutoCache != nil {
		params.CacheControl = cacheControl(req.AutoCache)
	}
	if req.UserID != "" {
		params.Metadata = anthropic.MetadataParam{UserID: anthropic.String(req.UserID)}
	}

	if tools := toTools(req); len(tools) > 0 {
		params.Tools = tools
	}
	if tc := toToolChoice(req.ToolChoice); tc != nil {
		params.ToolChoice = *tc
	}

	switch req.Thinking {
	case ThinkingDisabled:
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
	default:
		adaptive := anthropic.ThinkingConfigAdaptiveParam{}
		if req.ThinkingDisplay != "" {
			adaptive.Display = anthropic.ThinkingConfigAdaptiveDisplay(req.ThinkingDisplay)
		}
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive}
	}

	if req.Effort != "" || req.JSONSchema != nil {
		oc := anthropic.OutputConfigParam{}
		if req.Effort != "" {
			oc.Effort = anthropic.OutputConfigEffort(req.Effort)
		}
		if req.JSONSchema != nil {
			oc.Format = anthropic.JSONOutputFormatParam{Schema: req.JSONSchema}
		}
		params.OutputConfig = oc
	}

	return params, model, nil
}

// cacheControl мапит точку кэша кита в параметр SDK; пустой TTL оставляет
// дефолт API (5 минут).
func cacheControl(c *CacheControl) anthropic.CacheControlEphemeralParam {
	p := anthropic.NewCacheControlEphemeralParam()
	if c != nil && c.TTL != "" {
		p.TTL = anthropic.CacheControlEphemeralTTL(c.TTL)
	}
	return p
}

func blockToParam(b Block) (anthropic.ContentBlockParamUnion, error) {
	switch v := b.(type) {
	case TextBlock:
		cb := anthropic.NewTextBlock(v.Text)
		if v.Cache != nil && cb.OfText != nil {
			cb.OfText.CacheControl = cacheControl(v.Cache)
		}
		return cb, nil
	case ImageBlock:
		var cb anthropic.ContentBlockParamUnion
		if v.URL != "" {
			cb = anthropic.NewImageBlock(anthropic.URLImageSourceParam{URL: v.URL})
		} else {
			cb = anthropic.NewImageBlockBase64(v.MediaType, v.Data)
		}
		if v.Cache != nil && cb.OfImage != nil {
			cb.OfImage.CacheControl = cacheControl(v.Cache)
		}
		return cb, nil
	case DocumentBlock:
		var cb anthropic.ContentBlockParamUnion
		if v.PDFBase64 != "" {
			cb = anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{Data: v.PDFBase64})
		} else {
			cb = anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{Data: v.Text})
		}
		if v.Cache != nil && cb.OfDocument != nil {
			cb.OfDocument.CacheControl = cacheControl(v.Cache)
		}
		return cb, nil
	case ThinkingBlock:
		return anthropic.ContentBlockParamUnion{
			OfThinking: &anthropic.ThinkingBlockParam{Thinking: v.Thinking, Signature: v.Signature},
		}, nil
	case RedactedThinkingBlock:
		return anthropic.NewRedactedThinkingBlock(v.Data), nil
	case ToolUseBlock:
		return anthropic.ContentBlockParamUnion{
			OfToolUse: &anthropic.ToolUseBlockParam{ID: v.ID, Name: v.Name, Input: v.Input},
		}, nil
	case ToolResultBlock:
		return anthropic.NewToolResultBlock(v.ToolUseID, v.Content, v.IsError), nil
	default:
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("%w: unknown block type %T", ErrInvalidRequest, b)
	}
}

// toolParam — единственный маппинг ToolDef; Messages и CountTokens отличаются
// только union'ом, в который он оборачивается.
func toolParam(td ToolDef) anthropic.ToolParam {
	tp := anthropic.ToolParam{
		Name:        td.Name,
		InputSchema: anthropic.ToolInputSchemaParam{Properties: td.Schema, Required: td.Required},
	}
	if td.Description != "" {
		tp.Description = anthropic.String(td.Description)
	}
	if td.Strict {
		// API отвергает strict-инструмент с открытой схемой.
		tp.Strict = anthropic.Bool(true)
		tp.InputSchema.ExtraFields = map[string]any{"additionalProperties": false}
	}
	return tp
}

func toTools(req Request) []anthropic.ToolUnionParam {
	if len(req.Tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(req.Tools))
	for _, td := range req.Tools {
		tp := toolParam(td)
		out = append(out, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return out
}

func toCountTokensTools(req Request) []anthropic.MessageCountTokensToolUnionParam {
	if len(req.Tools) == 0 {
		return nil
	}
	out := make([]anthropic.MessageCountTokensToolUnionParam, 0, len(req.Tools))
	for _, td := range req.Tools {
		tp := toolParam(td)
		out = append(out, anthropic.MessageCountTokensToolUnionParam{OfTool: &tp})
	}
	return out
}

func toToolChoice(tc *ToolChoice) *anthropic.ToolChoiceUnionParam {
	if tc == nil {
		return nil
	}
	switch tc.Type {
	case ToolChoiceAny:
		return &anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}}
	case ToolChoiceTool:
		return &anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{Name: tc.Name}}
	case ToolChoiceNone:
		return &anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
	default:
		return &anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	}
}
