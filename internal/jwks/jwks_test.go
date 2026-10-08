package jwks_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/internal/jwks"
)

func jwk(t *testing.T, kid string) jose.JSONWebKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return jose.JSONWebKey{Key: priv.Public(), KeyID: kid, Algorithm: string(jose.ES256), Use: "sig"}
}

// keyServer отдаёт текущий набор ключей и считает загрузки.
type keyServer struct {
	srv          *httptest.Server
	mu           sync.Mutex
	keys         []jose.JSONWebKey
	raw          string
	cacheControl string
	status       int
	hits         atomic.Int64
}

func newKeyServer(t *testing.T, keys ...jose.JSONWebKey) *keyServer {
	t.Helper()
	ks := &keyServer{keys: keys, status: http.StatusOK}
	ks.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ks.hits.Add(1)
		ks.mu.Lock()
		defer ks.mu.Unlock()
		if ks.cacheControl != "" {
			w.Header().Set("Cache-Control", ks.cacheControl)
		}
		if ks.status != http.StatusOK {
			w.WriteHeader(ks.status)
			return
		}
		if ks.raw != "" {
			_, _ = io.WriteString(w, ks.raw)
			return
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: ks.keys})
	}))
	t.Cleanup(ks.srv.Close)
	return ks
}

func (ks *keyServer) setRaw(body string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.raw = body
}

func (ks *keyServer) setCacheControl(v string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.cacheControl = v
}

func (ks *keyServer) set(status int, keys ...jose.JSONWebKey) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.status = status
	ks.keys = keys
}

func TestKey_UnknownKIDStorm_SingleFetchPerInterval(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock))
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if _, err := c.Key(ctx, "attacker-kid"); !errors.Is(err, jwks.ErrUnknownKey) {
				t.Errorf("ожидали ErrUnknownKey, получили %v", err)
			}
		})
	}
	wg.Wait()
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("100 промахов по kid → ожидали 1 загрузку, получили %d", got)
	}

	// Известный kid из кэша — без сети.
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("известный kid вызвал загрузку: %d", got)
	}

	// По истечении интервала промах снова ведёт к загрузке — ровно одной.
	clock.Advance(jwks.DefaultMinRefresh)
	for range 10 {
		_, _ = c.Key(ctx, "attacker-kid")
	}
	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("после интервала ожидали 2 загрузки, получили %d", got)
	}
}

func TestKey_RotationPicksNewKID(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock))
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	ks.set(http.StatusOK, jwk(t, "k1"), jwk(t, "k2"))

	// Внутри интервала новый kid ещё неизвестен (троттлинг), после — подтянут.
	if _, err := c.Key(ctx, "k2"); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("до интервала ожидали ErrUnknownKey, получили %v", err)
	}
	clock.Advance(jwks.DefaultMinRefresh)
	k, err := c.Key(ctx, "k2")
	if err != nil {
		t.Fatalf("Key k2 после ротации: %v", err)
	}
	if k.KeyID != "k2" {
		t.Fatalf("kid=%s", k.KeyID)
	}
}

func TestRefresh_ForceIgnoresThrottle(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	c := jwks.New(ks.srv.URL, ks.srv.Client())
	ctx := context.Background()

	for range 3 {
		if err := c.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
	}
	if got := ks.hits.Load(); got != 3 {
		t.Fatalf("Refresh должен грузить всегда: %d загрузок", got)
	}
}

func TestKey_FailureIsThrottledToo(t *testing.T) {
	ks := newKeyServer(t)
	ks.set(http.StatusInternalServerError)
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock))
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err == nil {
		t.Fatal("недоступный JWKS не дал ошибку")
	}
	// Пока набор не загружен, повторные вызовы отдают ошибку последней попытки без сети.
	if _, err := c.Key(ctx, "k1"); err == nil || errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("ожидали ошибку загрузки, получили %v", err)
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("провал не затроттлен: %d загрузок", got)
	}

	ks.set(http.StatusOK, jwk(t, "k1"))
	clock.Advance(jwks.DefaultMinRefresh)
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("после восстановления: %v", err)
	}
}

func TestKey_StaleKeysSurviveOutage(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock))
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key: %v", err)
	}
	ks.set(http.StatusBadGateway)
	clock.Advance(jwks.DefaultMinRefresh)

	if _, err := c.Key(ctx, "k2"); err == nil {
		t.Fatal("промах при недоступном сервере не дал ошибку")
	}
	// Загруженные ключи остаются рабочими при недоступном issuer'е.
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("старые ключи потеряны: %v", err)
	}
}

