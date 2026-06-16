package googleaikit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// Дефолт max_tokens: держим под HTTP-таймаутами SDK; 0 в запросе → без явного лимита.
const defaultMaxTokens = 8_192

type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

// ThinkingMode управляет reasoning-бюджетом Gemini 3 (thinkingLevel). Adaptive —
// дефолт SDK (поле не ставится). Disabled и Low/Medium/High — явные уровни.
type ThinkingMode int

const (
	ThinkingAdaptive ThinkingMode = iota
	ThinkingDisabled
	ThinkingLow
	ThinkingMedium
	ThinkingHigh
)

type StopReason string

const (
	StopUnspecified StopReason = ""
	StopEndTurn     StopReason = "STOP"
	StopMaxTokens   StopReason = "MAX_TOKENS"
	StopSafety      StopReason = "SAFETY"
	StopRecitation  StopReason = "RECITATION"
)

type ToolChoiceMode string

const (
	ToolChoiceAuto      ToolChoiceMode = "AUTO"
	ToolChoiceAny       ToolChoiceMode = "ANY"
	ToolChoiceNone      ToolChoiceMode = "NONE"
	ToolChoiceValidated ToolChoiceMode = "VALIDATED"
)

// Block — sealed-интерфейс контент-блока (исчерпывается типами ниже).
type Block interface{ isBlock() }

type TextBlock struct{ Text string }

// MediaBlock: FileURI != "" → fileData (files/… на AI-Studio, gs://… на Vertex);
// иначе inlineData (base64 байты + MIMEType). Покрывает image/audio/video/PDF-вход.
type MediaBlock struct {
	MIMEType string
	Data     []byte
	FileURI  string
}

type FunctionCallBlock struct {
	ID   string
	Name string
	Args map[string]any
}

type FunctionResponseBlock struct {
	ID       string
	Name     string
	Response map[string]any
}

// ThoughtBlock — reasoning-блок Gemini 3 с opaque-сигнатурой; эхо обратно без
// изменений в истории (строгая валидация function-calling).
type ThoughtBlock struct {
	Text      string
	Signature []byte
}

func (TextBlock) isBlock()             {}
func (MediaBlock) isBlock()            {}
func (FunctionCallBlock) isBlock()     {}
func (FunctionResponseBlock) isBlock() {}
func (ThoughtBlock) isBlock()          {}

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
	Mode    ToolChoiceMode
	Allowed []string // ограничивает ANY/VALIDATED
}

// Request — нативный вход generateContent (декаплинг потребителя от SDK).
type Request struct {
	Model        string // "" → Config.DefaultModel
	System       string // systemInstruction
	Messages     []Message
	MaxTokens    int32 // 0 → defaultMaxTokens
	Temperature  *float32
	TopP         *float32
	Tools        []ToolDef
	ToolChoice   *ToolChoice
	Thinking     ThinkingMode
	StopSeqs     []string
	GoogleSearch bool           // grounding-tool {google_search:{}}
	JSONSchema   map[string]any // structured output → responseMimeType+responseSchema; nil = свободный вывод
	Modalities   []string       // responseModalities (TEXT/IMAGE/AUDIO); nil = дефолт
}

