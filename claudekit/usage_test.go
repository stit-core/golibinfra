package claudekit

import "testing"

func TestUsageTotalInput(t *testing.T) {
	u := Usage{InputTokens: 10, CacheWrite5mTokens: 5, CacheWrite1hTokens: 3, CacheReadTokens: 2}
	if got := u.TotalInput(); got != 20 {
		t.Fatalf("TotalInput() = %d, want 20", got)
	}
}