func TestKey_NoKeys(t *testing.T) {
	ks := newKeyServer(t)
	c := jwks.New(ks.srv.URL, ks.srv.Client())
	if err := c.Refresh(context.Background()); !errors.Is(err, jwks.ErrNoKeys) {
		t.Fatalf("ожидали ErrNoKeys, получили %v", err)
	}
}

func TestKey_EmptyKID(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "only"))
	c := jwks.New(ks.srv.URL, ks.srv.Client())
	ctx := context.Background()

	k, err := c.Key(ctx, "")
	if err != nil || k.KeyID != "only" {
		t.Fatalf("единственный ключ без kid: k=%s err=%v", k.KeyID, err)
	}

	ks.set(http.StatusOK, jwk(t, "a"), jwk(t, "b"))
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := c.Key(ctx, ""); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("пустой kid при нескольких ключах: ожидали ErrUnknownKey, получили %v", err)
	}
}

func TestFetch_DetachedFromCallerCancel(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	var seen atomic.Int64
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithOnRefresh(func(_ context.Context, err error) {
		if err == nil {
			seen.Add(1)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("отменённый ctx вызывающего уронил общую загрузку: %v", err)
	}
	if seen.Load() != 1 {
		t.Fatalf("OnRefresh не вызван: %d", seen.Load())
	}
}

func TestNew_NilDoerHasTimeout(t *testing.T) {
	// Сервер молчит дольше таймаута — nil Doer обязан оборваться сам.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := jwks.New(srv.URL, nil, jwks.WithTimeout(50*time.Millisecond))
	start := time.Now()
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("ожидали ошибку таймаута")
	}
	if time.Since(start) > time.Second {
		t.Fatal("таймаут не сработал")
	}
}

// Force-Refresh не должен «слиться» с промахом, который троттлинг отказался
// грузить: контракт Refresh — фактическая загрузка.
func TestRefresh_NotMergedWithThrottledMiss(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock))
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	ks.set(http.StatusOK, jwk(t, "k1"), jwk(t, "k2"))

	var wg sync.WaitGroup
	wg.Go(func() {
		// Внутри окна троттлинга промах сети не касается.
		if _, err := c.Key(ctx, "k2"); !errors.Is(err, jwks.ErrUnknownKey) {
			t.Errorf("промах внутри окна: ожидали ErrUnknownKey, получили %v", err)
		}
	})
	wg.Go(func() {
		if err := c.Refresh(ctx); err != nil {
			t.Errorf("Refresh: %v", err)
		}
	})
	wg.Wait()

	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("ожидали 2 загрузки (первичная + force), получили %d", got)
	}
	if _, err := c.Key(ctx, "k2"); err != nil {
		t.Fatalf("после force-Refresh новый kid не подтянулся: %v", err)
	}
}

// Неподдерживаемый kty в наборе не должен ронять разбор остальных ключей.
func TestLoad_SkipsUnsupportedKeyType(t *testing.T) {
	ks := newKeyServer(t)
	good, err := jwk(t, "k1").MarshalJSON()
	if err != nil {
		t.Fatalf("marshal jwk: %v", err)
	}
	ks.setRaw(`{"keys":[{"kty":"XYZ","kid":"future"},` + string(good) + `]}`)
	c := jwks.New(ks.srv.URL, ks.srv.Client())

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	k, err := c.Key(context.Background(), "k1")
	if err != nil || k.KeyID != "k1" {
		t.Fatalf("k1 не найден: k=%q err=%v", k.KeyID, err)
	}
	if _, err := c.Key(context.Background(), "future"); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("неподдерживаемый ключ попал в набор: %v", err)
	}
}

// Ключ с use=enc под тем же kid не должен подменять подписывающий.
func TestLookup_SkipsNonSigningKey(t *testing.T) {
	sig := jwk(t, "k1")
	enc := jwk(t, "k1")
	enc.Use = "enc"
	ks := newKeyServer(t, enc, sig)
	c := jwks.New(ks.srv.URL, ks.srv.Client())

	k, err := c.Key(context.Background(), "k1")
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if k.Use != "sig" {
		t.Fatalf("выбран ключ use=%q, ожидали sig", k.Use)
	}
}

func TestLoad_NoUsableKeys(t *testing.T) {
	ks := newKeyServer(t)
	ks.setRaw(`{"keys":[{"kty":"XYZ","kid":"future"}]}`)
	c := jwks.New(ks.srv.URL, ks.srv.Client())

	if err := c.Refresh(context.Background()); !errors.Is(err, jwks.ErrNoKeys) {
		t.Fatalf("ожидали ErrNoKeys, получили %v", err)
	}
}

