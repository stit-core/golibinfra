package claudekit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Дефолты max_tokens: sync держим под HTTP-таймаутами SDK, stream — щедро.
const (
	defaultMaxTokensSync   = 16_000
	defaultMaxTokensStream = 64_000
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type ThinkingMode int

const (
	ThinkingAdaptive ThinkingMode = iota // дефолт: модель сама решает глубину
	ThinkingDisabled
)

type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

type StopReason string

const (
	StopEndTurn         StopReason = "end_turn"
	StopMaxTokens       StopReason = "max_tokens"
	StopToolUse         StopReason = "tool_use"
	StopStopSequence    StopReason = "stop_sequence"
	StopRefusal         StopReason = "refusal"
	StopContextExceeded StopReason = "model_context_window_exceeded"
)

type CacheTTL string

const (
	CacheTTL5m CacheTTL = "5m"
	CacheTTL1h CacheTTL = "1h"
)

type CacheControl struct{ TTL CacheTTL }

type ToolChoiceType string

const (
	ToolChoiceAuto ToolChoiceType = "auto"
	ToolChoiceAny  ToolChoiceType = "any"
	ToolChoiceTool ToolChoiceType = "tool"
	ToolChoiceNone ToolChoiceType = "none"
)

// Block — sealed-интерфейс контент-блока (исчерпывается типами ниже).
type Block interface{ isBlock() }

type TextBlock struct {
	Text  string
	Cache *CacheControl
}

// ImageBlock: URL != "" → image по ссылке; иначе base64 (MediaType + Data).
type ImageBlock struct {
	MediaType string
	Data      string
	URL       string
	Cache     *CacheControl
}

// DocumentBlock: PDFBase64 != "" → PDF; иначе Text как plain-text документ.
type DocumentBlock struct {
	PDFBase64 string
	Text      string
	Cache     *CacheControl
}

type ThinkingBlock struct {
	Thinking  string
	Signature string // эхо обратно без изменений на той же модели
}

type ToolUseBlock struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type ToolResultBlock struct {
	ToolUseID string
	Content   string
	IsError   bool
}

func (TextBlock) isBlock()       {}
func (ImageBlock) isBlock()      {}
func (DocumentBlock) isBlock()   {}
func (ThinkingBlock) isBlock()   {}
func (ToolUseBlock) isBlock()    {}
func (ToolResultBlock) isBlock() {}

type Message struct {
	Role    Role
	Content []Block
}

type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema properties-объект
	Required    []string
}

type ToolChoice struct {
	Type ToolChoiceType
	Name string // только для Type == ToolChoiceTool
}

// Request — нативный вход claudekit (декаплинг потребителя от SDK).
type Request struct {
	Model      string // "" → Config.DefaultModel
	System     []TextBlock
	Messages   []Message
	MaxTokens  int64 // 0 → дефолт (sync/stream)
	Tools      []ToolDef
	ToolChoice *ToolChoice
	Thinking   ThinkingMode
	Effort     Effort
	StopSeqs   []string
}

// Response — нативный ответ с нормализованным Usage и посчитанной стоимостью.
type Response struct {
	ID         string
	Model      string
	Role       Role
	Content    []Block
	StopReason StopReason
	StopSeq    string
	Usage      Usage
	Cost       USDMicros
}

func (r *Response) Text() string {
	var b strings.Builder
	for _, blk := range r.Content {
		if t, ok := blk.(TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func (r *Response) IsRefusal() bool { return r.StopReason == StopRefusal }

// SystemText — удобный системный промпт одним блоком без кэша.
func SystemText(s string) []TextBlock { return []TextBlock{{Text: s}} }

// Complete — синхронный Messages-вызов.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	params, model, err := c.toParams(req, defaultMaxTokensSync)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	msg, err := c.sdk.Messages.New(ctx, params)
	if err != nil {
		c.instr.record(ctx, model, "error", time.Since(start), Usage{}, 0)
		return nil, classify(err)
	}
	resp := c.buildResponse(msg, model)
	c.instr.record(ctx, model, "ok", time.Since(start), resp.Usage, resp.Cost)
	return resp, nil
}

// CountTokens — предполётный подсчёт входных токенов под бюджетирование.
func (c *Client) CountTokens(ctx context.Context, req Request) (int64, error) {
	params, _, err := c.toParams(req, defaultMaxTokensSync)
	if err != nil {
		return 0, err
	}
	res, err := c.sdk.Messages.CountTokens(ctx, anthropic.MessageCountTokensParams{
		Model:      params.Model,
		Messages:   params.Messages,
		System:     anthropic.MessageCountTokensParamsSystemUnion{OfTextBlockArray: params.System},
		Tools:      toCountTokensTools(req),
		Thinking:   params.Thinking,
		ToolChoice: params.ToolChoice,
	})
	if err != nil {
		return 0, classify(err)
	}
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
	for _, blk := range m.Content {
		switch b := blk.AsAny().(type) {
		case anthropic.TextBlock:
			r.Content = append(r.Content, TextBlock{Text: b.Text})
		case anthropic.ToolUseBlock:
			r.Content = append(r.Content, ToolUseBlock{ID: b.ID, Name: b.Name, Input: b.Input})
		case anthropic.ThinkingBlock:
			r.Content = append(r.Content, ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature})
		}
	}
	r.Cost = c.costOf(r.Model, r.Usage)
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
				p.CacheControl = anthropic.NewCacheControlEphemeralParam()
			}
			sys = append(sys, p)
		}
		params.System = sys
	}

	if len(req.StopSeqs) > 0 {
		params.StopSequences = req.StopSeqs
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
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
	}

	if req.Effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(req.Effort)}
	}

	return params, model, nil
}

func blockToParam(b Block) (anthropic.ContentBlockParamUnion, error) {
	switch v := b.(type) {
	case TextBlock:
		cb := anthropic.NewTextBlock(v.Text)
		if v.Cache != nil && cb.OfText != nil {
			cb.OfText.CacheControl = anthropic.NewCacheControlEphemeralParam()
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
			cb.OfImage.CacheControl = anthropic.NewCacheControlEphemeralParam()
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
			cb.OfDocument.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		return cb, nil
	case ThinkingBlock:
		return anthropic.ContentBlockParamUnion{
			OfThinking: &anthropic.ThinkingBlockParam{Thinking: v.Thinking, Signature: v.Signature},
		}, nil
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

func toTools(req Request) []anthropic.ToolUnionParam {
	if len(req.Tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(req.Tools))
	for _, td := range req.Tools {
		tp := anthropic.ToolParam{
			Name:        td.Name,
			InputSchema: anthropic.ToolInputSchemaParam{Properties: td.Schema, Required: td.Required},
		}
		if td.Description != "" {
			tp.Description = anthropic.String(td.Description)
		}
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
		tp := anthropic.ToolParam{
			Name:        td.Name,
			InputSchema: anthropic.ToolInputSchemaParam{Properties: td.Schema, Required: td.Required},
		}
		if td.Description != "" {
			tp.Description = anthropic.String(td.Description)
		}
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
