package authnkit

import (
	"context"
	"encoding/base64"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func defaultHasher(t *testing.T) *Argon2idHasher {
	t.Helper()
	return newHasherOrFail(t, Argon2idConfig{
		Time:    1,
		Memory:  8 * 1024,
		Threads: 1,
		KeyLen:  32,
		SaltLen: 16,
	})
}

func newHasherOrFail(t *testing.T, p Argon2idConfig) *Argon2idHasher {
	t.Helper()
	h, err := NewArgon2idHasher(p)
	if err != nil {
		t.Fatalf("NewArgon2idHasher: %v", err)
	}
	return h
}

func TestHashVerifyRoundtrip(t *testing.T) {
	ctx := t.Context()
	h := defaultHasher(t)

	encoded, err := h.Hash(ctx, "abc")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	ok, err := h.Verify(ctx, "abc", encoded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatal("expected Verify to return true")
	}
	ok, err = h.Verify(ctx, "abd", encoded)
	if err != nil || ok {
		t.Fatalf("wrong password: ok=%v err=%v", ok, err)
	}
}

func TestHashSaltIsRandom(t *testing.T) {
	ctx := t.Context()
	h := defaultHasher(t)

	e1, err := h.Hash(ctx, "abc")
	if err != nil {
		t.Fatalf("Hash 1: %v", err)
	}
	e2, err := h.Hash(ctx, "abc")
	if err != nil {
		t.Fatalf("Hash 2: %v", err)
	}
	if e1 == e2 {
		t.Fatal("expected different encoded strings due to random salt")
	}
}

// Невалидные параметры валят конструктор, а не первый Hash.
func TestNewArgon2idHasher_InvalidParams(t *testing.T) {
	cases := map[string]Argon2idConfig{
		"zero":           {},
		"t=0":            {Time: 0, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		"t above window": {Time: MaxTime + 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		"p=0":            {Time: 1, Memory: 8 * 1024, Threads: 0, KeyLen: 32, SaltLen: 16},
		"p above window": {Time: 1, Memory: 8 * 1024, Threads: MaxThreads + 1, KeyLen: 32, SaltLen: 16},
		"m too small":    {Time: 1, Memory: 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		"m too large":    {Time: 1, Memory: 2 * 1024 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		// t и m по отдельности в окне, произведение — нет.
		"cost above window": {Time: 16, Memory: 64 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		"short key":         {Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 8, SaltLen: 16},
		"short salt":        {Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 4},
		"too long key":      {Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 256, SaltLen: 16},
		"too long salt":     {Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: MaxSaltLen + 1},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			h, err := NewArgon2idHasher(p)
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("expected ErrConfig, got %v", err)
			}
			if h != nil {
				t.Fatal("expected nil hasher")
			}
			if err := p.Validate(); !errors.Is(err, ErrConfig) {
				t.Fatalf("Validate: expected ErrConfig, got %v", err)
			}
		})
	}
}

func phcString(params string, saltLen, hashLen int) string {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, saltLen))
	hash := base64.RawStdEncoding.EncodeToString(make([]byte, hashLen))
	return "$argon2id$v=19$" + params + "$" + salt + "$" + hash
}

// Параметры PHC-строки приходят из хранилища: вне окна → ErrInvalidHash, не
// паника в argon2 и не аллокация терабайтов.
func TestVerify_RejectsHostileParams(t *testing.T) {
	ctx := t.Context()
	h := defaultHasher(t)
	cases := map[string]string{
		"t=0":                   phcString("m=8192,t=0,p=1", 16, 32),
		"t above window":        phcString("m=8192,t=65,p=1", 16, 32),
		"huge t":                phcString("m=8192,t=4294967295,p=1", 16, 32),
		"t overflow":            phcString("m=8192,t=99999999999,p=1", 16, 32),
		"p=0":                   phcString("m=8192,t=1,p=0", 16, 32),
		"p above window":        phcString("m=8192,t=1,p=17", 16, 32),
		"empty hash":            phcString("m=8192,t=1,p=1", 16, 0),
		"short hash":            phcString("m=8192,t=1,p=1", 16, 8),
		"huge hash":             phcString("m=8192,t=1,p=1", 16, 129),
		"huge m":                phcString("m=4294967295,t=1,p=1", 16, 32),
		"m overflow":            phcString("m=99999999999,t=1,p=1", 16, 32),
		"m below window":        phcString("m=64,t=1,p=1", 16, 32),
		"cost above window":     phcString("m=1048576,t=64,p=4", 16, 32),
		"negative t":            phcString("m=8192,t=-1,p=1", 16, 32),
		"p overflow":            phcString("m=8192,t=1,p=300", 16, 32),
		"short salt":            phcString("m=8192,t=1,p=1", 4, 32),
		"long salt":             phcString("m=8192,t=1,p=1", 65, 32),
		"wrong version":         strings.Replace(phcString("m=8192,t=1,p=1", 16, 32), "v=19", "v=16", 1),
		"bad version field":     strings.Replace(phcString("m=8192,t=1,p=1", 16, 32), "v=19", "x=19", 1),
		"non-numeric version":   strings.Replace(phcString("m=8192,t=1,p=1", 16, 32), "v=19", "v=abc", 1),
		"missing param":         phcString("m=8192,t=1", 16, 32),
		"reordered params":      phcString("t=1,m=8192,p=1", 16, 32),
		"leading zero in m":     phcString("m=008192,t=1,p=1", 16, 32),
		"leading zero in t":     phcString("m=8192,t=01,p=1", 16, 32),
		"plus sign in p":        phcString("m=8192,t=1,p=+1", 16, 32),
		"trailing garbage in p": phcString("m=8192,t=1,p=1x", 16, 32),
		"not argon2id":          strings.Replace(phcString("m=8192,t=1,p=1", 16, 32), "argon2id", "argon2i", 1),
		"bad base64":            "$argon2id$v=19$m=8192,t=1,p=1$***$***",
		"padded salt":           "$argon2id$v=19$m=8192,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA==$" + base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
		"non-canonical salt":    nonCanonicalTail(phcString("m=8192,t=1,p=1", 16, 32), 4),
		"non-canonical hash":    nonCanonicalTail(phcString("m=8192,t=1,p=1", 16, 32), 5),
		"newline in salt":       "$argon2id$v=19$m=8192,t=1,p=1$AAAAAAAAAAAA\nAAAAAAAAAA$" + base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
		"newline in hash":       "$argon2id$v=19$m=8192,t=1,p=1$" + base64.RawStdEncoding.EncodeToString(make([]byte, 16)) + "$AAAA\r\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"leading space":         " " + phcString("m=8192,t=1,p=1", 16, 32),
		"bad hash base64":       "$argon2id$v=19$m=8192,t=1,p=1$" + base64.RawStdEncoding.EncodeToString(make([]byte, 16)) + "$!!!!",
		"garbage":               "notahash",
		"empty":                 "",
		"leading garbage":       "x" + phcString("m=8192,t=1,p=1", 16, 32),
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := h.Verify(ctx, "abc", encoded)
			if !errors.Is(err, ErrInvalidHash) {
				t.Fatalf("expected ErrInvalidHash, got %v", err)
			}
			if ok {
				t.Fatal("expected false")
			}
		})
	}
}

