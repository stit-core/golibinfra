package pgkit

import (
	"testing"
	"time"
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
