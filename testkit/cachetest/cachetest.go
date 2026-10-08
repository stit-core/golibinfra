// Package cachetest — общий контрактный набор для реализаций cachekit.Cache: Redis-кэш,
// cachekit.MemoryCache и кэш потребителя проверяются одними и теми же кейсами.
package cachetest

import (
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
)

// ttl — срок жизни ключа в кейсах истечения: короче — флак на медленном Redis, длиннее —
// медленный набор. Ключ, который кейсу нужен живым, пишется на минуту.
const ttl = 200 * time.Millisecond

// RunContract прогоняет контракт Cache. newCache вызывается на каждый кейс и должен отдавать
// изолированный кэш: свой инстанс или свой префикс ключей.
func RunContract(t *testing.T, newCache func(t *testing.T) cachekit.Cache) {
	t.Helper()

	cases := []struct {
		name string
		run  func(t *testing.T, c cachekit.Cache)
	}{
		{"GetSetOverwrite", getSetOverwrite},
		{"ValuesAreCopies", valuesAreCopies},
		{"TTLExpires", ttlExpires},
		{"SetNX", setNX},
		{"GetDel", getDel},
		{"CompareAndDelete", compareAndDelete},
		{"CompareAndSet", compareAndSet},
		{"Delete", deleteKeys},
		{"InvalidateByTag", invalidateByTag},
		{"TagMembershipOutlivesKey", tagMembershipOutlivesKey},
		{"TagSetOutlivesShorterMember", tagSetOutlivesShorterMember},
		{"TagSetExpiresWithMembers", tagSetExpiresWithMembers},
		{"PingAndClose", pingAndClose},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, newCache(t))
		})
	}
}

func set(t *testing.T, c cachekit.Cache, key, value string, ttl time.Duration, tags ...string) {
	t.Helper()
	if err := c.SetWithTags(t.Context(), key, []byte(value), ttl, tags...); err != nil {
		t.Fatalf("SetWithTags(%q, %v): %v", key, tags, err)
	}
}

// want проверяет значение ключа; пустой value — ключа нет.
func want(t *testing.T, c cachekit.Cache, key, value string) {
	t.Helper()
	got, found, err := c.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if found != (value != "") || string(got) != value {
		t.Fatalf("Get(%q) = %q, found %v; want %q", key, got, found, value)
	}
}

func invalidate(t *testing.T, c cachekit.Cache, tag string) {
	t.Helper()
	if err := c.InvalidateByTag(t.Context(), tag); err != nil {
		t.Fatalf("InvalidateByTag(%q): %v", tag, err)
	}
}

