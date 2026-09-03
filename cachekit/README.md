# cachekit

Обёртка над Redis для сервисов stit. Интерфейс `Cache` — байтовый key-value с
TTL, атомарные `SetNX` (резерв/lock) и `GetDel` (одноразовые токены),
тегированная инвалидация, `Ping`/`Close`. Поверх него типизированные
`GetJSON[T]`/`SetJSON[T]`, `IdempotencyStore` для `httpkit.Idempotency`,
`MemoryCache` для тестов потребителя и health-проба для `/ready`.

Kit — про доступ к Redis и сериализацию; политику ключей и TTL определяет
потребитель.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/cachekit"
```

## Быстрый старт

```go
fx.New(
    fx.Provide(cachekit.NewConfig),          // REDIS_* из ENV (caller провайдит явно)
    cachekit.Module,                         // Cache: Ping на старте, Close на стопе
    healthkit.Provide(cachekit.Probe),       // /ready через httpkit

    fx.Invoke(func(c cachekit.Cache) {
        type Profile struct{ Name string }
        _ = cachekit.SetJSON(ctx, c, "user:42", Profile{Name: "alice"}, 5*time.Minute)
        p, found, err := cachekit.GetJSON[Profile](ctx, c, "user:42")   // miss: found=false, err=nil
        _ = c.SetWithTags(ctx, "user:42:orders", raw, time.Hour, "user:42")
        _ = c.InvalidateByTag(ctx, "user:42")
    }),
)
```

Без fx: `cachekit.NewRedisCache(cfg)`; закрывать `Close()` самому.

## Конфигурация

`cachekit.Config` — public API (корневой `README.md` § Стабильность API).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `REDIS_URL` | `URL` | `string` | `redis://localhost:6379/0` | Адрес Redis (`redis.ParseURL`); per-concern endpoint |
| `REDIS_POOL_SIZE` | `PoolSize` | `int` | `10` | Размер пула соединений |
| `REDIS_DIAL_TIMEOUT` | `DialTimeout` | `time.Duration` | `5s` | Таймаут установления соединения |
| `REDIS_READ_TIMEOUT` | `ReadTimeout` | `time.Duration` | `3s` | Таймаут чтения команды |
| `REDIS_WRITE_TIMEOUT` | `WriteTimeout` | `time.Duration` | `3s` | Таймаут записи команды |
| `REDIS_PING_ON_START` | `PingOnStart` | `bool` | `true` | `Module` делает `PING` в `OnStart`: недоступный Redis валит старт, а не первый запрос |

## Observability

Instrumentation-scope `git.stit.tech/stit-core/golibinfra/cachekit`, глобальный
`otel.Meter` (no-op без провайдера). Хук go-redis считает каждую команду:

| Сигнал | Тип | Labels | Назначение |
|---|---|---|---|
| `cache_commands_total` | Int64 Counter | `cmd`, `result` | Команды по имени (`get`, `set`, `setnx`, `getdel`, `del`, `pipeline`…); `result` — `ok`, `miss` (`redis.Nil`), `error` |
| `cache_command_duration_seconds` | Float64 Histogram (`s`) | `cmd` | Round-trip команды |

Health-проба:

```go
func Probe(c Cache) healthkit.Probe    // Redis PING
```

`Module` не кладёт пробу в граф сам — регистрирует потребитель:
`healthkit.Provide(cachekit.Probe)`.

## API

### `Cache`

```go
type Cache interface {
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
    GetDel(ctx context.Context, key string) ([]byte, bool, error)
    Delete(ctx context.Context, keys ...string) error
    SetWithTags(ctx context.Context, key string, value []byte, ttl time.Duration, tags ...string) error
    InvalidateByTag(ctx context.Context, tag string) error
    Ping(ctx context.Context) error
    Close() error
}
```

- `Get`/`GetDel` — `(value, found, error)`; промах это `found == false`, `err == nil`.
- `Set`/`SetWithTags` — `ttl == 0` означает ключ без срока жизни.
- `SetNX` — `true`, если ключ записан (его не было); `false`, если занят.
- `SetWithTags` — `SET` + `SADD tag:<tag> key` в одной транзакции; набор тега
  получает `EXPIRE ttl` при каждой записи.
- `InvalidateByTag` — `SMEMBERS` + `DEL` членов и самого набора; отсутствующий
  тег не ошибка.

### `GetJSON[T]` / `SetJSON[T]`

`encoding/json` поверх `Get`/`Set`; ошибка `Unmarshal` возвращается с `found == false`.

### `IdempotencyStore`

```go
func NewIdempotencyStore(c Cache) *IdempotencyStore

func (s *IdempotencyStore) Reserve(ctx, key string, ttl time.Duration) (bool, error)
func (s *IdempotencyStore) Get(ctx, key string) ([]byte, bool, error)
func (s *IdempotencyStore) Set(ctx, key string, record []byte, ttl time.Duration) error
func (s *IdempotencyStore) Release(ctx, key string) error
```

Байтовый store для `httpkit.Idempotency` (структурно совместим с
`httpkit.IdempotencyStore`): `Reserve` — `SETNX` маркера на время выполнения
запроса, `Set` — готовая запись, `Get` не отдаёт маркер как запись, `Release`
снимает резерв. Ключи живут в namespace `idempotency:`.

```go
fx.Provide(func(c cachekit.Cache) httpkit.IdempotencyStore { return cachekit.NewIdempotencyStore(c) })
```

### `MemoryCache`

`NewMemoryCache()` — потокобезопасная in-memory реализация `Cache` с той же
семантикой TTL, `SetNX`, `GetDel` и тегов. Для unit-тестов потребителя, не для прода.

## Тестирование

Интеграционные тесты поднимают Redis через `testkit.StartRedis` и пропускаются
в `-short`; `MemoryCache`, `IdempotencyStore` и хук метрик покрыты unit-тестами.

```bash
go test -short -race ./cachekit/...   # unit
go test -race ./cachekit/...          # + Redis в testcontainers
```

## Инварианты и подводные камни

- **`GetDel` и `SetNX` атомарны на сервере** — используйте их для одноразовых
  state/refresh-токенов и резервов вместо `Get`+`Delete` / `Get`+`Set`.
- **Набор тега живёт `ttl` после последней записи.** Члены с разными TTL могут
  истечь раньше набора — `InvalidateByTag` просто удалит уже отсутствующие ключи.
- **Служебные префиксы:** `tag:` (наборы тегов) и `idempotency:`; не используйте
  их для своих ключей.
- **`Close` в `OnStop`, `Ping` в `OnStart`.** Без fx закрывайте кэш сами;
  `REDIS_PING_ON_START=false` только для окружений, где Redis поднимается позже
  сервиса.