// Response — нативный ответ с нормализованным Usage и посчитанной стоимостью.
type Response struct {
	ID         string
	Model      string
	Content    []Block
	StopReason StopReason
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

// GenerateContent — синхронный generateContent-вызов.
func (c *Client) GenerateContent(ctx context.Context, req Request) (*Response, error) {
	model := c.modelOr(req.Model)
	contents, cfg, err := c.toParams(req)
	if err != nil {
		return nil, err
	}
	start := c.clock.Now()
	resp, err := c.sdk.Models.GenerateContent(ctx, model, contents, cfg)
	if err != nil {
		c.instr.record(ctx, model, "generate", "error", c.clock.Now().Sub(start), Usage{}, 0)
		return nil, classify(err)
	}
	out := c.buildResponse(resp, model)
	c.instr.record(ctx, model, "generate", "ok", c.clock.Now().Sub(start), out.Usage, out.Cost)
	return out, nil
}

// CountTokens — предполётный подсчёт входных токенов под бюджетирование.
func (c *Client) CountTokens(ctx context.Context, req Request) (int64, error) {
	model := c.modelOr(req.Model)
	contents, _, err := c.toParams(req)
	if err != nil {
		return 0, err
	}
	res, err := c.sdk.Models.CountTokens(ctx, model, contents, nil)
	if err != nil {
		return 0, classify(err)
	}
	return int64(res.TotalTokens), nil
}

func (c *Client) buildResponse(resp *genai.GenerateContentResponse, model string) *Response {
	r := &Response{
		ID:    resp.ResponseID,
		Model: resp.ModelVersion,
		Usage: mapUsage(resp.UsageMetadata),
	}
	if r.Model == "" {
		r.Model = model
	}
	if len(resp.Candidates) > 0 {
		cand := resp.Candidates[0]
		r.StopReason = StopReason(cand.FinishReason)
		if cand.Content != nil {
			r.Content = mapParts(cand.Content.Parts)
		}
	}
	r.Cost = c.costOf(model, r.Usage)
	return r
}

// mapParts нормализует genai.Part → Block. Thought-части (Part.Thought) → ThoughtBlock.
func mapParts(parts []*genai.Part) []Block {
	out := make([]Block, 0, len(parts))
	for _, p := range parts {
		switch {
		case p == nil:
			continue
		case p.Thought:
			out = append(out, ThoughtBlock{Text: p.Text, Signature: p.ThoughtSignature})
		case p.Text != "":
			out = append(out, TextBlock{Text: p.Text})
		case p.FunctionCall != nil:
			out = append(out, FunctionCallBlock{ID: p.FunctionCall.ID, Name: p.FunctionCall.Name, Args: p.FunctionCall.Args})
		case p.InlineData != nil:
			out = append(out, MediaBlock{MIMEType: p.InlineData.MIMEType, Data: p.InlineData.Data})
		case p.FileData != nil:
			out = append(out, MediaBlock{MIMEType: p.FileData.MIMEType, FileURI: p.FileData.FileURI})
		}
	}
	return out
}

func (c *Client) toParams(req Request) ([]*genai.Content, *genai.GenerateContentConfig, error) {
	contents := make([]*genai.Content, 0, len(req.Messages))
	for _, m := range req.Messages {
		parts := make([]*genai.Part, 0, len(m.Content))
		for _, b := range m.Content {
			p, err := blockToPart(b)
			if err != nil {
				return nil, nil, err
			}
			parts = append(parts, p)
		}
		role := genai.Role(genai.RoleUser)
		if m.Role == RoleModel {
			role = genai.Role(genai.RoleModel)
		}
		contents = append(contents, genai.NewContentFromParts(parts, role))
	}

	cfg := &genai.GenerateContentConfig{
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if req.MaxTokens > 0 {
		cfg.MaxOutputTokens = req.MaxTokens
	} else {
		cfg.MaxOutputTokens = defaultMaxTokens
	}
	if req.System != "" {
		cfg.SystemInstruction = genai.NewContentFromText(req.System, genai.RoleUser)
	}
	if len(req.StopSeqs) > 0 {
		cfg.StopSequences = req.StopSeqs
	}
	if len(req.Modalities) > 0 {
		cfg.ResponseModalities = req.Modalities
	}
	if tc := thinkingConfig(req.Thinking); tc != nil {
		cfg.ThinkingConfig = tc
	}
	if tools := toTools(req); len(tools) > 0 {
		cfg.Tools = tools
	}
	if tcfg := toToolConfig(req.ToolChoice); tcfg != nil {
		cfg.ToolConfig = tcfg
	}
	if req.JSONSchema != nil {
		schema, err := schemaFromMap(req.JSONSchema)
		if err != nil {
			return nil, nil, err
		}
		cfg.ResponseMIMEType = "application/json"
		cfg.ResponseSchema = schema
	}
	return contents, cfg, nil
}

func blockToPart(b Block) (*genai.Part, error) {
	switch v := b.(type) {
	case TextBlock:
		return genai.NewPartFromText(v.Text), nil
	case MediaBlock:
		if v.FileURI != "" {
			return genai.NewPartFromURI(v.FileURI, v.MIMEType), nil
		}
		return genai.NewPartFromBytes(v.Data, v.MIMEType), nil
	case FunctionCallBlock:
		return &genai.Part{FunctionCall: &genai.FunctionCall{ID: v.ID, Name: v.Name, Args: v.Args}}, nil
	case FunctionResponseBlock:
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: v.ID, Name: v.Name, Response: v.Response}}, nil
	case ThoughtBlock:
		return &genai.Part{Text: v.Text, Thought: true, ThoughtSignature: v.Signature}, nil
	default:
		return nil, fmt.Errorf("%w: unknown block type %T", ErrInvalidRequest, b)
	}
}

