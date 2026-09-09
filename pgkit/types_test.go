package pgkit

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestTimestamptz(t *testing.T) {
	t.Run("nil → invalid (NULL)", func(t *testing.T) {
		if got := Timestamptz(nil); got.Valid {
			t.Fatalf("Timestamptz(nil).Valid = true, want false")
		}
	})
	t.Run("value → valid", func(t *testing.T) {
		now := time.Now()
		got := Timestamptz(&now)
		if !got.Valid || !got.Time.Equal(now) {
			t.Fatalf("Timestamptz(&now) = %+v, want valid %v", got, now)
		}
	})
	t.Run("round-trips with NullableTime", func(t *testing.T) {
		now := time.Now()
		if got := NullableTime(Timestamptz(&now)); got == nil || !got.Equal(now) {
			t.Fatalf("NullableTime(Timestamptz(&now)) = %v, want %v", got, now)
		}
		if got := NullableTime(Timestamptz(nil)); got != nil {
			t.Fatalf("NullableTime(Timestamptz(nil)) = %v, want nil", got)
		}
	})
}

func TestParseUUID(t *testing.T) {
	t.Parallel()
	const canonical = "0192f5b6-6a3d-7c4e-8b1a-2f3c4d5e6f70"

	tests := []struct {
		name     string
		in       string
		wantErr  error
		wantStr  string
		wantOKay bool
	}{
		{name: "canonical", in: canonical, wantStr: canonical, wantOKay: true},
		{name: "braced form normalizes", in: "{" + canonical + "}", wantStr: canonical, wantOKay: true},
		{name: "empty", in: "", wantErr: ErrEmptyUUID},
		{name: "not a uuid", in: "not-a-uuid", wantErr: ErrInvalidUUID},
		{name: "truncated", in: canonical[:20], wantErr: ErrInvalidUUID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseUUID(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ParseUUID(%q) err = %v, want %v", tt.in, err, tt.wantErr)
				}
				if got.Valid {
					t.Fatalf("ParseUUID(%q) = %+v, want invalid on error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseUUID(%q): %v", tt.in, err)
			}
			if got.Valid != tt.wantOKay {
				t.Fatalf("ParseUUID(%q).Valid = %v, want %v", tt.in, got.Valid, tt.wantOKay)
			}
			if s := UUIDString(got); s != tt.wantStr {
				t.Fatalf("UUIDString(ParseUUID(%q)) = %q, want %q", tt.in, s, tt.wantStr)
			}
		})
	}
}

func TestUUIDString_InvalidIsEmpty(t *testing.T) {
	t.Parallel()
	if s := UUIDString(pgtype.UUID{}); s != "" {
		t.Fatalf("UUIDString(invalid) = %q, want empty", s)
	}
}
