# cachekit

Обёртка над Redis для сервисов stit. Интерфейс `Cache` — байтовый key-value с
TTL, атомарные `SetNX` (резерв/lock), `GetDel` (одноразовые токены) и
`CompareAndDelete` (снятие собственного резерва), тегированная инвалидация,
`Ping`/`Close`. Поверх него типизированные `GetJSON[T]`/`SetJSON[T]`,
`IdempotencyStore` для `httpkit.Idempotency`, `MemoryCache` и health-проба для
`/ready`.

Kit — про доступ к Redis и сериализацию; политику ключей и TTL определяет
потребитель.

Требуется **Redis >= 7.0**: TTL набора тега поддерживается через `PEXPIRE … GT`.
`Module` проверяет версию сервера (`INFO server`) в том же `OnStart`, что и
`PING`, и валит старт с `cachekit.ErrServerVersion`: на 6.x первая запись в новый
тег прошла бы, а вторая упала ошибкой скрипта. Redis-совместимый сервер, который
не отдаёт `redis_version`, — `REDIS_SKIP_VERSION_CHECK=true`. Cluster-режим кит
не поддерживает: инвалидация тега удаляет ключи, не объявленные в `KEYS`.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/cachekit"
```

## Быстрый старт

```go
type Profile struct{ Name string }

fx.New(
    fx.Provide(logkit.NewConfig, logkit.New),   // logkit.Logger в графе
    fx.Provide(cachekit.NewConfig),             // REDIS_* из ENV (caller провайдит явно)
    cachekit.Module,                            // Cache: Ping на старте, Close на стопе
    healthkit.Provide(cachekit.Probe),          // /ready через httpkit

    fx.Invoke(func(lc fx.Lifecycle, c cachekit.Cache) {
        lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
            if err := cachekit.SetJSON(ctx, c, "user:42", Profile{Name: "alice"}, 5*time.Minute); err != nil {
                return err
            }
            if _, found, err := cachekit.GetJSON[Profile](ctx, c, "user:42"); err != nil || !found {
                return err // промах: found == false, err == nil
            }
            if err := c.SetWithTags(ctx, "user:42:orders", []byte("[]"), time.Hour, "user:42"); err != nil {
                return err
            }
            return c.InvalidateByTag(ctx, "user:42")
        }})
    }),
)
```

Без fx: `cachekit.NewRedisCache(cfg)`; закрывать `Close()` самому.

## Конфигурация

`cachekit.Config` — public API (корневой `README.md` § Стабильность API).
`NewConfig` и `NewRedisCache` вызывают `Config.Validate()`: невалидное значение
валит старт с `cachekit.ErrConfig`, а не первую команду.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `REDIS_URL` | `URL` | `string` | `redis://localhost:6379/0` | Адрес Redis (`redis.ParseURL`); должен парситься |
| `REDIS_KEY_PREFIX` | `KeyPrefix` | `string` | пусто | Префикс **всех** ключей кита, включая `tag:` и `idempotency:` |
| `REDIS_POOL_SIZE` | `PoolSize` | `int` | `10` | Размер пула соединений; `>= 1` |
| `REDIS_MAX_RETRIES` | `MaxRetries` | `int` | `3` | Повторы команды после сетевой ошибки; `-1` отключает, `>= -1` |
| `REDIS_DIAL_TIMEOUT` | `DialTimeout` | `time.Duration` | `5s` | Таймаут установления соединения; `>= 0` |
| `REDIS_READ_TIMEOUT` | `ReadTimeout` | `time.Duration` | `3s` | Верхняя граница ожидания ответа; `>= 0` |
| `REDIS_WRITE_TIMEOUT` | `WriteTimeout` | `time.Duration` | `3s` | Верхняя граница записи команды; `>= 0` |
| `REDIS_PING_ON_START` | `PingOnStart` | `bool` | `true` | `Module` делает `PING` и проверку версии в `OnStart`: недоступный Redis валит старт, а не первый запрос |
| `REDIS_SKIP_VERSION_CHECK` | `SkipVersionCheck` | `bool` | `false` | Снимает проверку `redis_version >= 7`; для redis-совместимых серверов, не отдающих это поле в `INFO` |

