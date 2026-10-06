// Package idempotencytest — общий контрактный набор для реализаций httpkit.IdempotencyStore:
// httpkit.MemoryStore, cachekit.IdempotencyStore на памяти и на Redis и store потребителя
// проверяются одними и теми же кейсами.
package idempotencytest

import (
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// ttl — срок жизни резерва и записи в кейсах истечения: короче — флак на медленном Redis,
// длиннее — медленный набор.
const ttl = 200 * time.Millisecond

// RunContract прогоняет контракт IdempotencyStore. newStore вызывается на каждый кейс и должен
// отдавать изолированный store.
func RunContract(t *testing.T, newStore func(t *testing.T) httpkit.IdempotencyStore) {
	t.Helper()

	cases := []struct {
		name string
		run  func(t *testing.T, s httpkit.IdempotencyStore)
	}{
		{"ReserveIsExclusive", reserveIsExclusive},
		{"GetHidesReservation", getHidesReservation},
		{"SetByOwnerStoresRecord", setByOwnerStoresRecord},
		{"SetByForeignTokenIsRejected", setByForeignTokenIsRejected},
		{"SetAfterReservationExpiredIsRejected", setAfterReservationExpiredIsRejected},
		{"ReleaseOnlyByOwner", releaseOnlyByOwner},
		{"ReleaseKeepsRecord", releaseKeepsRecord},
		{"RecordExpires", recordExpires},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, newStore(t))
		})
	}
}

func reserve(t *testing.T, s httpkit.IdempotencyStore, key string, ttl time.Duration) string {
	t.Helper()
	token, ok, err := s.Reserve(t.Context(), key, ttl)
	if err != nil || !ok || token == "" {
		t.Fatalf("Reserve(%q) = %q, %v, %v; want a token", key, token, ok, err)
	}
	return token
}

func reserveTaken(t *testing.T, s httpkit.IdempotencyStore, key string) bool {
	t.Helper()
	_, ok, err := s.Reserve(t.Context(), key, time.Minute)
	if err != nil {
		t.Fatalf("Reserve(%q): %v", key, err)
	}
	return !ok
}

func get(t *testing.T, s httpkit.IdempotencyStore, key string) ([]byte, bool) {
	t.Helper()
	rec, found, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return rec, found
}

func set(t *testing.T, s httpkit.IdempotencyStore, key, token, record string, ttl time.Duration) bool {
	t.Helper()
	stored, err := s.Set(t.Context(), key, token, []byte(record), ttl)
	if err != nil {
		t.Fatalf("Set(%q): %v", key, err)
	}
	return stored
}

// Резерв занимает ключ одному запросу; у каждого резерва свой токен, ключи независимы.
func reserveIsExclusive(t *testing.T, s httpkit.IdempotencyStore) {
	first := reserve(t, s, "a", time.Minute)
	if !reserveTaken(t, s, "a") {
		t.Fatal("second Reserve of a held key succeeded")
	}
	if other := reserve(t, s, "b", time.Minute); other == first {
		t.Fatalf("two reservations share the token %q", first)
	}
}

// Резерв — не запись: Get его не отдаёт.
func getHidesReservation(t *testing.T, s httpkit.IdempotencyStore) {
	reserve(t, s, "k", time.Minute)
	if rec, found := get(t, s, "k"); found {
		t.Fatalf("Get of a reserved key = %q, want not found", rec)
	}
}

// Владелец резерва кладёт запись; она занимает ключ, как резерв.
func setByOwnerStoresRecord(t *testing.T, s httpkit.IdempotencyStore) {
	token := reserve(t, s, "k", time.Minute)
	if !set(t, s, "k", token, `{"status":201}`, time.Hour) {
		t.Fatal("Set by the reservation owner was not stored")
	}
	if rec, found := get(t, s, "k"); !found || string(rec) != `{"status":201}` {
		t.Fatalf("Get = %q, %v; want the stored record", rec, found)
	}
	if !reserveTaken(t, s, "k") {
		t.Fatal("Reserve over a stored record succeeded")
	}
}

// Запрос, переживший свой резерв, не затирает резерв следующего (E07).
func setByForeignTokenIsRejected(t *testing.T, s httpkit.IdempotencyStore) {
	stale := reserve(t, s, "k", ttl)
	time.Sleep(2 * ttl)
	current := reserve(t, s, "k", time.Minute)
	if set(t, s, "k", stale, `{"from":"stale"}`, time.Hour) {
		t.Fatal("Set with an expired token overwrote the next reservation")
	}
	if rec, found := get(t, s, "k"); found {
		t.Fatalf("Get = %q while the next request is in flight, want not found", rec)
	}
	if !set(t, s, "k", current, `{"from":"current"}`, time.Hour) {
		t.Fatal("Set by the current owner was not stored")
	}
}

// Резерв истёк и ключ свободен — запись без резерва не пишется.
func setAfterReservationExpiredIsRejected(t *testing.T, s httpkit.IdempotencyStore) {
	token := reserve(t, s, "k", ttl)
	time.Sleep(2 * ttl)
	if set(t, s, "k", token, `{"late":true}`, time.Hour) {
		t.Fatal("Set after the reservation expired was stored")
	}
	if _, found := get(t, s, "k"); found {
		t.Fatal("a late Set left a record")
	}
}

// Резерв снимает только владелец токена.
func releaseOnlyByOwner(t *testing.T, s httpkit.IdempotencyStore) {
	token := reserve(t, s, "k", time.Minute)
	if err := s.Release(t.Context(), "k", "someone-else"); err != nil {
		t.Fatalf("Release with a foreign token: %v", err)
	}
	if !reserveTaken(t, s, "k") {
		t.Fatal("Release with a foreign token freed the key")
	}
	if err := s.Release(t.Context(), "k", token); err != nil {
		t.Fatalf("Release by the owner: %v", err)
	}
	reserve(t, s, "k", time.Minute)
}

// Release опоздавшего запроса не трогает сохранённую запись.
func releaseKeepsRecord(t *testing.T, s httpkit.IdempotencyStore) {
	token := reserve(t, s, "k", time.Minute)
	if !set(t, s, "k", token, `{"status":201}`, time.Hour) {
		t.Fatal("Set by the owner was not stored")
	}
	if err := s.Release(t.Context(), "k", token); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, found := get(t, s, "k"); !found {
		t.Fatal("Release deleted a completed record")
	}
}

// Запись живёт свой ttl, потом ключ свободен.
func recordExpires(t *testing.T, s httpkit.IdempotencyStore) {
	token := reserve(t, s, "k", time.Minute)
	if !set(t, s, "k", token, `{"status":201}`, ttl) {
		t.Fatal("Set by the owner was not stored")
	}
	time.Sleep(2 * ttl)
	if _, found := get(t, s, "k"); found {
		t.Fatal("record outlived its ttl")
	}
	reserve(t, s, "k", time.Minute)
}
