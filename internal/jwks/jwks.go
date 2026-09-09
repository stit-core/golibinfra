// Package jwks — общий JWKS-кэш верификаторов (oidckit, hubkit/sso): ключи
// грузятся лениво, перечитываются по неизвестному kid и по истечении окна
// свежести, но не чаще него, а параллельные промахи сливаются в одну загрузку
// (singleflight).
package jwks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/sync/singleflight"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/internal/httpdoer"
)

var (
	// ErrUnknownKey — в наборе нет подписывающего ключа с таким kid.
	ErrUnknownKey = errors.New("jwks: no signing key for kid")
	// ErrNoKeys — endpoint не отдал ни одного пригодного ключа проверки подписи.
	ErrNoKeys = errors.New("jwks: endpoint returned no usable keys")
)

const (
	// DefaultMinRefresh — окно свежести набора по умолчанию.
	DefaultMinRefresh = time.Minute
	// DefaultTimeout — дедлайн одной загрузки.
	DefaultTimeout = 10 * time.Second
	// MaxCacheControlAge — потолок окна свежести, которое issuer может
	// назначить через Cache-Control: max-age (WithRespectCacheControl).
	MaxCacheControlAge = 15 * time.Minute

	// maxBody — JWKS реального issuer'а занимает килобайты; лимит защищает от бесконечного тела.
	maxBody = 1 << 20
)

// Doer — минимальный HTTP-клиент загрузки JWKS; ему удовлетворяют
// *httpclient.Client (retry + breaker + OTel) и *http.Client.
type Doer = httpdoer.Doer

// Option — настройка кэша, передаётся в New.
type Option func(*Cache)

// WithMinRefresh задаёт окно свежести набора: минимальный интервал между
// загрузками и он же период перечитывания (default DefaultMinRefresh).
func WithMinRefresh(d time.Duration) Option {
	return func(c *Cache) {
		if d > 0 {
			c.minRefresh = d
		}
	}
}

// WithTimeout задаёт дедлайн одной загрузки (default DefaultTimeout).
func WithTimeout(d time.Duration) Option {
	return func(c *Cache) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithClock подменяет часы троттлинга и grace-периода (детерминированные тесты).
func WithClock(clock clockkit.Clock) Option {
	return func(c *Cache) {
		if clock != nil {
			c.clock = clock
		}
	}
}

// WithOnRefresh регистрирует наблюдателя исхода каждой фактической загрузки
// (метрики кита-владельца).
func WithOnRefresh(fn func(ctx context.Context, err error)) Option {
	return func(c *Cache) { c.onRefresh = fn }
}

// WithRetainPrevious оставляет предыдущее поколение ключей рабочим ещё d после
// успешной загрузки: токен, подписанный только что выведенным kid, проверяется
// весь grace-период, а не отвергается в момент ротации.
func WithRetainPrevious(d time.Duration) Option {
	return func(c *Cache) {
		if d > 0 {
			c.retainPrevious = d
		}
	}
}

// WithRespectCacheControl берёт окно свежести из `Cache-Control: max-age`
// ответа JWKS, когда оно длиннее MinRefresh (но не больше MaxCacheControlAge):
// issuer сам говорит, как часто его ключи имеет смысл перечитывать. Плата — на
// столько же растёт окно, в котором новый kid ещё неизвестен, а снятый ещё
// доверенный.
func WithRespectCacheControl() Option {
	return func(c *Cache) { c.respectCacheControl = true }
}

// Cache — набор ключей одного JWKS-endpoint'а: ленивая загрузка, перечитывание
// по окну свежести и по неизвестному kid, singleflight на параллельные промахи
// и grace-период на предыдущее поколение.
type Cache struct {
	url                 string
	http                Doer
	minRefresh          time.Duration
	timeout             time.Duration
	clock               clockkit.Clock
	onRefresh           func(ctx context.Context, err error)
	retainPrevious      time.Duration
	respectCacheControl bool

	group singleflight.Group

	mu          sync.RWMutex
	keys        []jose.JSONWebKey
	prev        []jose.JSONWebKey
	prevUntil   time.Time
	loaded      bool
	lastAttempt time.Time
	lastErr     error
	interval    time.Duration
	gen         uint64
}

// New строит кэш JWKS-endpoint'а url. nil doer → http.Client с таймаутом загрузки.
func New(url string, doer Doer, opts ...Option) *Cache {
	c := &Cache{
		url:        url,
		http:       doer,
		minRefresh: DefaultMinRefresh,
		timeout:    DefaultTimeout,
		clock:      clockkit.Real{},
	}
	for _, o := range opts {
		o(c)
	}
	c.interval = c.minRefresh
	if c.http == nil {
		c.http = &http.Client{Timeout: c.timeout}
	}
	return c
}

// Key отдаёт ключ по kid; пустой kid допустим только при единственном ключе в
// наборе. Промах → загрузка (с троттлингом) и повторный поиск. Набор старше
// окна перечитывания обновляется и при попадании: снятый issuer'ом ключ
// сохраняет прежний kid, промахов не даёт и без этого оставался бы доверенным
// до перезапуска процесса. Недоступный endpoint не отзывает уже известный
// ключ — иначе простой JWKS ронял бы проверку всех живых токенов.
func (c *Cache) Key(ctx context.Context, kid string) (jose.JSONWebKey, error) {
	cached, hit := c.lookup(kid)
	if hit && !c.stale() {
		return cached, nil
	}
	if err := c.refresh(ctx, false); err != nil {
		if hit {
			return cached, nil
		}
		return jose.JSONWebKey{}, err
	}
	if k, ok := c.lookup(kid); ok {
		return k, nil
	}
	return jose.JSONWebKey{}, ErrUnknownKey
}

// Refresh грузит набор без троттлинга (первичная загрузка, health-проба).
// Загрузку, завершившуюся уже после вызова, Refresh принимает как свою.
func (c *Cache) Refresh(ctx context.Context) error {
	return c.refresh(ctx, true)
}

// Loaded — загружен ли хотя бы один набор ключей (готовность верификатора).
func (c *Cache) Loaded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

func (c *Cache) lookup(kid string) (jose.JSONWebKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return jose.JSONWebKey{}, false
	}
	if k, ok := pick(c.keys, kid); ok {
		return k, true
	}
	if len(c.prev) > 0 && c.clock.Now().Before(c.prevUntil) {
		return pick(c.prev, kid)
	}
	return jose.JSONWebKey{}, false
}

