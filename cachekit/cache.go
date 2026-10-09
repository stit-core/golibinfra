// Package cachekit — доступ к Redis: байтовый Cache с TTL, атомарными SetNX/GetDel/Compare* и
// тегированной инвалидацией, типизированные GetJSON/SetJSON, IdempotencyStore для httpkit,
// MemoryCache и health-проба для /ready; эмитит метрики и спаны команд.
package cachekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/internal/redact"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ErrConfig — невалидный Config: сервис падает на старте, а не на первой команде.
var ErrConfig = errors.New("cachekit: invalid config")

// ErrServerVersion — сервер старше minRedisMajor: тегированная инвалидация на
// нём не работает. Проверка снимается REDIS_SKIP_VERSION_CHECK=true.
var ErrServerVersion = errors.New("cachekit: unsupported redis server version")

// minRedisMajor — тегированная инвалидация опирается на `PEXPIRE … GT`
// (Redis 7.0+); на 6.x скрипт падает не на первой записи в тег, а на второй.
const minRedisMajor = 7

// Config — параметры клиента Redis; envconfig-теги — public API кита.
type Config struct {
	URL          string        `envconfig:"REDIS_URL" default:"redis://localhost:6379/0"`
	KeyPrefix    string        `envconfig:"REDIS_KEY_PREFIX" default:""`
	PoolSize     int           `envconfig:"REDIS_POOL_SIZE" default:"10"`
	MaxRetries   int           `envconfig:"REDIS_MAX_RETRIES" default:"3"`
	DialTimeout  time.Duration `envconfig:"REDIS_DIAL_TIMEOUT" default:"5s"`
	ReadTimeout  time.Duration `envconfig:"REDIS_READ_TIMEOUT" default:"3s"`
	WriteTimeout time.Duration `envconfig:"REDIS_WRITE_TIMEOUT" default:"3s"`
	PingOnStart  bool          `envconfig:"REDIS_PING_ON_START" default:"true"`
	// SkipVersionCheck снимает проверку версии сервера на старте — для
	// redis-совместимых серверов, которые не отдают redis_version в INFO.
	SkipVersionCheck bool `envconfig:"REDIS_SKIP_VERSION_CHECK" default:"false"`
}

// Validate проверяет инварианты конфига; вызывается из NewConfig и NewRedisCache.
func (c Config) Validate() error {
	switch {
	case c.PoolSize < 1:
		return fmt.Errorf("%w: REDIS_POOL_SIZE must be >= 1, got %d", ErrConfig, c.PoolSize)
	case c.MaxRetries < -1:
		return fmt.Errorf("%w: REDIS_MAX_RETRIES must be >= -1 (-1 disables retries), got %d", ErrConfig, c.MaxRetries)
	case c.DialTimeout < 0:
		return fmt.Errorf("%w: REDIS_DIAL_TIMEOUT must be >= 0, got %s", ErrConfig, c.DialTimeout)
	case c.ReadTimeout < 0:
		return fmt.Errorf("%w: REDIS_READ_TIMEOUT must be >= 0, got %s", ErrConfig, c.ReadTimeout)
	case c.WriteTimeout < 0:
		return fmt.Errorf("%w: REDIS_WRITE_TIMEOUT must be >= 0, got %s", ErrConfig, c.WriteTimeout)
	}
	if _, err := redis.ParseURL(c.URL); err != nil {
		return fmt.Errorf("%w: REDIS_URL %q: %w", ErrConfig, redact.URL(c.URL), redact.ParseError(err))
	}
	return nil
}

// Cache — байтовый KV. Промах везде — found == false, err == nil. SetNX, GetDel, CompareAndDelete
// и CompareAndSet на сбое связи не повторяются: повтор увидел бы эффект первой попытки, и ответ
// «нет ключа» или «занято» был бы ложным — вызывающий получает ошибку.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// SetNX пишет только отсутствующий ключ: атомарный резерв/lock.
	SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	// GetDel читает и удаляет атомарно: одноразовые токены и state.
	GetDel(ctx context.Context, key string) ([]byte, bool, error)
	// CompareAndDelete удаляет ключ атомарно и только если его значение равно
	// expected: снятие собственного резерва, не затирающее чужую запись.
	CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
	// CompareAndSet пишет value с ttl атомарно и только если значение ключа равно expected:
	// запись поверх собственного резерва, не затирающая чужой.
	CompareAndSet(ctx context.Context, key string, expected, value []byte, ttl time.Duration) (bool, error)
	Delete(ctx context.Context, keys ...string) error
	// SetWithTags пишет ключ и добавляет его в наборы tag:<tag> для InvalidateByTag;
	// набор живёт не меньше самого долгоживущего своего члена.
	SetWithTags(ctx context.Context, key string, value []byte, ttl time.Duration, tags ...string) error
	InvalidateByTag(ctx context.Context, tag string) error
	Ping(ctx context.Context) error
	// Close идемпотентен: повторный вызов возвращает nil.
	Close() error
}

