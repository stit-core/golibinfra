// Package jwks — общий JWKS-кэш верификаторов (oidckit, hubkit/sso): ключи
// грузятся лениво и по неизвестному kid, но не чаще MinRefresh, а параллельные
// промахи сливаются в одну загрузку (singleflight).
package jwks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/sync/singleflight"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

var (
	ErrUnknownKey = errors.New("jwks: no signing key for kid")
	ErrNoKeys     = errors.New("jwks: endpoint returned no keys")
)

const (
	DefaultMinRefresh = time.Minute
	DefaultTimeout    = 10 * time.Second

	// maxBody — JWKS реального issuer'а занимает килобайты; лимит защищает от бесконечного тела.
	maxBody = 1 << 20
)

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type Options struct {
	MinRefresh time.Duration
	Timeout    time.Duration
	Clock      clockkit.Clock
	// OnRefresh получает исход каждой фактической загрузки (метрики кита-владельца).
	OnRefresh func(ctx context.Context, err error)
}

type Cache struct {
	url        string
	http       Doer
	minRefresh time.Duration
	timeout    time.Duration
	clock      clockkit.Clock
	onRefresh  func(ctx context.Context, err error)

	group singleflight.Group

	mu          sync.RWMutex
	keys        jose.JSONWebKeySet
	loaded      bool
	lastAttempt time.Time
	lastErr     error
}

func New(url string, doer Doer, opts Options) *Cache {
	if opts.MinRefresh <= 0 {
		opts.MinRefresh = DefaultMinRefresh
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Clock == nil {
		opts.Clock = clockkit.Real{}
	}
	if doer == nil {
		doer = &http.Client{Timeout: opts.Timeout}
	}
	return &Cache{
		url:        url,
		http:       doer,
		minRefresh: opts.MinRefresh,
		timeout:    opts.Timeout,
		clock:      opts.Clock,
		onRefresh:  opts.OnRefresh,
	}
}

// Key отдаёт ключ по kid; пустой kid допустим только при единственном ключе в
// наборе. Промах → загрузка (с троттлингом) и повторный поиск.
func (c *Cache) Key(ctx context.Context, kid string) (jose.JSONWebKey, error) {
	if k, ok := c.lookup(kid); ok {
		return k, nil
	}
	if err := c.refresh(ctx, false); err != nil {
		return jose.JSONWebKey{}, err
	}
	if k, ok := c.lookup(kid); ok {
		return k, nil
	}
	return jose.JSONWebKey{}, ErrUnknownKey
}

// Refresh грузит набор без троттлинга (первичная загрузка в конструкторе);
// параллельные вызовы всё равно сливаются.
func (c *Cache) Refresh(ctx context.Context) error {
	return c.refresh(ctx, true)
}

func (c *Cache) lookup(kid string) (jose.JSONWebKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return jose.JSONWebKey{}, false
	}
	if kid != "" {
		if ks := c.keys.Key(kid); len(ks) > 0 {
			return ks[0], true
		}
		return jose.JSONWebKey{}, false
	}
	if len(c.keys.Keys) == 1 {
		return c.keys.Keys[0], true
	}
	return jose.JSONWebKey{}, false
}

func (c *Cache) refresh(ctx context.Context, force bool) error {
	_, err, _ := c.group.Do("refresh", func() (any, error) {
		if !force {
			if skip, err := c.throttled(); skip {
				return nil, err
			}
		}
		return nil, c.fetch(ctx)
	})
	return err
}

// throttled — повторная загрузка не раньше minRefresh после предыдущей попытки;
// пока набор ни разу не загружен, наружу уходит ошибка последней попытки.
func (c *Cache) throttled() (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastAttempt.IsZero() || c.clock.Now().Sub(c.lastAttempt) >= c.minRefresh {
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

	err := c.load(ctx)

	c.mu.Lock()
	c.lastAttempt = c.clock.Now()
	c.lastErr = err
	c.mu.Unlock()

	if c.onRefresh != nil {
		c.onRefresh(ctx, err)
	}
	return err
}

func (c *Cache) load(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return fmt.Errorf("jwks: request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("jwks: fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks: fetch: status %d", resp.StatusCode)
	}

	var ks jose.JSONWebKeySet
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&ks); err != nil {
		return fmt.Errorf("jwks: decode: %w", err)
	}
	if len(ks.Keys) == 0 {
		return ErrNoKeys
	}

	c.mu.Lock()
	c.keys = ks
	c.loaded = true
	c.mu.Unlock()
	return nil
}