func pick(keys []jose.JSONWebKey, kid string) (jose.JSONWebKey, bool) {
	if kid == "" {
		if len(keys) == 1 {
			return keys[0], true
		}
		return jose.JSONWebKey{}, false
	}
	for _, k := range keys {
		if k.KeyID == kid {
			return k, true
		}
	}
	return jose.JSONWebKey{}, false
}

func (c *Cache) refresh(ctx context.Context, force bool) error {
	// Троттлинг решается до singleflight: иначе force-Refresh слился бы с
	// промахом, который внутри Do отказался грузить, и вернулся бы без загрузки.
	if !force {
		if skip, err := c.throttled(); skip {
			return err
		}
	}
	gen := c.generation()
	_, err, _ := c.group.Do("refresh", func() (any, error) {
		// Пока ждали очереди, набор загрузил кто-то другой: повторный поход в
		// сеть ничего не добавит.
		if c.generation() != gen {
			return nil, c.lastError()
		}
		return nil, c.fetch(ctx)
	})
	return err
}

func (c *Cache) generation() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.gen
}

func (c *Cache) lastError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErr
}

// stale — окно перечитывания после последней попытки истекло: следующий Key
// сходит за набором, даже если его kid уже известен.
func (c *Cache) stale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.lastAttempt.IsZero() && c.clock.Now().Sub(c.lastAttempt) >= c.interval
}

// throttled — повторная загрузка не раньше окна свежести после предыдущей
// попытки; пока набор ни разу не загружен, наружу уходит ошибка последней попытки.
func (c *Cache) throttled() (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastAttempt.IsZero() || c.clock.Now().Sub(c.lastAttempt) >= c.interval {
		return false, nil
	}
	if !c.loaded {
		return true, fmt.Errorf("jwks: refresh throttled: %w", c.lastErr)
	}
	return true, nil
}

// fetch идёт по сети с собственным дедлайном и без отмены ctx вызывающего:
// результат singleflight делят все ожидающие, чужой Cancel не должен его ронять.
func (c *Cache) fetch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()

	keys, maxAge, err := c.load(ctx)

	c.mu.Lock()
	c.lastAttempt = c.clock.Now()
	c.lastErr = err
	c.gen++
	if err == nil {
		if c.retainPrevious > 0 && len(c.keys) > 0 {
			c.prev = c.keys
			c.prevUntil = c.lastAttempt.Add(c.retainPrevious)
		}
		c.keys = keys
		c.loaded = true
		c.interval = c.effectiveInterval(maxAge)
	}
	c.mu.Unlock()

	if c.onRefresh != nil {
		c.onRefresh(ctx, err)
	}
	return err
}

func (c *Cache) effectiveInterval(maxAge time.Duration) time.Duration {
	if !c.respectCacheControl || maxAge <= c.minRefresh {
		return c.minRefresh
	}
	return min(maxAge, MaxCacheControlAge)
}

func (c *Cache) load(ctx context.Context) ([]jose.JSONWebKey, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("jwks: request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("jwks: fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("jwks: fetch: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, 0, fmt.Errorf("jwks: read: %w", err)
	}
	keys, err := parseKeys(body)
	if err != nil {
		return nil, 0, err
	}
	return keys, cacheControlMaxAge(resp.Header.Get("Cache-Control")), nil
}

// parseKeys разбирает набор поэлементно: RFC 7517 §5 требует игнорировать
// непонятные JWK, а не отбрасывать весь набор из-за одного нового kty.
func parseKeys(body []byte) ([]jose.JSONWebKey, error) {
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("jwks: decode: %w", err)
	}
	keys := make([]jose.JSONWebKey, 0, len(set.Keys))
	for _, raw := range set.Keys {
		var k jose.JSONWebKey
		if err := k.UnmarshalJSON(raw); err != nil {
			continue
		}
		if usable(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, ErrNoKeys
	}
	return keys, nil
}

// usable — ключ годится для проверки подписи: публичный, валидный и не
// помеченный другим use (шифровальный ключ под тем же kid не должен подменять
// подписывающий).
func usable(k jose.JSONWebKey) bool {
	if k.Use != "" && k.Use != "sig" {
		return false
	}
	return k.Valid() && k.IsPublic()
}

func cacheControlMaxAge(header string) time.Duration {
	var maxAge time.Duration
	for part := range strings.SplitSeq(strings.ToLower(header), ",") {
		directive := strings.TrimSpace(part)
		if directive == "no-store" || directive == "no-cache" {
			return 0
		}
		value, ok := strings.CutPrefix(directive, "max-age=")
		if !ok {
			continue
		}
		secs, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || secs <= 0 {
			return 0
		}
		maxAge = time.Duration(secs) * time.Second
	}
	return maxAge
}