// Промах — found == false без ошибки; Set пишет и перезаписывает, пустое значение — тоже значение.
func getSetOverwrite(t *testing.T, c cachekit.Cache) {
	ctx := t.Context()
	want(t, c, "k", "")
	if err := c.Set(ctx, "k", []byte("v1"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	want(t, c, "k", "v1")
	if err := c.Set(ctx, "k", []byte("v2"), 0); err != nil {
		t.Fatalf("Set over a live key: %v", err)
	}
	want(t, c, "k", "v2")

	if err := c.Set(ctx, "empty", nil, time.Minute); err != nil {
		t.Fatalf("Set of an empty value: %v", err)
	}
	if v, found, err := c.Get(ctx, "empty"); err != nil || !found || len(v) != 0 {
		t.Fatalf("Get of an empty value = %q, %v, %v; want found", v, found, err)
	}
}

// Кэш хранит и отдаёт копии: правка байт у вызывающего значение не меняет.
func valuesAreCopies(t *testing.T, c cachekit.Cache) {
	ctx := t.Context()
	src := []byte("abc")
	if err := c.Set(ctx, "k", src, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	src[0] = 'z'
	got, _, err := c.Get(ctx, "k")
	if err != nil || string(got) != "abc" {
		t.Fatalf("Get = %q, %v; want the value as it was written", got, err)
	}
	got[0] = 'z'
	want(t, c, "k", "abc")
}

// Ключ живёт свой ttl; ttl 0 — без срока, и перезапись без срока снимает прежний.
func ttlExpires(t *testing.T, c cachekit.Cache) {
	ctx := t.Context()
	for key, d := range map[string]time.Duration{"short": ttl, "forever": 0, "rewritten": ttl} {
		if err := c.Set(ctx, key, []byte("v"), d); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	if err := c.Set(ctx, "rewritten", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(2 * ttl)
	want(t, c, "short", "")
	want(t, c, "forever", "v")
	want(t, c, "rewritten", "v")
}

// SetNX пишет только отсутствующий ключ; истёкший — отсутствует.
func setNX(t *testing.T, c cachekit.Cache) {
	ctx := t.Context()
	nx := func(key, value string, d time.Duration) bool {
		t.Helper()
		ok, err := c.SetNX(ctx, key, []byte(value), d)
		if err != nil {
			t.Fatalf("SetNX(%q): %v", key, err)
		}
		return ok
	}
	if !nx("lock", "first", time.Minute) {
		t.Fatal("SetNX of a missing key did not write it")
	}
	if nx("lock", "second", time.Minute) {
		t.Fatal("SetNX of a live key wrote over it")
	}
	want(t, c, "lock", "first")

	if !nx("expiring", "first", ttl) {
		t.Fatal("SetNX of a missing key did not write it")
	}
	time.Sleep(2 * ttl)
	if !nx("expiring", "second", time.Minute) {
		t.Fatal("SetNX of an expired key did not write it")
	}
	want(t, c, "expiring", "second")
}

// GetDel отдаёт значение один раз.
func getDel(t *testing.T, c cachekit.Cache) {
	ctx := t.Context()
	set(t, c, "k", "v", time.Minute)
	if v, found, err := c.GetDel(ctx, "k"); err != nil || !found || string(v) != "v" {
		t.Fatalf("GetDel = %q, %v, %v; want the value", v, found, err)
	}
	if v, found, err := c.GetDel(ctx, "k"); err != nil || found {
		t.Fatalf("second GetDel = %q, %v, %v; want a miss", v, found, err)
	}
	want(t, c, "k", "")
}

// CompareAndDelete снимает только своё значение.
func compareAndDelete(t *testing.T, c cachekit.Cache) {
	ctx := t.Context()
	set(t, c, "k", "mine", time.Minute)
	if ok, err := c.CompareAndDelete(ctx, "k", []byte("theirs")); ok || err != nil {
		t.Fatalf("CompareAndDelete of a foreign value = %v, %v; want false", ok, err)
	}
	want(t, c, "k", "mine")
	if ok, err := c.CompareAndDelete(ctx, "k", []byte("mine")); !ok || err != nil {
		t.Fatalf("CompareAndDelete of the own value = %v, %v; want true", ok, err)
	}
	want(t, c, "k", "")
	if ok, err := c.CompareAndDelete(ctx, "k", []byte("mine")); ok || err != nil {
		t.Fatalf("CompareAndDelete of a missing key = %v, %v; want false", ok, err)
	}
}

// CompareAndSet пишет только поверх своего значения, с новым ttl; ключа не создаёт.
func compareAndSet(t *testing.T, c cachekit.Cache) {
	ctx := t.Context()
	set(t, c, "k", "mine", time.Minute)
	if ok, err := c.CompareAndSet(ctx, "k", []byte("theirs"), []byte("v"), time.Minute); ok || err != nil {
		t.Fatalf("CompareAndSet over a foreign value = %v, %v; want false", ok, err)
	}
	want(t, c, "k", "mine")
	if ok, err := c.CompareAndSet(ctx, "missing", []byte("mine"), []byte("v"), time.Minute); ok || err != nil {
		t.Fatalf("CompareAndSet of a missing key = %v, %v; want false", ok, err)
	}
	want(t, c, "missing", "")
	if ok, err := c.CompareAndSet(ctx, "k", []byte("mine"), []byte("v"), ttl); !ok || err != nil {
		t.Fatalf("CompareAndSet over the own value = %v, %v; want true", ok, err)
	}
	want(t, c, "k", "v")
	time.Sleep(2 * ttl)
	want(t, c, "k", "")
}

// Delete снимает несколько ключей разом; отсутствующий ключ и пустой список — не ошибка.
func deleteKeys(t *testing.T, c cachekit.Cache) {
	ctx := t.Context()
	set(t, c, "a", "1", time.Minute)
	set(t, c, "b", "2", time.Minute)
	set(t, c, "c", "3", time.Minute)
	if err := c.Delete(ctx, "a", "b", "missing"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := c.Delete(ctx); err != nil {
		t.Fatalf("Delete without keys: %v", err)
	}
	want(t, c, "a", "")
	want(t, c, "b", "")
	want(t, c, "c", "3")
}

// InvalidateByTag сносит членов тега — и тех, что состоят в других тегах; ключи без тега и
// SetWithTags без тегов не трогает, отсутствующий тег — не ошибка.
func invalidateByTag(t *testing.T, c cachekit.Cache) {
	set(t, c, "a", "1", time.Minute, "t1", "t2")
	set(t, c, "b", "2", 0, "t2")
	set(t, c, "c", "3", time.Minute, "t1")
	set(t, c, "plain", "4", time.Minute)
	invalidate(t, c, "t2")
	invalidate(t, c, "missing")
	want(t, c, "a", "")
	want(t, c, "b", "")
	want(t, c, "c", "3")
	want(t, c, "plain", "4")

	invalidate(t, c, "t1")
	want(t, c, "c", "")
	set(t, c, "c", "5", time.Minute)
	invalidate(t, c, "t1")
	want(t, c, "c", "5")
}

// Членство в теге переживает ключ: удалённый или перезаписанный без тега ключ InvalidateByTag
// сносит — набор тега помнит имя, а не запись.
func tagMembershipOutlivesKey(t *testing.T, c cachekit.Cache) {
	ctx := t.Context()
	set(t, c, "deleted", "v1", time.Minute, "t")
	set(t, c, "overwritten", "v1", time.Minute, "t")
	if err := c.Delete(ctx, "deleted"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	set(t, c, "deleted", "v2", time.Minute)
	set(t, c, "overwritten", "v2", time.Minute)
	invalidate(t, c, "t")
	want(t, c, "deleted", "")
	want(t, c, "overwritten", "")
}

// Набор тега живёт не меньше самого долгоживущего члена: запись с меньшим ttl его не укорачивает,
// и членство истёкшего ключа остаётся.
func tagSetOutlivesShorterMember(t *testing.T, c cachekit.Cache) {
	set(t, c, "long", "v", time.Minute, "t")
	set(t, c, "short", "v", ttl, "t")
	time.Sleep(2 * ttl)
	want(t, c, "short", "")
	set(t, c, "short", "again", 0)
	invalidate(t, c, "t")
	want(t, c, "short", "")
	want(t, c, "long", "")
}

// Набор тега истекает вместе с последним членом: членства после этого нет.
func tagSetExpiresWithMembers(t *testing.T, c cachekit.Cache) {
	set(t, c, "k", "v", ttl, "t")
	time.Sleep(2 * ttl)
	set(t, c, "k", "again", 0)
	invalidate(t, c, "t")
	want(t, c, "k", "again")
}

// Ping живого кэша успешен, Close идемпотентен.
func pingAndClose(t *testing.T, c cachekit.Cache) {
	if err := c.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
