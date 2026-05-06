package logkit

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestServiceAttrs_InLog(t *testing.T) {
	resetAttrs()
	t.Cleanup(resetAttrs)

	cfg := ServiceConfig{
		Service: "my-svc",
		Env:     "test",
		Version: "1.2.3",
	}
	registerServiceAttrs(cfg)

	var buf bytes.Buffer
	l := NewWithWriter(Config{Level: "debug"}, &buf)
	l.Info(context.Background(), "hello")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, buf.String())
	}

	tests := []struct {
		field string
		want  string
	}{
		{"service", "my-svc"},
		{"env", "test"},
		{"version", "1.2.3"},
	}
	for _, tc := range tests {
		if m[tc.field] != tc.want {
			t.Errorf("field %q: want %q, got %v", tc.field, tc.want, m[tc.field])
		}
	}
}