// Параметры вне окна отличимы от «строка не разбирается», но остаются
// ErrInvalidHash для потребителя, который различий не делает.
func TestVerify_ErrHashParams(t *testing.T) {
	ctx := t.Context()
	h := defaultHasher(t)

	_, err := h.Verify(ctx, "abc", phcString("m=8192,t=65,p=1", 16, 32))
	if !errors.Is(err, ErrHashParams) || !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("out-of-window params: %v", err)
	}
	_, err = h.Verify(ctx, "abc", "notahash")
	if errors.Is(err, ErrHashParams) {
		t.Fatalf("unparsable string must not be ErrHashParams: %v", err)
	}
	if !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("unparsable string: %v", err)
	}
	if !h.NeedsRehash(phcString("m=8192,t=65,p=1", 16, 32)) {
		t.Fatal("out-of-window hash must need rehash")
	}
}

// Окно параметров проверяется до argon2.IDKey: строка с t=2^31 отвергается
// мгновенно, а не занимает ядро на часы (CPU-DoS из хранилища).
func TestVerify_HugeTimeCostsNothing(t *testing.T) {
	h := defaultHasher(t)
	encoded := phcString("m=1048576,t=2147483648,p=1", 16, 32)

	start := time.Now()
	ok, err := h.Verify(t.Context(), "abc", encoded)
	elapsed := time.Since(start)

	if ok || !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Verify took %s: parameters were used before the window check", elapsed)
	}
}

