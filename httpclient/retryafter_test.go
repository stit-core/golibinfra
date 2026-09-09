package httpclient

import (
	"math"
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	cases := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{name: "empty", value: ""},
		{name: "seconds", value: "3", want: 3 * time.Second, ok: true},
		{name: "zero", value: "0", ok: true},
		{name: "fractional rounds up", value: "1.5", want: 2 * time.Second, ok: true},
		{name: "negative", value: "-5"},
		{name: "garbage", value: "soon"},
		{name: "infinity", value: "Inf"},
		{name: "not a number", value: "NaN"},
		{name: "saturates instead of overflowing", value: "99999999999", want: math.MaxInt64, ok: true},
		{name: "http date", value: now.Add(90 * time.Second).Format(http.TimeFormat), want: 90 * time.Second, ok: true},
		{name: "http date in the past", value: now.Add(-time.Hour).Format(http.TimeFormat), ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseRetryAfter(tc.value, now)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("ParseRetryAfter(%q) = %v, %v; want %v, %v", tc.value, got, ok, tc.want, tc.ok)
			}
		})
	}
}