**Query-опции `REDIS_URL`.** `redis.ParseURL` понимает часть настроек прямо в
адресе, но `pool_size`, `max_retries`, `dial_timeout`, `read_timeout`,
`write_timeout` из URL перекрываются одноимёнными `REDIS_*` (у них всегда есть
дефолт). Остальные опции go-redis (`protocol`, `client_name`, `min_idle_conns`,
`pool_timeout`, `conn_max_idle_time`, `skip_verify`) применяются как есть.
`skip_verify=true` отключает проверку TLS-сертификата — только dev.

**Namespace.** Служебные ключи (`tag:`, `idempotency:`) фиксированы, поэтому два
сервиса на одной БД Redis делят их пространство. Изолируйте сервисы: свой
`REDIS_KEY_PREFIX`, своя БД (`redis://host:6379/N`) или свой Redis.

## Observability

Instrumentation-scope `git.stit.tech/stit-core/golibinfra/cachekit`, глобальные
`otel.Meter`/`otel.Tracer` (no-op без провайдера). Хук go-redis наблюдает каждую
команду:

| Сигнал | Тип | Labels | Назначение |
|---|---|---|---|
| `cache_commands_total` | Int64 Counter | `cmd`, `result` | Команды по имени; `result` — `ok`, `miss` (`redis.Nil`), `error` |
| `cache_command_duration_seconds` | Float64 Histogram (`s`) | `cmd` | Round-trip команды; общие бакеты длительности |
| спан `redis <cmd>` | Client span | `db.system=redis`, `db.operation`, `db.redis.batch_size` (пайплайн) | Латентность Redis внутри трейса запроса; статус `Error` на ошибке, `redis.Nil` — не ошибка |

`cmd` — имя команды go-redis: `get`, `set` (и `Set`, и `SetNX`), `getdel`,
`unlink` (`Delete`), `evalsha`/`eval` (`SetWithTags`, `CompareAndDelete`,
`InvalidateByTag` — `SPOP`+`UNLINK` внутри скрипта отдельными командами не
видны), `ping`, `info` (проверка версии на старте),
`hello`/`client`/`pipeline` при открытии соединения. Первый `evalsha` на новом сервере отвечает `NOSCRIPT`
(`result=error`) — go-redis тут же загружает скрипт через `eval`; на алертах это
единичный datapoint на инстанс Redis, а не ошибка кита.

Ключи и значения в атрибуты спанов не попадают: PII и неограниченная
кардинальность.

Health-проба:

```go
func Probe(c Cache) healthkit.Probe    // Redis PING, имя пробы `cachekit`
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
    CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
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
- `CompareAndDelete` — удаляет ключ, только если его значение совпадает с
  `expected`; `false` без ошибки, если значение другое или ключа нет.
- `SetWithTags` — `SET` + `SADD tag:<tag> key` одним атомарным скриптом; набор
  тега живёт не меньше самого долгоживущего своего члена.
- `InvalidateByTag` — порциями по 256 членов, `SPOP` + `UNLINK` одним скриптом;
  отсутствующий тег не ошибка.
- `Close` идемпотентен: повторный вызов возвращает `nil`.

### `GetJSON[T]` / `SetJSON[T]`

`encoding/json` поверх `Get`/`Set`. Промах — `(zero, false, nil)`; битое
значение — `(zero, false, error)`: испорченная запись отличается от промаха,
чтобы потребитель мог её залогировать, а не молча перезаписать.

### `IdempotencyStore`

```go
func NewIdempotencyStore(c Cache) *IdempotencyStore