// Выведенный из набора kid остаётся рабочим весь grace-период и отвергается после.
func TestRetainPrevious_RotatedKIDSurvivesGrace(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(),
		jwks.WithClock(clock), jwks.WithRetainPrevious(10*time.Minute))
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	ks.set(http.StatusOK, jwk(t, "k2"))
	clock.Advance(jwks.DefaultMinRefresh)
	if _, err := c.Key(ctx, "k2"); err != nil {
		t.Fatalf("Key k2 после ротации: %v", err)
	}

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("выведенный kid не пережил ротацию: %v", err)
	}
	clock.Advance(11 * time.Minute)
	if _, err := c.Key(ctx, "k1"); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("после grace-периода ожидали ErrUnknownKey, получили %v", err)
	}
}

// Регрессия KTST-02: grace-срок выведенного поколения истекает и при недоступном JWKS — простой
// endpoint'а не продлевает доверие снятому ключу, а действующий набор остаётся рабочим.
func TestRetainPrevious_GraceExpiresDuringOutage(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(),
		jwks.WithClock(clock), jwks.WithRetainPrevious(10*time.Minute))
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	ks.set(http.StatusOK, jwk(t, "k2"))
	clock.Advance(jwks.DefaultMinRefresh)
	if _, err := c.Key(ctx, "k2"); err != nil {
		t.Fatalf("Key k2 после ротации: %v", err)
	}

	ks.set(http.StatusBadGateway)
	clock.Advance(5 * time.Minute)
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("выведенный kid внутри grace-периода при недоступном JWKS: %v", err)
	}
	clock.Advance(6 * time.Minute)
	if k, err := c.Key(ctx, "k1"); err == nil {
		t.Fatalf("после grace-периода выведенный kid %q всё ещё доверен: JWKS недоступен, а срок истёк", k.KeyID)
	}
	if _, err := c.Key(ctx, "k2"); err != nil {
		t.Fatalf("действующий ключ потерян при недоступном JWKS: %v", err)
	}
}

// Регрессия KTST-02: в набор идут только публичные ключи. Симметричный (oct) или приватный JWK,
// попавший в ответ issuer'а, проверять подпись не должен: его значение знает каждый, кто читает JWKS.
func TestLoad_SkipsNonPublicKeys(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	secret := jose.JSONWebKey{Key: []byte("0123456789abcdef0123456789abcdef"), KeyID: "oct", Algorithm: string(jose.HS256), Use: "sig"}
	private := jose.JSONWebKey{Key: priv, KeyID: "private", Algorithm: string(jose.ES256), Use: "sig"}
	ctx := context.Background()

	mixed := newKeyServer(t, secret, private, jwk(t, "public"))
	c := jwks.New(mixed.srv.URL, mixed.srv.Client())
	if k, err := c.Key(ctx, "public"); err != nil || !k.IsPublic() {
		t.Fatalf("публичный ключ рядом с непубличными: public=%v err=%v", k.IsPublic(), err)
	}
	for _, kid := range []string{"oct", "private"} {
		if _, err := c.Key(ctx, kid); !errors.Is(err, jwks.ErrUnknownKey) {
			t.Errorf("kid %q: непубличный ключ попал в набор (err=%v), ожидали ErrUnknownKey", kid, err)
		}
	}

	only := newKeyServer(t, secret, private)
	if err := jwks.New(only.srv.URL, only.srv.Client()).Refresh(ctx); !errors.Is(err, jwks.ErrNoKeys) {
		t.Fatalf("набор из одних непубличных ключей: ожидали ErrNoKeys, получили %v", err)
	}
}

// Регрессия KTST-02: тело JWKS читается не дальше 1 МиБ — набор с бесконечным «хвостом» отвергается
// ошибкой разбора, а не загружается в память целиком.
func TestLoad_BodyIsLimited(t *testing.T) {
	good, err := jwk(t, "k1").MarshalJSON()
	if err != nil {
		t.Fatalf("marshal jwk: %v", err)
	}
	ks := newKeyServer(t)
	ks.setRaw(`{"keys":[` + string(good) + `],"padding":"` + strings.Repeat("x", 2<<20) + `"}`)
	c := jwks.New(ks.srv.URL, ks.srv.Client())

	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("JWKS на 2 МиБ принят: тело прочитано без предела")
	}
	if c.Loaded() {
		t.Fatal("после отвергнутого тела набор считается загруженным")
	}
}

