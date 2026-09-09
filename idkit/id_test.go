package idkit_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"git.stit.tech/stit-core/golibinfra/idkit"
)

func TestNewV7_Format(t *testing.T) {
	id := idkit.NewV7()

	if len(id) != 36 {
		t.Fatalf("len(%q) = %d, want 36", id, len(id))
	}
	if id != strings.ToLower(id) {
		t.Fatalf("NewV7() = %q, want lowercase", id)
	}
	for _, i := range []int{8, 13, 18, 23} {
		if id[i] != '-' {
			t.Fatalf("NewV7() = %q, want '-' at %d", id, i)
		}
	}

	u, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("NewV7() = %q, not a valid uuid: %v", id, err)
	}
	if got := u.Version(); got != 7 {
		t.Fatalf("version = %d, want 7", got)
	}
	if got := u.Variant(); got != uuid.RFC4122 {
		t.Fatalf("variant = %v, want RFC4122", got)
	}
	if !idkit.IsV7(id) {
		t.Fatalf("IsV7(%q) = false, want true", id)
	}
}

func TestNewV7_Unique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := range n {
		id := idkit.NewV7()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q at i=%d", id, i)
		}
		seen[id] = struct{}{}
	}
}

// Пиннит гарантию google/uuid: (ms<<12 | seq) строго растёт на каждом вызове,
// поэтому id упорядочены даже внутри одной миллисекунды.
func TestNewV7_StrictlyIncreasingWithinProcess(t *testing.T) {
	const n = 20000
	ids := make([]string, n)
	for i := range ids {
		ids[i] = idkit.NewV7()
	}

	sameMillisecond := 0
	for i := 1; i < n; i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("ids[%d] = %q is not greater than %q", i, ids[i], ids[i-1])
		}
		if ids[i][:13] == ids[i-1][:13] {
			sameMillisecond++
		}
	}
	if sameMillisecond == 0 {
		t.Fatal("no adjacent pair within one millisecond: the sub-ms ordering is not exercised")
	}
}

func TestNewV7_ConcurrentUnique(t *testing.T) {
	const (
		goroutines = 8
		perRoutine = 2000
	)

	var (
		mu   sync.Mutex
		seen = make(map[string]struct{}, goroutines*perRoutine)
		wg   sync.WaitGroup
	)
	for range goroutines {
		wg.Go(func() {
			local := make([]string, perRoutine)
			for i := range local {
				local[i] = idkit.NewV7()
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				seen[id] = struct{}{}
			}
		})
	}
	wg.Wait()

	if len(seen) != goroutines*perRoutine {
		t.Fatalf("unique ids = %d, want %d", len(seen), goroutines*perRoutine)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		valid bool
	}{
		{"canonical v7", "0190e8a1-1c2d-7e3f-8a4b-5c6d7e8f9a0b", true},
		{"nil uuid", "00000000-0000-0000-0000-000000000000", true},
		{"uppercase", "0190E8A1-1C2D-7E3F-8A4B-5C6D7E8F9A0B", false},
		{"urn form", "urn:uuid:0190e8a1-1c2d-7e3f-8a4b-5c6d7e8f9a0b", false},
		{"braces form", "{0190e8a1-1c2d-7e3f-8a4b-5c6d7e8f9a0b}", false},
		{"32 hex without dashes", "0190e8a11c2d7e3f8a4b5c6d7e8f9a0b", false},
		{"empty", "", false},
		{"truncated", "0190e8a1-1c2d-7e3f-8a4b-5c6d7e8f9a0", false},
		{"trailing space", "0190e8a1-1c2d-7e3f-8a4b-5c6d7e8f9a0b ", false},
		{"non-hex character", "0190e8a1-1c2d-7e3f-8a4b-5c6d7e8f9a0z", false},
		{"dashes in wrong places", "0190e8a11-c2d-7e3f-8a4b-5c6d7e8f9a0b", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := idkit.Validate(tt.id)
			if tt.valid {
				if err != nil {
					t.Fatalf("Validate(%q) = %v, want nil", tt.id, err)
				}
				return
			}
			if !errors.Is(err, idkit.ErrInvalid) {
				t.Fatalf("Validate(%q) = %v, want ErrInvalid", tt.id, err)
			}
		})
	}
}

func TestIsV7(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"generated v7", idkit.NewV7(), true},
		{"v4 is not v7", uuid.NewString(), false},
		{"nil uuid", "00000000-0000-0000-0000-000000000000", false},
		{"uppercase v7", strings.ToUpper(idkit.NewV7()), false},
		{"garbage", "not-an-id", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := idkit.IsV7(tt.id); got != tt.want {
				t.Fatalf("IsV7(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