func (s *IdempotencyStore) Reserve(ctx, key string, ttl time.Duration) (token string, ok bool, err error)
func (s *IdempotencyStore) Get(ctx, key string) ([]byte, bool, error)
func (s *IdempotencyStore) Set(ctx, key string, record []byte, ttl time.Duration) error
func (s *IdempotencyStore) Release(ctx, key, token string) error
```

Байтовый store для `httpkit.Idempotency` (структурно совместим с
`httpkit.IdempotencyStore`): `Reserve` — `SETNX` маркера с токеном владельца на
время выполнения запроса, `Set` — готовая запись, `Get` не отдаёт маркер как
запись. `Release` снимает резерв **только по своему токену** (`CompareAndDelete`):
ни готовая запись, ни резерв следующего запроса (предыдущий истёк по
`ReserveTTL`) опоздавшим `Release` не сносятся. Ключи живут в namespace
`idempotency:`.

```go
fx.Provide(func(c cachekit.Cache) httpkit.IdempotencyStore { return cachekit.NewIdempotencyStore(c) })
```

### `MemoryCache`

```go
func NewMemoryCache(opts ...MemoryOption) *MemoryCache
func WithClock(clk clockkit.Clock) MemoryOption
```

Потокобезопасная in-memory реализация `Cache` с той же семантикой TTL, `SetNX`,
`GetDel`, `CompareAndDelete` и тегов. Годится для unit-тестов потребителя и для
single-replica развёртывания без Redis (`oidckit` state, `hubkit/issuer`
refresh): истёкшие ключи и членство в тегах вычищаются амортизированным
проходом на записи, поэтому ключ, который никто не читает, не живёт вечно.
Данные лежат в процессе — при рестарте и при второй реплике их нет.

`WithClock` подменяет часы: тест двигает TTL через `clockkit.NewTickable` вместо
`time.Sleep`.

## Тестирование

Интеграционные тесты поднимают Redis через `testkit.StartRedis`: в `-short`
пропускаются, без Docker падают; `MemoryCache`, `IdempotencyStore`, хук наблюдаемости,
`Config.Validate`, `Module` и работа с закрытым клиентом покрыты unit-тестами.
Отдельные интеграционные проверки: старт на `redis:6-alpine` валится
`ErrServerVersion` (и проходит с `REDIS_SKIP_VERSION_CHECK=true`), а инвалидация
тега остаётся одной серверной операцией — хук, отвергающий команду `unlink`, на
неё не влияет.

В тестах потребителя фейком кэша служит `cachekit.NewMemoryCache()` — та же
семантика TTL, тегов и `CompareAndDelete`, без Docker; часы подменяются
`cachekit.WithClock`.

```bash
go test -short -race ./cachekit/...   # unit
go test -race ./cachekit/...          # + Redis в testcontainers
```

## Инварианты и подводные камни

- **Эффективный таймаут команды — `min(дедлайн ctx, REDIS_READ_TIMEOUT)`.**
  Дедлайн `ctx` доходит до сокета, поэтому HTTP-хендлер с бюджетом 500ms не ждёт
  зависший Redis три секунды. `ctx` без дедлайна ограничен только таймаутами.
- **После сетевого таймаута команда может быть повторена.** При
  `REDIS_MAX_RETRIES > 0` go-redis шлёт команду заново, поэтому `SetNX` может
  вернуть `false`, а `GetDel` — промах, хотя первая попытка уже применилась на
  сервере (atomic на сервере, at-least-once на клиенте). Где это неприемлемо
  (резерв идемпотентности, одноразовый refresh-токен), ставьте
  `REDIS_MAX_RETRIES=-1` и обрабатывайте ошибку ретраем на своём уровне.
- **`GetDel` и `SetNX` атомарны на сервере** — используйте их для одноразовых
  state/refresh-токенов и резервов вместо `Get`+`Delete` / `Get`+`Set`.
- **Набор тега живёт не меньше самого долгоживущего члена.** Запись с бо́льшим
  TTL продлевает набор, с меньшим — не укорачивает, запись без TTL делает набор
  персистентным. Члены могут истечь раньше набора — `InvalidateByTag` просто
  удалит уже отсутствующие ключи.
- **Инвалидация не теряет конкурентные записи.** `SPOP` снимает членство до
  удаления ключей: ключ, добавленный в тег во время инвалидации, либо попадёт в
  текущую порцию, либо останется членом набора и уйдёт при следующем вызове.
  Обе команды — один скрипт: сетевой сбой между ними иначе оставил бы ключи в
  Redis вне какого-либо тега, и повторная инвалидация их бы не нашла.
- **Служебные префиксы:** `tag:` (наборы тегов) и `idempotency:`; не используйте
  их для своих ключей. `REDIS_KEY_PREFIX` добавляется поверх них.
- **`Close` в `OnStop`, `Ping` и проверка версии в `OnStart`.** Без fx закрывайте
  кэш сами; `REDIS_PING_ON_START=false` только для окружений, где Redis
  поднимается позже сервиса, — вместе с `PING` оно снимает и проверку версии.