// Регрессия KTST-02: max-age issuer'а расширяет окно не дальше MaxCacheControlAge — сутки в
// Cache-Control не оставляют снятый ключ доверенным на сутки.
func TestRespectCacheControl_CappedAtMaxAge(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	ks.setCacheControl("public, max-age=86400")
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock), jwks.WithRespectCacheControl())
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	clock.Advance(jwks.MaxCacheControlAge - time.Second)
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("внутри потолка окна ожидали 1 загрузку, получили %d", got)
	}
	clock.Advance(time.Second)
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("max-age=86400 не ограничен потолком %s: %d загрузок, ожидали 2", jwks.MaxCacheControlAge, got)
	}
}

func TestRespectCacheControl_WidensThrottleWindow(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	ks.setCacheControl("public, max-age=300")
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock), jwks.WithRespectCacheControl())
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	clock.Advance(jwks.DefaultMinRefresh)
	if _, err := c.Key(ctx, "k2"); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("ожидали ErrUnknownKey, получили %v", err)
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("max-age=300 не расширил окно: %d загрузок", got)
	}

	clock.Advance(5 * time.Minute)
	if _, err := c.Key(ctx, "k2"); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("ожидали ErrUnknownKey, получили %v", err)
	}
	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("после max-age ожидали 2 загрузки, получили %d", got)
	}
}

// WithMinRefresh сужает окно троттлинга: новый kid подхватывается раньше
// DefaultMinRefresh. Неположительное значение опции окно не трогает.
func TestWithMinRefresh_NarrowsThrottleWindow(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock), jwks.WithMinRefresh(5*time.Second))
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	ks.set(http.StatusOK, jwk(t, "k1"), jwk(t, "k2"))
	clock.Advance(5 * time.Second)
	if _, err := c.Key(ctx, "k2"); err != nil {
		t.Fatalf("новый kid внутри 5s-окна не подтянулся: %v", err)
	}
	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("загрузок: %d, ожидали 2", got)
	}

	ignored := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock), jwks.WithMinRefresh(0))
	if _, err := ignored.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	clock.Advance(5 * time.Second)
	if _, err := ignored.Key(ctx, "k3"); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("ожидали ErrUnknownKey, получили %v", err)
	}
	if got := ks.hits.Load(); got != 3 {
		t.Fatalf("WithMinRefresh(0) сузил окно: %d загрузок, ожидали 3", got)
	}
}

// Issuer, запрещающий кэширование или присылающий мусор в max-age, не должен
// ни расширять, ни ломать окно троттлинга — остаётся DefaultMinRefresh.
func TestRespectCacheControl_IgnoresNoStoreAndGarbage(t *testing.T) {
	headers := map[string]string{
		"no-store":        "no-store",
		"no-cache":        "private, no-cache",
		"битый max-age":   "max-age=abc",
		"нулевой max-age": "public, max-age=0",
	}
	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			ks := newKeyServer(t, jwk(t, "k1"))
			ks.setCacheControl(header)
			clock := clockkit.NewTickable(time.Now())
			c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock), jwks.WithRespectCacheControl())
			ctx := context.Background()

			if _, err := c.Key(ctx, "k1"); err != nil {
				t.Fatalf("Key k1: %v", err)
			}
			clock.Advance(jwks.DefaultMinRefresh / 2)
			if _, err := c.Key(ctx, "k2"); !errors.Is(err, jwks.ErrUnknownKey) {
				t.Fatalf("ожидали ErrUnknownKey, получили %v", err)
			}
			if got := ks.hits.Load(); got != 1 {
				t.Fatalf("промах внутри окна сходил в сеть: %d загрузок", got)
			}

			clock.Advance(jwks.DefaultMinRefresh / 2)
			if _, err := c.Key(ctx, "k2"); !errors.Is(err, jwks.ErrUnknownKey) {
				t.Fatalf("ожидали ErrUnknownKey, получили %v", err)
			}
			if got := ks.hits.Load(); got != 2 {
				t.Fatalf("после DefaultMinRefresh ожидали 2 загрузки, получили %d", got)
			}
		})
	}
}

func TestNew_LoadedReflectsFirstFetch(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	c := jwks.New(ks.srv.URL, ks.srv.Client())
	if c.Loaded() {
		t.Fatal("до загрузки Loaded=true")
	}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !c.Loaded() {
		t.Fatal("после загрузки Loaded=false")
	}
}