const tagPrefix = "tag:"

// invalidateBatch — размер порции SPOP: набор в миллион членов не превращается
// в один блокирующий ответ, но и round-trip'ов остаётся немного.
const invalidateBatch = 256

// setWithTagsScript держит TTL набора не меньше TTL любого живого члена: новый
// набор получает TTL записи, существующий — только продление (PEXPIRE … GT),
// а запись без TTL делает набор персистентным. PEXPIRE … GT — Redis >= 7.0.
var setWithTagsScript = redis.NewScript(`
local ttl = tonumber(ARGV[2])
if ttl > 0 then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl)
else
  redis.call('SET', KEYS[1], ARGV[1])
end
for i = 2, #KEYS do
  local fresh = redis.call('EXISTS', KEYS[i]) == 0
  redis.call('SADD', KEYS[i], KEYS[1])
  if ttl <= 0 then
    redis.call('PERSIST', KEYS[i])
  elseif fresh then
    redis.call('PEXPIRE', KEYS[i], ttl)
  else
    redis.call('PEXPIRE', KEYS[i], ttl, 'GT')
  end
end
return 1
`)

// invalidateTagScript снимает членство и удаляет ключи одной командой: между
// отдельными SPOP и UNLINK сетевой сбой оставил бы ключи в Redis вне какого-либо
// тега — следующий InvalidateByTag их бы уже не нашёл. Ключи-члены не объявлены
// в KEYS: кит работает с одиночным сервером, не с cluster-режимом.
var invalidateTagScript = redis.NewScript(`
local members = redis.call('SPOP', KEYS[1], ARGV[1])
if #members > 0 then
  redis.call('UNLINK', unpack(members))
end
return #members
`)

var compareAndDeleteScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

var compareAndSetScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
local ttl = tonumber(ARGV[3])
if ttl > 0 then
  redis.call('SET', KEYS[1], ARGV[2], 'PX', ttl)
else
  redis.call('SET', KEYS[1], ARGV[2])
