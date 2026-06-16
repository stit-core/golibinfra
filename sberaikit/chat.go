package sberaikit

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleFunction  Role = "function"
)

type FinishReason string

const (
	FinishStop         FinishReason = "stop"
	FinishLength       FinishReason = "length"
	FinishFunctionCall FinishReason = "function_call"
	FinishBlacklist    FinishReason = "blacklist"
)

// Message — сообщение GigaChat. Attachments — file-id вложений (vision/doc; файл
// предварительно загружен POST /files). FunctionCall — на ответах ассистента.
type Message struct {
	Role             Role          `json:"role"`
	Content          string        `json:"content"`
	Name             string        `json:"name,omitempty"`          // role=function
	FunctionCall     *FunctionCall `json:"function_call,omitempty"` // ответ ассистента
	FunctionsStateID string        `json:"functions_state_id,omitempty"`
	Attachments      []string      `json:"attachments,omitempty"`
}

// FunctionCall — вызов функции. Arguments — json.RawMessage: GigaChat шлёт объект,
// OpenAI-слой — JSON-строку; декодим оба defensive (research §4).
type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Function — спека функции. FewShotExamples/ReturnParameters — Sber-расширения.
type Function struct {
	Name             string          `json:"name"`
	Description      string          `json:"description,omitempty"`
	Parameters       json.RawMessage `json:"parameters,omitempty"`
	FewShotExamples  json.RawMessage `json:"few_shot_examples,omitempty"`
	ReturnParameters json.RawMessage `json:"return_parameters,omitempty"`
}

// ChatRequest — нативный вход chat-вызова. function_call: "none"|"auto"|{name}.
// Для image-генерации: function_call="auto" + промпт с глаголом "нарисуй" →
// модель зовёт built-in text2image, в content вернётся <img src="file-id"/>.
type ChatRequest struct {
	Model             string     `json:"model"`
	Messages          []Message  `json:"messages"`
	Functions         []Function `json:"functions,omitempty"`
	FunctionCall      any        `json:"function_call,omitempty"`
	Temperature       *float64   `json:"temperature,omitempty"`
	TopP              *float64   `json:"top_p,omitempty"`
	N                 *int       `json:"n,omitempty"`
	MaxTokens         *int       `json:"max_tokens,omitempty"`
	RepetitionPenalty *float64   `json:"repetition_penalty,omitempty"`
	UpdateInterval    *int       `json:"update_interval,omitempty"`
	Stream            bool       `json:"stream,omitempty"`
}

// Choice — один вариант ответа (n>1 редко).
type Choice struct {
	Index        int          `json:"index"`
	Message      Message      `json:"message"`
	FinishReason FinishReason `json:"finish_reason"`
}

// ChatResponse — нативный ответ с нормализованным Usage и посчитанной стоимостью.
type ChatResponse struct {
	Object  string    `json:"object"`
	Created int64     `json:"created"`
	Model   string    `json:"model"`
	Choices []Choice  `json:"choices"`
	Usage   Usage     `json:"-"`
	Cost    RUBMicros `json:"-"`
}

// Text — конкатенация content всех choices (обычно один).
func (r *ChatResponse) Text() string {
	var b strings.Builder
	for _, c := range r.Choices {
		b.WriteString(c.Message.Content)
	}
	return b.String()
}

// imgSrcRe выдёргивает file-id из <img src="<uuid>" .../> в content ответа
// (image-as-file: text2image не отдаёт байты, только тег со ссылкой на файл).
var imgSrcRe = regexp.MustCompile(`<img\s+src="([^"]+)"`)

// ImageFileIDs — file-id всех <img src> в ответе (для последующего FileContent).
// Пусто, если генерации картинки не было.
func (r *ChatResponse) ImageFileIDs() []string {
	var ids []string
	for _, c := range r.Choices {
		for _, m := range imgSrcRe.FindAllStringSubmatch(c.Message.Content, -1) {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// chatRespWire — wire-форма (raw usage + extra-ключи). Usage держим отдельным
// rawUsage, а неизвестные usage-ключи ловим вторым проходом по json.RawMessage.
type chatRespWire struct {
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []Choice        `json:"choices"`
	Usage   json.RawMessage `json:"usage"`
}

// Chat — синхронный chat/completions. stream форсится в false (стрим — ChatStream).
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	model := c.model(req.Model)
	req.Model = model
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"

	start := c.clock.Now()
	var wire chatRespWire
	if err := c.doJSON(ctx, "chat", url, req, &wire); err != nil {
		c.instr.record(ctx, "chat", model, "error", c.clock.Now().Sub(start), Usage{}, 0)
		return nil, err
	}

	resp := &ChatResponse{
		Object:  wire.Object,
		Created: wire.Created,
		Model:   wire.Model,
		Choices: wire.Choices,
		Usage:   decodeUsage(wire.Usage),
	}
	if resp.Model == "" {
		resp.Model = model
	}
	resp.Cost = c.costOf(resp.Model, resp.Usage)
	c.instr.record(ctx, "chat", resp.Model, "ok", c.clock.Now().Sub(start), resp.Usage, resp.Cost)
	return resp, nil
}

// decodeUsage нормализует raw usage и удерживает неизвестные целочисленные ключи в
// Usage.Extra (forward-совместимость биллинга, research §7/§13).
func decodeUsage(raw json.RawMessage) Usage {
	if len(raw) == 0 {
		return Usage{}
	}
	var ru rawUsage
	_ = json.Unmarshal(raw, &ru)
	u := ru.normalize()

	var all map[string]json.RawMessage
	if json.Unmarshal(raw, &all) == nil {
		for k, v := range all {
			switch k {
			case "prompt_tokens", "completion_tokens", "total_tokens",
				"precached_prompt_tokens", "system_tokens":
				continue // известные — уже в normalize
			}
			if n, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64); err == nil {
				u.addExtra(k, n)
			}
		}
	}
	return u
}
