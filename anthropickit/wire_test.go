package anthropickit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// wireBody runs Complete against a stub server and returns the JSON body the
// SDK actually put on the wire.
func wireBody(t *testing.T, req Request) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return body
}

// dig walks a decoded JSON body; a numeric segment indexes an array.
func dig(t *testing.T, v any, path ...string) any {
	t.Helper()
	for _, p := range path {
		switch node := v.(type) {
		case map[string]any:
			v = node[p]
		case []any:
			idx := 0
			for _, c := range p {
				idx = idx*10 + int(c-'0')
			}
			if idx >= len(node) {
				t.Fatalf("index %s out of range in %v", p, node)
			}
			v = node[idx]
		default:
			t.Fatalf("cannot walk %q into %T", p, v)
		}
	}
	return v
}

func userBlocks(blocks ...Block) []Message {
	return []Message{{Role: RoleUser, Content: blocks}}
}

func TestWire_RequestMapping(t *testing.T) {
	schema := map[string]any{"city": map[string]any{"type": "string"}}
	cases := []struct {
		name string
		req  Request
		path []string
		want any
	}{
		{
			name: "cache control 1h reaches the wire",
			req:  Request{Messages: userBlocks(TextBlock{Text: "hi", Cache: &CacheControl{TTL: CacheTTL1h}})},
			path: []string{"messages", "0", "content", "0", "cache_control"},
			want: map[string]any{"type": "ephemeral", "ttl": "1h"},
		},
		{
			name: "cache control 5m is explicit too",
			req:  Request{Messages: userBlocks(TextBlock{Text: "hi", Cache: &CacheControl{TTL: CacheTTL5m}})},
			path: []string{"messages", "0", "content", "0", "cache_control"},
			want: map[string]any{"type": "ephemeral", "ttl": "5m"},
		},
		{
			name: "cache control without TTL keeps the API default",
			req:  Request{Messages: userBlocks(TextBlock{Text: "hi", Cache: &CacheControl{}})},
			path: []string{"messages", "0", "content", "0", "cache_control"},
			want: map[string]any{"type": "ephemeral"},
		},
		{
			name: "system block carries its TTL",
			req: Request{
				System:   []TextBlock{{Text: "sys", Cache: &CacheControl{TTL: CacheTTL1h}}},
				Messages: userBlocks(TextBlock{Text: "hi"}),
			},
			path: []string{"system", "0", "cache_control", "ttl"},
			want: "1h",
		},
		{
			name: "image by URL",
			req:  Request{Messages: userBlocks(ImageBlock{URL: "https://example.test/a.png"})},
			path: []string{"messages", "0", "content", "0", "source"},
			want: map[string]any{"type": "url", "url": "https://example.test/a.png"},
		},
		{
			name: "image base64 with its TTL",
			req: Request{Messages: userBlocks(ImageBlock{
				MediaType: "image/png", Data: "Zm9v", Cache: &CacheControl{TTL: CacheTTL1h},
			})},
			path: []string{"messages", "0", "content", "0", "cache_control", "ttl"},
			want: "1h",
		},
		{
			name: "document PDF",
			req:  Request{Messages: userBlocks(DocumentBlock{PDFBase64: "JVBER"})},
			path: []string{"messages", "0", "content", "0", "source"},
			want: map[string]any{"type": "base64", "media_type": "application/pdf", "data": "JVBER"},
		},
		{
			name: "document plain text with its TTL",
			req: Request{Messages: userBlocks(DocumentBlock{
				Text: "plain", Cache: &CacheControl{TTL: CacheTTL1h},
			})},
			path: []string{"messages", "0", "content", "0", "cache_control", "ttl"},
			want: "1h",
		},
		{
			name: "thinking block replays verbatim",
			req:  Request{Messages: userBlocks(ThinkingBlock{Thinking: "step", Signature: "sig"})},
			path: []string{"messages", "0", "content", "0"},
			want: map[string]any{"type": "thinking", "thinking": "step", "signature": "sig"},
		},
		{
			name: "redacted thinking block replays verbatim",
			req:  Request{Messages: userBlocks(RedactedThinkingBlock{Data: "opaque"})},
			path: []string{"messages", "0", "content", "0"},
			want: map[string]any{"type": "redacted_thinking", "data": "opaque"},
		},
		{
			name: "tool use block",
			req: Request{Messages: userBlocks(ToolUseBlock{
				ID: "toolu_1", Name: "get_weather", Input: json.RawMessage(`{"city":"Paris"}`),
			})},
			path: []string{"messages", "0", "content", "0"},
			want: map[string]any{
				"type": "tool_use", "id": "toolu_1", "name": "get_weather",
				"input": map[string]any{"city": "Paris"},
			},
		},
		{
			name: "tool result block",
			req:  Request{Messages: userBlocks(ToolResultBlock{ToolUseID: "toolu_1", Content: "+3", IsError: true})},
			path: []string{"messages", "0", "content", "0"},
			want: map[string]any{
				"type": "tool_result", "tool_use_id": "toolu_1", "is_error": true,
				"content": []any{map[string]any{"type": "text", "text": "+3"}},
			},
		},
		{
			name: "tool definition",
			req: Request{
				Messages: userBlocks(TextBlock{Text: "hi"}),
				Tools:    []ToolDef{{Name: "get_weather", Description: "weather", Schema: schema, Required: []string{"city"}}},
			},
			path: []string{"tools", "0"},
			want: map[string]any{
				"name": "get_weather", "description": "weather",
				"input_schema": map[string]any{
					"type": "object", "properties": schema, "required": []any{"city"},
				},
			},
		},
		{
			name: "strict tool closes the schema",
			req: Request{
				Messages: userBlocks(TextBlock{Text: "hi"}),
				Tools:    []ToolDef{{Name: "get_weather", Schema: schema, Strict: true}},
			},
			path: []string{"tools", "0"},
			want: map[string]any{
				"name": "get_weather", "strict": true,
				"input_schema": map[string]any{
					"type": "object", "properties": schema, "additionalProperties": false,
				},
			},
		},
		{
			name: "tool choice auto",
			req: Request{
				Messages:   userBlocks(TextBlock{Text: "hi"}),
				ToolChoice: &ToolChoice{Type: ToolChoiceAuto},
			},
			path: []string{"tool_choice"},
			want: map[string]any{"type": "auto"},
		},
		{
			name: "tool choice any",
			req: Request{
				Messages:   userBlocks(TextBlock{Text: "hi"}),
				ToolChoice: &ToolChoice{Type: ToolChoiceAny},
			},
			path: []string{"tool_choice"},
			want: map[string]any{"type": "any"},
		},
		{
			name: "tool choice tool",
			req: Request{
				Messages:   userBlocks(TextBlock{Text: "hi"}),
				ToolChoice: &ToolChoice{Type: ToolChoiceTool, Name: "get_weather"},
			},
			path: []string{"tool_choice"},
			want: map[string]any{"type": "tool", "name": "get_weather"},
		},
		{
			name: "tool choice none",
			req: Request{
				Messages:   userBlocks(TextBlock{Text: "hi"}),
				ToolChoice: &ToolChoice{Type: ToolChoiceNone},
			},
			path: []string{"tool_choice"},
			want: map[string]any{"type": "none"},
		},
		{
			name: "thinking disabled",
			req:  Request{Messages: userBlocks(TextBlock{Text: "hi"}), Thinking: ThinkingDisabled},
			path: []string{"thinking"},
			want: map[string]any{"type": "disabled"},
		},
		{
			name: "thinking display summarized",
			req: Request{
				Messages:        userBlocks(TextBlock{Text: "hi"}),
				ThinkingDisplay: ThinkingDisplaySummarized,
			},
			path: []string{"thinking"},
			want: map[string]any{"type": "adaptive", "display": "summarized"},
		},
		{
			name: "thinking display omitted by default is not sent",
			req:  Request{Messages: userBlocks(TextBlock{Text: "hi"})},
			path: []string{"thinking"},
			want: map[string]any{"type": "adaptive"},
		},
		{
			name: "effort",
			req:  Request{Messages: userBlocks(TextBlock{Text: "hi"}), Effort: EffortXHigh},
			path: []string{"output_config", "effort"},
			want: "xhigh",
		},
		{
			name: "structured output schema",
			req: Request{
				Messages:   userBlocks(TextBlock{Text: "hi"}),
				JSONSchema: map[string]any{"type": "object"},
			},
			path: []string{"output_config", "format"},
			want: map[string]any{"type": "json_schema", "schema": map[string]any{"type": "object"}},
		},
		{
			name: "stop sequences",
			req:  Request{Messages: userBlocks(TextBlock{Text: "hi"}), StopSeqs: []string{"END"}},
			path: []string{"stop_sequences"},
			want: []any{"END"},
		},
		{
			name: "auto cache breakpoint",
			req: Request{
				Messages:  userBlocks(TextBlock{Text: "hi"}),
				AutoCache: &CacheControl{TTL: CacheTTL1h},
			},
			path: []string{"cache_control"},
			want: map[string]any{"type": "ephemeral", "ttl": "1h"},
		},
		{
			name: "metadata user id",
			req:  Request{Messages: userBlocks(TextBlock{Text: "hi"}), UserID: "u-42"},
			path: []string{"metadata"},
			want: map[string]any{"user_id": "u-42"},
		},
		{
			name: "assistant turn keeps its role",
			req:  Request{Messages: []Message{{Role: RoleAssistant, Content: []Block{TextBlock{Text: "hi"}}}}},
			path: []string{"messages", "0", "role"},
			want: "assistant",
		},
		{
			name: "explicit max tokens wins over the default",
			req:  Request{Messages: userBlocks(TextBlock{Text: "hi"}), MaxTokens: 512},
			path: []string{"max_tokens"},
			want: float64(512),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dig(t, wireBody(t, tc.req), tc.path...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%v = %#v, want %#v", tc.path, got, tc.want)
			}
		})
	}
}