// Потолок стоимости: параметры, каждый из которых в окне, вместе стоят на
// порядки больше дефолтных — такую строку Verify отвергает до вычисления.
func TestVerify_CostAboveWindowCostsNothing(t *testing.T) {
	h := defaultHasher(t)
	encoded := phcString("m=1048576,t=64,p=4", 16, 32)

	start := time.Now()
	ok, err := h.Verify(t.Context(), "abc", encoded)
	elapsed := time.Since(start)

	if ok || !errors.Is(err, ErrHashParams) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	// Тот же вход, посчитанный честно, занимает ~10 с и гигабайт RSS.
	if elapsed > 2*time.Second {
		t.Fatalf("Verify занял %s: стоимость проверена после вычисления", elapsed)
	}
	if !h.NeedsRehash(encoded) {
		t.Fatal("строка вне потолка стоимости должна требовать перехэширования")
	}
}

func TestArgon2idConfig_CostCeiling(t *testing.T) {
	// m*t ровно на потолке — валидно, шаг выше — ErrConfig.
	atCeiling := Argon2idConfig{Time: MaxTime, Memory: MaxCostKiBPasses / MaxTime, Threads: 1, KeyLen: 32, SaltLen: 16}
	if err := atCeiling.Validate(); err != nil {
		t.Fatalf("стоимость на потолке отвергнута: %v", err)
	}
	above := atCeiling
	above.Memory += 1024
	if err := above.Validate(); !errors.Is(err, ErrConfig) {
		t.Fatalf("стоимость выше потолка: ожидали ErrConfig, получили %v", err)
	}
	if err := (Argon2idConfig{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16}).Validate(); err != nil {
		t.Fatalf("дефолтные параметры кита вне окна: %v", err)
	}
}

// nonCanonicalTail портит хвостовые биты последнего символа base64-поля field
// PHC-строки: строгий декодер обязан отвергнуть такую форму.
func nonCanonicalTail(encoded string, field int) string {
	parts := strings.Split(encoded, "$")
	parts[field] = parts[field][:len(parts[field])-1] + "B"
	return strings.Join(parts, "$")
}