end
return 1
`)

type redisCache struct {
	client *redis.Client
	// once — тот же сервер без повторов go-redis: для неидемпотентных команд.
	once   *redis.Client
	prefix string
}

// NewRedisCache собирает Cache поверх go-redis; без fx закрывать Close самому. Внутренний лог
// go-redis (пул, переподключения) уходит в log записью WARN; nil и logkit.Nop его не забирают.
// Версию сервера сверяет только Module на старте: вне fx её не проверяет никто.
func NewRedisCache(cfg Config, log logkit.Logger) (Cache, error) {
	return newRedisCache(cfg, log)
}

func newRedisCache(cfg Config, log logkit.Logger) (*redisCache, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	redisLog.install(log)
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("cachekit: parse redis url: %w", redact.ParseError(err))
	}
	opts.PoolSize = cfg.PoolSize
	opts.MaxRetries = cfg.MaxRetries
	opts.DialTimeout = cfg.DialTimeout
	opts.ReadTimeout = cfg.ReadTimeout
	opts.WriteTimeout = cfg.WriteTimeout
	// Иначе go-redis ставит на сокет только ReadTimeout, и команда переживает
	// бюджет запроса: дедлайн ctx до соединения не доходит.
	opts.ContextTimeoutEnabled = true

	hook := newObserveHook()
	client := redis.NewClient(opts)
	client.AddHook(hook)
	onceOpts := *opts
	onceOpts.MaxRetries = -1
	once := redis.NewClient(&onceOpts)
	once.AddHook(hook)
	return &redisCache{client: client, once: once, prefix: cfg.KeyPrefix}, nil
}

func (c *redisCache) key(key string) string { return c.prefix + key }

func (c *redisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.client.Get(ctx, c.key(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cachekit: get: %w", err)
	}
	return val, true, nil
}

func (c *redisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := c.client.Set(ctx, c.key(key), value, ttl).Err(); err != nil {
		return fmt.Errorf("cachekit: set: %w", err)
	}
	return nil
}

func (c *redisCache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	ok, err := c.once.SetNX(ctx, c.key(key), value, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("cachekit: setnx: %w", err)
	}
	return ok, nil
}

func (c *redisCache) GetDel(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.once.GetDel(ctx, c.key(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cachekit: getdel: %w", err)
	}
	return val, true, nil
}

func (c *redisCache) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	deleted, err := compareAndDeleteScript.Run(ctx, c.once, []string{c.key(key)}, expected).Int64()
	if err != nil {
		return false, fmt.Errorf("cachekit: compare and delete: %w", err)
	}
	return deleted > 0, nil
}

func (c *redisCache) CompareAndSet(ctx context.Context, key string, expected, value []byte, ttl time.Duration) (bool, error) {
	set, err := compareAndSetScript.Run(ctx, c.once, []string{c.key(key)}, expected, value, milliseconds(ttl)).Int64()
	if err != nil {
		return false, fmt.Errorf("cachekit: compare and set: %w", err)
	}
	return set > 0, nil
}

func (c *redisCache) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = c.key(k)
	}
	if err := c.client.Unlink(ctx, full...).Err(); err != nil {
		return fmt.Errorf("cachekit: delete: %w", err)
	}
	return nil
}

func (c *redisCache) SetWithTags(ctx context.Context, key string, value []byte, ttl time.Duration, tags ...string) error {
	if len(tags) == 0 {
		return c.Set(ctx, key, value, ttl)
	}
	keys := make([]string, 0, len(tags)+1)
	keys = append(keys, c.key(key))
	for _, tag := range tags {
		keys = append(keys, c.key(tagPrefix+tag))
	}
	if err := setWithTagsScript.Run(ctx, c.client, keys, value, milliseconds(ttl)).Err(); err != nil {
		return fmt.Errorf("cachekit: set with tags: %w", err)
	}
	return nil
}

func (c *redisCache) InvalidateByTag(ctx context.Context, tag string) error {
	tagKey := c.key(tagPrefix + tag)
	for {
		// SPOP снимает членство до удаления ключей: конкурентный SetWithTags
		// либо попадёт в текущую порцию, либо пересоздаст набор со своим ключом.
		popped, err := invalidateTagScript.Run(ctx, c.client, []string{tagKey}, invalidateBatch).Int64()
		if err != nil {
			return fmt.Errorf("cachekit: invalidate tag: %w", err)
		}
		if popped < invalidateBatch {
			return nil
		}
	}
}

func (c *redisCache) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("cachekit: ping: %w", err)
	}
	return nil
}

// checkServerVersion валит старт на сервере старше minRedisMajor: иначе первая
// запись в новый тег проходит, а вторая падает ошибкой скрипта — отказ
// «иногда работает» дороже в диагностике, чем отказ на старте.
func (c *redisCache) checkServerVersion(ctx context.Context) error {
	info, err := c.client.Info(ctx, "server").Result()
	if err != nil {
		return fmt.Errorf("cachekit: info server: %w", err)
	}
	return checkInfoVersion(info)
}

func checkInfoVersion(info string) error {
	version := infoField(info, "redis_version")
	if version == "" {
		return fmt.Errorf("%w: INFO server reports no redis_version; set REDIS_SKIP_VERSION_CHECK=true for a non-standard server", ErrServerVersion)
	}
	head, _, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(head)
	if err != nil {
		return fmt.Errorf("%w: cannot read the major version of %q; set REDIS_SKIP_VERSION_CHECK=true for a non-standard server", ErrServerVersion, version)
	}
	if major < minRedisMajor {
		return fmt.Errorf("%w: server reports %s, cachekit needs >= %d.0 (tagged invalidation uses PEXPIRE … GT)",
			ErrServerVersion, version, minRedisMajor)
	}
	return nil
}

// infoField — значение поля ответа INFO (строки вида `<name>:<value>`).
func infoField(info, name string) string {
	for line := range strings.SplitSeq(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), name+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (c *redisCache) Close() error {
	var errs []error
	for _, client := range []*redis.Client{c.client, c.once} {
		if err := client.Close(); err != nil && !errors.Is(err, redis.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("cachekit: close: %w", err)
	}
	return nil
}

// milliseconds — TTL в целых миллисекундах для PEXPIRE; sub-миллисекундный TTL
// округляется вверх, иначе он неотличим от «без срока жизни».
func milliseconds(ttl time.Duration) int64 {
	ms := ttl.Milliseconds()
	if ttl > 0 && ms == 0 {
		return 1
	}
	return ms
}

// Probe — health-проба кэша для fx-группы `healthkit.probes` (healthkit.ProvideProbe).
func Probe(c Cache) healthkit.Probe {
	return healthkit.New("cachekit", c.Ping)
}

// GetJSON читает значение и разбирает его в T; битый JSON — ошибка с found == false.
func GetJSON[T any](ctx context.Context, c Cache, key string) (T, bool, error) {
	var zero T
	data, found, err := c.Get(ctx, key)
	if err != nil || !found {
		return zero, found, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, false, fmt.Errorf("cachekit: unmarshal: %w", err)
	}
	return v, true, nil
}

// SetJSON сериализует value и пишет его по ключу с TTL (0 — без срока жизни).
func SetJSON[T any](ctx context.Context, c Cache, key string, value T, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cachekit: marshal: %w", err)
	}
	return c.Set(ctx, key, data, ttl)
}