func TestWire_OptionalFieldsAreOmitted(t *testing.T) {
	body := wireBody(t, Request{Messages: userBlocks(TextBlock{Text: "hi"})})
	for _, key := range []string{"cache_control", "metadata", "tools", "tool_choice", "stop_sequences", "output_config"} {
		if _, ok := body[key]; ok {
			t.Errorf("%s must not be sent when unset: %v", key, body[key])
		}
	}
}

func TestWire_CountTokensSendsTools(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens": 7}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	n, err := c.CountTokens(context.Background(), Request{
		System:   SystemText("sys"),
		Messages: userBlocks(TextBlock{Text: "hi"}),
		Tools:    []ToolDef{{Name: "get_weather", Strict: true, Schema: map[string]any{}}},
	})
	if err != nil || n != 7 {
		t.Fatalf("CountTokens = %d, %v", n, err)
	}
	if got := dig(t, body, "tools", "0", "strict"); got != true {
		t.Errorf("count_tokens tools = %v, want the same mapping as messages", body["tools"])
	}
	if _, ok := body["max_tokens"]; ok {
		t.Errorf("count_tokens must not carry max_tokens: %v", body["max_tokens"])
	}
}

func TestComplete_MaxTokensAboveSyncLimitRequiresStreaming(t *testing.T) {
	reader, _ := installOTel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no HTTP call must be made")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	req := userRequest("hi")
	req.MaxTokens = maxSyncTokens + 1
	_, err := c.Complete(context.Background(), req)
	if !errors.Is(err, ErrMaxTokensRequiresStreaming) {
		t.Fatalf("err = %v, want ErrMaxTokensRequiresStreaming", err)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", nil); got != 0 {
		t.Errorf("a call that never happened must not be counted: %d", got)
	}
}