// base64 в PHC строгий: у одного хэша ровно одно представление, иначе
// неканоническая форма осела бы в БД навсегда — NeedsRehash её не заметил бы.
func TestVerify_NonCanonicalBase64(t *testing.T) {
	ctx := t.Context()
	h := defaultHasher(t)
	canonical := phcString("m=8192,t=1,p=1", 16, 32)

	if ok, err := h.Verify(ctx, "abc", canonical); ok || err != nil {
		t.Fatalf("каноническая строка: ok=%v err=%v", ok, err)
	}
	for _, field := range []int{4, 5} {
		spoiled := nonCanonicalTail(canonical, field)
		if spoiled == canonical {
			t.Fatalf("поле %d не изменилось", field)
		}
		ok, err := h.Verify(ctx, "abc", spoiled)
		if ok || !errors.Is(err, ErrInvalidHash) {
			t.Fatalf("поле %d: ok=%v err=%v", field, ok, err)
		}
		if !h.NeedsRehash(spoiled) {
			t.Fatalf("поле %d: неканоническая строка не требует перехэширования", field)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	ctx := t.Context()
	base := Argon2idConfig{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
	h := newHasherOrFail(t, base)
	encoded, err := h.Hash(ctx, "abc")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if h.NeedsRehash(encoded) {
		t.Fatal("own hash must not need rehash")
	}

	// Изменение любого из пяти параметров — сигнал перехэшировать.
	changed := map[string]Argon2idConfig{
		"time":    {Time: 2, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		"memory":  {Time: 1, Memory: 16 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		"threads": {Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16},
		"keylen":  {Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 64, SaltLen: 16},
		"saltlen": {Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 32},
	}
	for name, p := range changed {
		t.Run(name, func(t *testing.T) {
			other := newHasherOrFail(t, p)
			if !other.NeedsRehash(encoded) {
				t.Fatalf("changed %s must need rehash", name)
			}
			// Старый хэш при этом проверяется на своих параметрах.
			ok, err := other.Verify(ctx, "abc", encoded)
			if err != nil || !ok {
				t.Fatalf("Verify with older params: ok=%v err=%v", ok, err)
			}
		})
	}

	if !h.NeedsRehash("garbage") {
		t.Fatal("unreadable hash must need rehash")
	}
}

// VerifyDummy — путь «пользователь не найден»: то же время, что Verify.
func TestVerifyDummy(t *testing.T) {
	ctx := t.Context()
	for _, saltLen := range []uint32{MinSaltLen, 16, MaxSaltLen} {
		h := newHasherOrFail(t, Argon2idConfig{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: saltLen})
		encoded, err := h.Hash(ctx, "abc")
		if err != nil {
			t.Fatalf("Hash: %v", err)
		}

		start := time.Now()
		if _, err := h.Verify(ctx, "abc", encoded); err != nil {
			t.Fatalf("Verify: %v", err)
		}
		verify := time.Since(start)

		start = time.Now()
		if err := h.VerifyDummy(ctx); err != nil {
			t.Fatalf("VerifyDummy: %v", err)
		}
		dummy := time.Since(start)

		// Запас на шум загруженной машины: no-op отличается на три порядка.
		if dummy < verify/10 {
			t.Fatalf("salt %d: VerifyDummy %s is much cheaper than Verify %s", saltLen, dummy, verify)
		}
	}
}

// Пароль длиннее MaxPasswordLen в Argon2id не попадает: Hash отказывает без вычисления, Verify —
// после холостого вычисления над входом VerifyDummy, иначе длина выдала бы учётную запись таймингом.
func TestPasswordLengthLimit(t *testing.T) {
	ctx := t.Context()
	h := defaultHasher(t)
	atLimit := strings.Repeat("p", MaxPasswordLen)
	encoded, err := h.Hash(ctx, atLimit)
	if err != nil {
		t.Fatalf("Hash of %d bytes: %v", MaxPasswordLen, err)
	}
	if ok, err := h.Verify(ctx, atLimit, encoded); err != nil || !ok {
		t.Fatalf("Verify of %d bytes: ok=%v err=%v", MaxPasswordLen, ok, err)
	}

	var derived [][]byte
	h.derive = func(password, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		derived = append(derived, password)
		return make([]byte, keyLen)
	}
	tooLong := atLimit + "p"
	if _, err := h.Hash(ctx, tooLong); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("Hash of %d bytes = %v, want ErrPasswordTooLong", len(tooLong), err)
	}
	if len(derived) != 0 {
		t.Fatalf("Hash of a too long password ran argon2id %d times", len(derived))
	}
	if ok, err := h.Verify(ctx, tooLong, encoded); ok || !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("Verify of %d bytes: ok=%v err=%v, want ErrPasswordTooLong", len(tooLong), ok, err)
	}
	if len(derived) != 1 || string(derived[0]) != string(dummyPassword) {
		t.Fatalf("Verify of a too long password: argon2id inputs %q, want one dummy computation", derived)
	}
}

// Предел — в байтах, а не в символах: пароль не из ASCII короче MaxPasswordLen символов его превышает.
// maxLength у поля DTO считает символы и такой пароль до Hash пропускает.
func TestPasswordLengthLimit_CountsBytes(t *testing.T) {
	ctx := t.Context()
	h := defaultHasher(t)
	fits := strings.Repeat("п", MaxPasswordLen/2)
	encoded, err := h.Hash(ctx, fits)
	if err != nil {
		t.Fatalf("Hash of %d runes in %d bytes: %v", utf8.RuneCountInString(fits), len(fits), err)
	}

	tooLong := fits + "п"
	if runes := utf8.RuneCountInString(tooLong); runes >= MaxPasswordLen || len(tooLong) <= MaxPasswordLen {
		t.Fatalf("fixture: %d runes in %d bytes, want fewer runes and more bytes than %d", runes, len(tooLong), MaxPasswordLen)
	}
	if _, err := h.Hash(ctx, tooLong); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("Hash of %d runes in %d bytes = %v, want ErrPasswordTooLong", utf8.RuneCountInString(tooLong), len(tooLong), err)
	}
	if ok, err := h.Verify(ctx, tooLong, encoded); ok || !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("Verify of %d runes in %d bytes: ok=%v err=%v, want ErrPasswordTooLong", utf8.RuneCountInString(tooLong), len(tooLong), ok, err)
	}
}

var _ Hasher = (*Argon2idHasher)(nil)

// Регрессия SEC-09: каждый Argon2id держит Memory КиБ, а логин анонимен —
// одновременно считается не больше max(1, GOMAXPROCS/Threads) хэшей, лишние
// ждут слот (не больше queuePerSlot на слот), и ожидание прерывает ctx.
func TestArgon2id_Concurrency(t *testing.T) {
	for _, threads := range []uint8{1, 2, MaxThreads} {
		h := newHasherOrFail(t, Argon2idConfig{Time: 1, Memory: MinMemoryKiB, Threads: threads, KeyLen: 32, SaltLen: 16})
		if want := max(1, runtime.GOMAXPROCS(0)/int(threads)); cap(h.slots) != want || cap(h.queue) != want*(1+queuePerSlot) {
			t.Fatalf("threads=%d: slots = %d, queue = %d; want max(1, GOMAXPROCS/threads) = %d and %d more waiting per slot",
				threads, cap(h.slots), cap(h.queue), want, queuePerSlot)
		}
	}

	h := newHasherOrFail(t, Argon2idConfig{Time: 1, Memory: MinMemoryKiB, Threads: 2, KeyLen: 32, SaltLen: 16})
	encoded, err := h.Hash(t.Context(), "p")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	limit := cap(h.slots)

	var inFlight, peak atomic.Int64
	release := make(chan struct{})
	h.derive = func(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		n := inFlight.Add(1)
		for {
			if p := peak.Load(); n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return make([]byte, keyLen)
	}

	var wg sync.WaitGroup
	for range 3 * limit {
		wg.Go(func() {
			if _, err := h.Hash(context.Background(), "p"); err != nil {
				t.Errorf("queued Hash: %v", err)
			}
		})
	}
	for deadline := time.Now().Add(5 * time.Second); inFlight.Load() < int64(limit); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("in flight = %d, want %d", inFlight.Load(), limit)
		}
	}
	// Слоты заняты: лишние вызовы ждут, а отмена ctx прерывает ожидание.
	time.Sleep(50 * time.Millisecond)
	if got := peak.Load(); got > int64(limit) {
		close(release)
		t.Fatalf("peak concurrent argon2id = %d, want %d", got, limit)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	waitCanceled := func(name string, call func() error) {
		t.Helper()
		errc := make(chan error, 1)
		go func() { errc <- call() }()
		select {
		case err := <-errc:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s while slots are busy = %v, want DeadlineExceeded", name, err)
			}
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatalf("%s ignores ctx while waiting for a slot", name)
		}
	}
	waitCanceled("Hash", func() error { _, err := h.Hash(ctx, "p"); return err })
	waitCanceled("Verify", func() error { _, err := h.Verify(ctx, "p", encoded); return err })
	waitCanceled("VerifyDummy", func() error { return h.VerifyDummy(ctx) })

	close(release)
	wg.Wait()
	if got := peak.Load(); got != int64(limit) {
		t.Fatalf("peak concurrent argon2id = %d, want %d", got, limit)
	}
}