func thinkingConfig(m ThinkingMode) *genai.ThinkingConfig {
	switch m {
	case ThinkingDisabled:
		return &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelMinimal}
	case ThinkingLow:
		return &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelLow, IncludeThoughts: true}
	case ThinkingMedium:
		return &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelMedium, IncludeThoughts: true}
	case ThinkingHigh:
		return &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelHigh, IncludeThoughts: true}
	default:
		return nil // adaptive: SDK/модель решают
	}
}

func toTools(req Request) []*genai.Tool {
	var tools []*genai.Tool
	if len(req.Tools) > 0 {
		decls := make([]*genai.FunctionDeclaration, 0, len(req.Tools))
		for _, td := range req.Tools {
			decls = append(decls, &genai.FunctionDeclaration{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  objectSchema(td.Schema, td.Required),
			})
		}
		tools = append(tools, &genai.Tool{FunctionDeclarations: decls})
	}
	if req.GoogleSearch {
		tools = append(tools, &genai.Tool{GoogleSearch: &genai.GoogleSearch{}})
	}
	return tools
}

func toToolConfig(tc *ToolChoice) *genai.ToolConfig {
	if tc == nil {
		return nil
	}
	mode := genai.FunctionCallingConfigModeAuto
	switch tc.Mode {
	case ToolChoiceAny:
		mode = genai.FunctionCallingConfigModeAny
	case ToolChoiceNone:
		mode = genai.FunctionCallingConfigModeNone
	case ToolChoiceValidated:
		mode = genai.FunctionCallingConfigModeValidated
	}
	return &genai.ToolConfig{
		FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: mode, AllowedFunctionNames: tc.Allowed},
	}
}

// objectSchema оборачивает properties-объект потребителя в OBJECT-Schema.
func objectSchema(props map[string]any, required []string) *genai.Schema {
	s := &genai.Schema{Type: genai.TypeObject, Required: required}
	if len(props) > 0 {
		s.Properties = make(map[string]*genai.Schema, len(props))
		for name, raw := range props {
			if sub, err := toSchema(raw); err == nil && sub != nil {
				s.Properties[name] = sub
			}
		}
	}
	return s
}

// schemaFromMap парсит полную JSON-Schema потребителя (structured output).
func schemaFromMap(m map[string]any) (*genai.Schema, error) {
	s, err := toSchema(m)
	if err != nil {
		return nil, fmt.Errorf("%w: response schema: %w", ErrInvalidRequest, err)
	}
	return s, nil
}

// toSchema конвертирует произвольную JSON-Schema-карту в genai.Schema через
// JSON-раунд-трип (поля genai.Schema несут json-теги OpenAPI-формы).
func toSchema(raw any) (*genai.Schema, error) {
	if raw == nil {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var s genai.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
