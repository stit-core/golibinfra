package jsonb

import "testing"

func TestUnstorable(t *testing.T) {
	for _, c := range []struct {
		name, js          string
		want, wantReplace string
	}{
		{"plain", `{"a":"b","n":1}`, "", ""},
		{"NUL escape", `{"a":"x\u0000y"}`, "contains NUL", "contains NUL"},
		{"escaped backslash is text", `{"a":"\\u0000"}`, "", ""},
		{"lone high surrogate", `{"a":"\ud800"}`, "has an unpaired surrogate", "has an unpaired surrogate"},
		{"lone low surrogate", `{"a":"\udc00"}`, "has an unpaired surrogate", "has an unpaired surrogate"},
		{"high without low", `{"a":"\ud800\u0041"}`, "has an unpaired surrogate", "has an unpaired surrogate"},
		{"surrogate pair", `{"a":"\ud83d\ude00"}`, "", ""},
		{"raw invalid UTF-8", "{\"a\":\"\xff\"}", "is not valid UTF-8", "is not valid UTF-8"},
		{"replacement escape", `{"a":"\ufffd"}`, "", "is not valid UTF-8"},
		{"U+FFFD as bytes", "{\"a\":\"�\"}", "", ""},
	} {
		if got := Unstorable([]byte(c.js)); got != c.want {
			t.Errorf("%s: Unstorable(%s) = %q, want %q", c.name, c.js, got, c.want)
		}
		if got := UnstorableOrReplaced([]byte(c.js)); got != c.wantReplace {
			t.Errorf("%s: UnstorableOrReplaced(%s) = %q, want %q", c.name, c.js, got, c.wantReplace)
		}
	}
}