// Регрессия RSECT-01: поток проверок пароля сильнее ёмкости хэшера не держит чужой вход до дедлайна запроса. При
// занятых слотах и полной очереди Hash, Verify и VerifyDummy отказывают сразу ErrBusy с подсказкой повтора — и Verify
// пароля длиннее предела: его холостое вычисление стоит в той же очереди, и отказ не отличает учётную запись. Отказ не
// пишет время вычисления, а принятые в очередь вызовы досчитываются.
func TestArgon2id_FullQueueFailsFast(t *testing.T) {
	h := newHasherOrFail(t, Argon2idConfig{Time: 1, Memory: MinMemoryKiB, Threads: 2, KeyLen: 32, SaltLen: 16})
	encoded, err := h.Hash(t.Context(), "p")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	limit, capacity := cap(h.slots), cap(h.queue)

	var inFlight atomic.Int64
	release := make(chan struct{})
	h.derive = func(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		inFlight.Add(1)
		<-release
		return make([]byte, keyLen)
	}
	var wg sync.WaitGroup
	for range capacity {
		wg.Go(func() {
			if _, err := h.Verify(context.Background(), "p", encoded); err != nil {
				t.Errorf("queued Verify: %v", err)
			}
		})
	}
	for deadline := time.Now().Add(5 * time.Second); len(h.queue) < capacity || inFlight.Load() < int64(limit); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("queue = %d of %d, in flight = %d of %d", len(h.queue), capacity, inFlight.Load(), limit)
		}
	}

	before := metrictest.Collect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tooLong := strings.Repeat("p", MaxPasswordLen+1)
	calls := []struct {
		name string
		call func() error
	}{
		{"Hash", func() error { _, err := h.Hash(ctx, "p"); return err }},
		{"Verify", func() error { _, err := h.Verify(ctx, "p", encoded); return err }},
		{"Verify of a too long password", func() error { _, err := h.Verify(ctx, tooLong, encoded); return err }},
		{"VerifyDummy", func() error { return h.VerifyDummy(ctx) }},
	}
	for _, c := range calls {
		start := time.Now()
		err := c.call()
		var retry interface{ RetryAfter() time.Duration }
		if !errors.Is(err, ErrBusy) || !errors.As(err, &retry) || retry.RetryAfter() <= 0 {
			t.Errorf("%s with a full queue = %v, want ErrBusy with a retry hint", c.name, err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("%s with a full queue took %s, want an immediate refusal", c.name, took)
		}
	}
	d := metrictest.Collect(t).Sub(before)
	if got := d.Value("authn_password_busy_total"); got != float64(len(calls)) {
		t.Errorf("authn_password_busy_total delta = %v, want %d", got, len(calls))
	}
	for _, name := range []string{"authn_password_hash_duration_seconds", "authn_password_verify_duration_seconds"} {
		if got := d.Find(name); len(got) != 0 {
			t.Errorf("%s recorded refused calls: %v", name, got)
		}
	}

	close(release)
	wg.Wait()
	computed := inFlight.Load()
	if _, err := h.Verify(ctx, "p", encoded); err != nil || inFlight.Load() != computed+1 {
		t.Fatalf("Verify after the queue drained = %v, want a computation", err)
	}
}
