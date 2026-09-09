package amocrmkit

import (
	"errors"
	"testing"
)

func TestClassify_StatusToSentinel(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{401, ErrAuth},
		{403, ErrForbidden},
		{404, ErrNotFound},
		{400, ErrValidation},
		{422, ErrValidation},
		{429, ErrRateLimited},
		{500, ErrServer},
		{503, ErrServer},
		{402, ErrPaymentRequired},
		{409, ErrValidation}, // прочие 4xx → validation
	}
	for _, tc := range cases {
		if got := classify(tc.status, nil); !errors.Is(got, tc.want) {
			t.Errorf("classify(%d) = %v, want Is %v", tc.status, got, tc.want)
		}
	}
}

func TestClassify_WrapsAPIError(t *testing.T) {
	apiErr := &APIError{Status: 400, Title: "Bad Request", Detail: "boom", RetryAfter: 0}
	err := classify(400, apiErr)

	if !errors.Is(err, ErrValidation) {
		t.Error("expected Is ErrValidation")
	}
	var got *APIError
	if !errors.As(err, &got) {
		t.Fatal("expected As *APIError")
	}
	if got.Detail != "boom" {
		t.Errorf("APIError.Detail = %q, want boom", got.Detail)
	}
}
