package yandexaikit

import "encoding/json"

// Wire-структуры нативного Yandex FM REST (proto-JSON, lowerCamelCase). Числовые
// int64-поля сериализуются как JSON-строки (proto-JSON rule) — отмечены `,string`.
// Nullable proto-wrappers (temperature/maxTokens/parallelToolCalls) — указатели +
// omitempty: "unset" ≠ "zero". Источник истины полей — go-genproto (research §12).

// --- Роли / статусы / enum'ы ---

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// AlternativeStatus — статус варианта ответа (research §3.1).
type AlternativeStatus string

const (
	StatusUnspecified    AlternativeStatus = "ALTERNATIVE_STATUS_UNSPECIFIED"
	StatusPartial        AlternativeStatus = "ALTERNATIVE_STATUS_PARTIAL" // стрим-чанк
	StatusTruncatedFinal AlternativeStatus = "ALTERNATIVE_STATUS_TRUNCATED_FINAL"
	StatusFinal          AlternativeStatus = "ALTERNATIVE_STATUS_FINAL"
	StatusContentFilter  AlternativeStatus = "ALTERNATIVE_STATUS_CONTENT_FILTER"
	StatusToolCalls      AlternativeStatus = "ALTERNATIVE_STATUS_TOOL_CALLS"
)

// IsFinal — терминальный статус (не PARTIAL): на нём стрим завершён.
func (s AlternativeStatus) IsFinal() bool {
	return s == StatusFinal || s == StatusTruncatedFinal || s == StatusContentFilter || s == StatusToolCalls
}

// ReasoningMode — completionOptions.reasoningOptions.mode.
type ReasoningMode string

const (
	ReasoningUnspecified   ReasoningMode = "REASONING_MODE_UNSPECIFIED"
	ReasoningDisabled      ReasoningMode = "DISABLED"
	ReasoningEnabledHidden ReasoningMode = "ENABLED_HIDDEN"
)

// ToolChoiceMode — toolChoice.mode.
type ToolChoiceMode string

const (
	ToolChoiceNone     ToolChoiceMode = "NONE"
	ToolChoiceAuto     ToolChoiceMode = "AUTO"
	ToolChoiceRequired ToolChoiceMode = "REQUIRED"
)

// --- Completion request ---

type CompletionRequest struct {
	ModelURI          string            `json:"modelUri"`
	CompletionOptions CompletionOptions `json:"completionOptions"`
	Messages          []Message         `json:"messages"`
	Tools             []Tool            `json:"tools,omitempty"`
	ToolChoice        *ToolChoice       `json:"toolChoice,omitempty"`
	ParallelToolCalls *bool             `json:"parallelToolCalls,omitempty"`
	// oneof responseFormat (set at most one):
	JSONObject *bool       `json:"jsonObject,omitempty"`
	JSONSchema *JSONSchema `json:"jsonSchema,omitempty"`
}

type CompletionOptions struct {
	Stream           bool              `json:"stream"`
	Temperature      *float64          `json:"temperature,omitempty"`
	MaxTokens        *int64            `json:"maxTokens,string,omitempty"` // int64 как JSON-строка
	ReasoningOptions *ReasoningOptions `json:"reasoningOptions,omitempty"`
}

type ReasoningOptions struct {
	Mode ReasoningMode `json:"mode"`
}

type Message struct {
	Role           Role            `json:"role"`
	Text           string          `json:"text,omitempty"`           // oneof content
	ToolCallList   *ToolCallList   `json:"toolCallList,omitempty"`   // assistant tool-call request
	ToolResultList *ToolResultList `json:"toolResultList,omitempty"` // tool results (на user-сообщении)
}

type Tool struct {
	Function FunctionTool `json:"function"`
}

type FunctionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema объект
	Strict      *bool           `json:"strict,omitempty"`
}

type ToolChoice struct {
	// set one: Mode OR FunctionName
	Mode         ToolChoiceMode `json:"mode,omitempty"`
	FunctionName string         `json:"functionName,omitempty"`
}

type ToolCallList struct {
	ToolCalls []ToolCall `json:"toolCalls"`
}

type ToolCall struct {
	FunctionCall FunctionCall `json:"functionCall"`
}

type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // распарсенный JSON-объект, НЕ строка
}

type ToolResultList struct {
	ToolResults []ToolResult `json:"toolResults"`
}

type ToolResult struct {
	FunctionResult FunctionResult `json:"functionResult"`
}

type FunctionResult struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type JSONSchema struct {
	Schema json.RawMessage `json:"schema"` // JSON Schema объект
}

// --- Completion response (sync: завёрнут в {"result": …}) ---

type CompletionEnvelope struct {
	Result CompletionResult `json:"result"`
}

type CompletionResult struct {
	Alternatives []Alternative `json:"alternatives"`
	Usage        ContentUsage  `json:"usage"`
	ModelVersion string        `json:"modelVersion"`
}

type Alternative struct {
	Message Message           `json:"message"`
	Status  AlternativeStatus `json:"status"`
}

type ContentUsage struct {
	InputTextTokens         int64                    `json:"inputTextTokens,string"`
	CompletionTokens        int64                    `json:"completionTokens,string"`
	TotalTokens             int64                    `json:"totalTokens,string"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completionTokensDetails,omitempty"`
}

type CompletionTokensDetails struct {
	ReasoningTokens int64 `json:"reasoningTokens,string"`
}

// --- Async long-running Operation (research §10.3) ---

type Operation struct {
	ID          string          `json:"id"`
	Description string          `json:"description,omitempty"`
	CreatedAt   string          `json:"createdAt,omitempty"` // RFC3339
	CreatedBy   string          `json:"createdBy,omitempty"`
	ModifiedAt  string          `json:"modifiedAt,omitempty"`
	Done        bool            `json:"done"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	// oneof result (present only when Done):
	Response json.RawMessage `json:"response,omitempty"` // типизируется @type-дискриминатором
	Error    *rpcStatus      `json:"error,omitempty"`    // google.rpc.Status
}

// rpcStatus — google.rpc.Status: тело sync REST-ошибки И поле Operation.error.
type rpcStatus struct {
	Code    int32             `json:"code"`
	Message string            `json:"message"`
	Details []json.RawMessage `json:"details,omitempty"`
}

// --- Embeddings ---

type EmbeddingRequest struct {
	ModelURI string `json:"modelUri"`
	Text     string `json:"text"`
	Dim      *int64 `json:"dim,string,omitempty"`
}

type EmbeddingResponse struct {
	Embedding    []float64 `json:"embedding"`
	NumTokens    int64     `json:"numTokens,string"`
	ModelVersion string    `json:"modelVersion"`
}

// --- YandexART (request; результат — из Operation.Response) ---

type ImageGenerationRequest struct {
	ModelURI          string                 `json:"modelUri"`
	Messages          []ImageMessage         `json:"messages"`
	GenerationOptions ImageGenerationOptions `json:"generationOptions"`
}

type ImageMessage struct {
	Text   string  `json:"text"`
	Weight float64 `json:"weight,omitempty"`
}

type ImageGenerationOptions struct {
	MimeType    string       `json:"mimeType,omitempty"`
	Seed        *int64       `json:"seed,string,omitempty"`
	AspectRatio *AspectRatio `json:"aspectRatio,omitempty"`
}

type AspectRatio struct {
	WidthRatio  int64 `json:"widthRatio,string"`
	HeightRatio int64 `json:"heightRatio,string"`
}

// ImageGenerationResponse — payload Operation.Response для YandexART.
type ImageGenerationResponse struct {
	Image        string `json:"image"` // base64 байты
	ModelVersion string `json:"modelVersion"`
}
