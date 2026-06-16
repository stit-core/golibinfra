package googleaikit

import (
	"errors"
	"testing"

	"google.golang.org/genai"
)

func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{APIKey: "k", DefaultModel: "gemini-2.5-flash"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestToParamsStructuredOutput(t *testing.T) {
	c := newTestClient(t)
	schema := map[string]any{
		"type":       "OBJECT",
		"properties": map[string]any{"name": map[string]any{"type": "STRING"}},
	}
	_, cfg, err := c.toParams(Request{
		Messages:   []Message{{Role: RoleUser, Content: []Block{TextBlock{Text: "hi"}}}},
		JSONSchema: schema,
	})
	if err != nil {
		t.Fatalf("toParams: %v", err)
	}
	if cfg.ResponseMIMEType != "application/json" {
		t.Fatalf("ResponseMIMEType = %q", cfg.ResponseMIMEType)
	}
	if cfg.ResponseSchema == nil || cfg.ResponseSchema.Type != genai.TypeObject {
		t.Fatalf("ResponseSchema not parsed: %+v", cfg.ResponseSchema)
	}
}

func TestToParamsModalitiesAndSystem(t *testing.T) {
	c := newTestClient(t)
	contents, cfg, err := c.toParams(Request{
		System:     "be brief",
		Messages:   []Message{{Role: RoleUser, Content: []Block{TextBlock{Text: "hi"}}}},
		Modalities: []string{"TEXT", "IMAGE"},
	})
	if err != nil {
		t.Fatalf("toParams: %v", err)
	}
	if len(contents) != 1 || contents[0].Role != genai.RoleUser {
		t.Fatalf("contents = %+v", contents)
	}
	if cfg.SystemInstruction == nil {
		t.Fatal("SystemInstruction not set")
	}
	if len(cfg.ResponseModalities) != 2 {
		t.Fatalf("ResponseModalities = %v", cfg.ResponseModalities)
	}
}

func TestThinkingConfig(t *testing.T) {
	if thinkingConfig(ThinkingAdaptive) != nil {
		t.Fatal("adaptive should produce nil ThinkingConfig")
	}
	if tc := thinkingConfig(ThinkingDisabled); tc == nil || tc.ThinkingLevel != genai.ThinkingLevelMinimal {
		t.Fatalf("disabled = %+v", tc)
	}
	if tc := thinkingConfig(ThinkingHigh); tc == nil || tc.ThinkingLevel != genai.ThinkingLevelHigh || !tc.IncludeThoughts {
		t.Fatalf("high = %+v", tc)
	}
}

func TestToToolsFunctionAndSearch(t *testing.T) {
	tools := toTools(Request{
		Tools: []ToolDef{{
			Name:     "get_weather",
			Schema:   map[string]any{"city": map[string]any{"type": "STRING"}},
			Required: []string{"city"},
		}},
		GoogleSearch: true,
	})
	if len(tools) != 2 {
		t.Fatalf("want 2 tools (function + search), got %d", len(tools))
	}
	if len(tools[0].FunctionDeclarations) != 1 || tools[0].FunctionDeclarations[0].Name != "get_weather" {
		t.Fatalf("function decl = %+v", tools[0])
	}
	if tools[0].FunctionDeclarations[0].Parameters.Type != genai.TypeObject {
		t.Fatal("function parameters not wrapped as OBJECT schema")
	}
	if tools[1].GoogleSearch == nil {
		t.Fatal("google_search tool missing")
	}
}

func TestToToolConfigModes(t *testing.T) {
	tc := toToolConfig(&ToolChoice{Mode: ToolChoiceAny, Allowed: []string{"f"}})
	if tc == nil || tc.FunctionCallingConfig.Mode != genai.FunctionCallingConfigModeAny {
		t.Fatalf("any mode = %+v", tc)
	}
	if toToolConfig(nil) != nil {
		t.Fatal("nil ToolChoice should yield nil config")
	}
}

func TestBlockToPartMedia(t *testing.T) {
	p, err := blockToPart(MediaBlock{FileURI: "files/abc", MIMEType: "image/png"})
	if err != nil {
		t.Fatalf("blockToPart: %v", err)
	}
	if p.FileData == nil || p.FileData.FileURI != "files/abc" {
		t.Fatalf("fileData not set: %+v", p)
	}
	p2, err := blockToPart(MediaBlock{Data: []byte("x"), MIMEType: "image/png"})
	if err != nil {
		t.Fatalf("blockToPart: %v", err)
	}
	if p2.InlineData == nil {
		t.Fatalf("inlineData not set: %+v", p2)
	}
}

func TestBlockToPartThought(t *testing.T) {
	p, err := blockToPart(ThoughtBlock{Text: "reason", Signature: []byte("sig")})
	if err != nil {
		t.Fatalf("blockToPart: %v", err)
	}
	if !p.Thought || string(p.ThoughtSignature) != "sig" {
		t.Fatalf("thought part = %+v", p)
	}
}

func TestBlockToPartUnknown(t *testing.T) {
	type bogus struct{ Block }
	if _, err := blockToPart(bogus{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}
