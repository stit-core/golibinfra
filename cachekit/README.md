# cachekit

Обёртка над Redis для сервисов stit. Интерфейс `Cache` — байтовый key-value с
TTL, атомарные `SetNX` (резерв/lock), `GetDel` (одноразовые токены),
`CompareAndDelete` (снятие собственного резерва) и `CompareAndSet` (запись поверх
собственного резерва), тегированная инвалидация, `Ping`/`Close`. Поверх него
типизированные `GetJSON[T]`/`SetJSON[T]`, `IdempotencyStore` для
`httpkit.Idempotency`, `MemoryCache` и health-проба для `/ready`.

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
    cachekit.Module,                            // Cache на старте (и без потребителя): Ping, Close на стопе
    healthkit.ProvideProbe(cachekit.Probe),          // /ready через httpkit

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

`Module` строит `Cache` на старте и без потребителя: невалидный конфиг валит сборку
графа, недоступный Redis (при `REDIS_PING_ON_START`) — старт. Логгер — `logkit.Logger`
графа.

Без fx: `cachekit.NewRedisCache(cfg, log)`; закрывать `Close()` самому, версию сервера
сверяет только `Module`.

## Конфигурация

`cachekit.Config` — public API (корневой `README.md` § Стабильность API).
`NewConfig` и `NewRedisCache` вызывают `Config.Validate()`: невалидное значение
валит старт с `cachekit.ErrConfig`, а не первую команду.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `REDIS_URL` | `URL` | `string` | `redis://localhost:6379/0` | Адрес Redis (`redis.ParseURL`); должен парситься, ошибка разбора — с паролем `xxxxx` (всё до последнего `@`, и у пароля с `#`, `?`, `/`) и без цитаты ввода |
| `REDIS_KEY_PREFIX` | `KeyPrefix` | `string` | `""` | Префикс **всех** ключей кита, включая `tag:` и `idempotency:` |
| `REDIS_POOL_SIZE` | `PoolSize` | `int` | `10` | Размер пула соединений; `>= 1`. Пулов два — с повторами и без (§ Инварианты): соединений до `2×REDIS_POOL_SIZE` |
| `REDIS_MAX_RETRIES` | `MaxRetries` | `int` | `3` | Повторы `Get`, `Set`, `Delete`, тегов и `Ping` после сетевой ошибки; `-1` отключает, `>= -1`. `SetNX`, `GetDel`, `CompareAndDelete`, `CompareAndSet` не повторяются |
| `REDIS_DIAL_TIMEOUT` | `DialTimeout` | `time.Duration` | `5s` | Таймаут установления соединения; `>= 0`, `0` — дефолт go-redis (5s) |
| `REDIS_READ_TIMEOUT` | `ReadTimeout` | `time.Duration` | `3s` | Верхняя граница ожидания ответа; `>= 0`, `0` — дефолт go-redis (5s) |
| `REDIS_WRITE_TIMEOUT` | `WriteTimeout` | `time.Duration` | `3s` | Верхняя граница записи команды; `>= 0`, `0` — как `ReadTimeout` |
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
| спан `redis <cmd>` | Client span | `db.system.name=redis`, `db.operation.name` (= `cmd`), `db.operation.batch.size` (пайплайн от двух команд) — semconv v1.41, как у natskit и httpclient | Латентность Redis внутри трейса запроса; статус `Error` на ошибке, `redis.Nil` — не ошибка |

`cmd` — имя команды go-redis: `get`, `set` (и `Set`, и `SetNX`), `getdel`,
`unlink` (`Delete`), `evalsha`/`eval` (`SetWithTags`, `CompareAndDelete`,
`CompareAndSet`, `InvalidateByTag` — `SPOP`+`UNLINK` внутри скрипта отдельными
командами не видны), `ping`, `info` (проверка версии на старте),
`hello`/`pipeline` при открытии соединения (уведомления о maintenance go-redis выключены:
исправный Redis не даёт `result=error` на рукопожатии). Первый `evalsha` на новом сервере отвечает `NOSCRIPT`
(`result=error`) — go-redis тут же загружает скрипт через `eval`; на алертах это
единичный datapoint на инстанс Redis, а не ошибка кита.

Ключи и значения в атрибуты спанов не попадают: PII и неограниченная
кардинальность.

Внутренний лог go-redis (отказы пула, переподключения) — запись `WARN` `redis`
логгера кита с текстом в поле `detail`: JSON, маски и
`trace_id` logkit, а не строка в stderr. Логгер у go-redis один на процесс: из
нескольких кэшей с настоящими логгерами пишет последний собранный, `nil` и
`logkit.Nop()` его не занимают; пока логгера нет, строка идёт в stderr, как у
go-redis без кита.

Health-проба:

```go
func Probe(c Cache) healthkit.Probe    // Redis PING, имя пробы `cachekit`
```

`Module` не кладёт пробу в граф сам — регистрирует потребитель:
`healthkit.ProvideProbe(cachekit.Probe)`.

## API

### `Cache`

```go
type Cache interface {
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
    GetDel(ctx context.Context, key string) ([]byte, bool, error)
    CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
    CompareAndSet(ctx context.Context, key string, expected, value []byte, ttl time.Duration) (bool, error)
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
- `CompareAndSet` — пишет `value` с `ttl`, только если значение ключа совпадает с
  `expected`; `false` без ошибки, если значение другое или ключа нет (ключ не
  создаётся).
- `SetNX`, `GetDel`, `CompareAndDelete`, `CompareAndSet` на сбое связи возвращают
  ошибку, а не повтор (§ Инварианты).
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

func (s *IdempotencyStore) Reserve(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
func (s *IdempotencyStore) Get(ctx context.Context, key string) ([]byte, bool, error)
func (s *IdempotencyStore) Set(ctx context.Context, key, token string, record []byte, ttl time.Duration) (stored bool, err error)
func (s *IdempotencyStore) Release(ctx context.Context, key, token string) error
```

Байтовый store для `httpkit.Idempotency` (структурно совместим с
`httpkit.IdempotencyStore`): `Reserve` — `SETNX` маркера с токеном владельца на
время выполнения запроса, `Get` не отдаёт маркер как запись. `Set` и `Release`
действуют **только по своему токену**: `Set` кладёт готовую запись поверх своего
маркера (`CompareAndSet`, `stored=false` — резерв истёк по `ReserveTTL` или ключ
занял следующий запрос, запись не пишется), `Release` снимает свой маркер
(`CompareAndDelete`). Опоздавший запрос не затирает ни готовую запись, ни резерв
следующего. Контракт — `testkit/idempotencytest`. Ключи живут в namespace
`idempotency:`.

```go
fx.Provide(func(c cachekit.Cache) httpkit.IdempotencyStore { return cachekit.NewIdempotencyStore(c) })
```

### `MemoryCache`

```go
func NewMemoryCache(opts ...MemoryOption) *MemoryCache
func WithClock(clk clockkit.Clock) MemoryOption
func WithMaxKeys(n int) MemoryOption

var ErrMemoryFull error
```

Потокобезопасная in-memory реализация `Cache` с той же семантикой TTL, `SetNX`,
`GetDel`, `CompareAndDelete`, `CompareAndSet` и тегов: Redis и память проходят
один контракт `testkit/cachetest`. Годится для unit-тестов потребителя и для
single-replica развёртывания без Redis (`oidckit` state, `hubkit/issuer` refresh):
истёкшие ключи и наборы тегов вычищаются амортизированным проходом на записи,
поэтому ключ, который никто не читает, не живёт вечно. Данные лежат в процессе —
при рестарте и при второй реплике их нет.

Отличия от Redis: `ctx` не читается (отменённый `ctx` операцию не прерывает),
`Close` — no-op, кэш после него работает, `SetNX` с отрицательным TTL принимается (Redis
отвечает ошибкой `invalid expire time`). Членство в теге переживает ключ, как в
Redis; когда членств становится больше `WithMaxKeys`, проход снимает членство
ключей, которых уже нет (Redis держит его до конца набора), — тег, который никто
не инвалидирует, не растёт без предела.

Ключей — не больше `WithMaxKeys` (по умолчанию 100 000; `n < 1` ничего не
меняет): новый ключ в полный кэш — `ErrMemoryFull` после прохода по истёкшим (у
полного кэша — не чаще раза в секунду), перезапись существующего проходит. Ключи
не вытесняются: резерв или lock не пропадает молча. Предел — ключи, не байты.
Хранилище идемпотентности с `HTTP_IDEMPOTENCY_TTL=24h` заполняет дефолтный предел
при ~1,2 новых ключа в секунду — дальше ошибка store; поднимите `WithMaxKeys` или
возьмите Redis.

`WithClock` подменяет часы: тест двигает TTL через `clockkit.NewTickable` вместо
`time.Sleep`.

## Тестирование

Интеграционные тесты поднимают Redis через `testkit.StartRedis`: в `-short`
пропускаются, без Docker падают; `MemoryCache`, хук наблюдаемости,
`Config.Validate`, `Module` (и без потребителя `Cache`) и работа с закрытым клиентом
покрыты unit-тестами, лог go-redis при отказе пула — записью `WARN` логгера кита.
`Cache` на Redis и на памяти проходит контракт `testkit/cachetest.RunContract`,
`IdempotencyStore` на памяти и на Redis — `testkit/idempotencytest.RunContract`,
тот же, что `httpkit.MemoryStore`.
Отдельные интеграционные проверки: старт на `redis:6-alpine` валится
`ErrServerVersion` (и проходит с `REDIS_SKIP_VERSION_CHECK=true`); инвалидация
тега остаётся одной серверной операцией — хук, отвергающий команду `unlink`, на
неё не влияет; на сервере, занятом долгим скриптом, `SetNX`, `GetDel`,
`CompareAndDelete` и `CompareAndSet` отвечают ошибкой, а не ложным «нет», а `Get`
дожидается ответа повтором.

В тестах потребителя фейком кэша служит `cachekit.NewMemoryCache()` — та же
семантика TTL, тегов, `CompareAndDelete` и `CompareAndSet`, без Docker; часы
подменяются `cachekit.WithClock`. Свою реализацию `Cache` проверяет тот же
`cachetest.RunContract(t, newCache)`.

```bash
go test -short -race ./cachekit/...   # unit
go test -race ./cachekit/...          # + Redis в testcontainers
```

## Инварианты и подводные камни

- **Эффективный таймаут команды — `min(дедлайн ctx, REDIS_READ_TIMEOUT)`.**
  Дедлайн `ctx` доходит до сокета, поэтому HTTP-хендлер с бюджетом 500ms не ждёт
  зависший Redis три секунды. `ctx` без дедлайна ограничен только таймаутами.
- **`SetNX`, `GetDel`, `CompareAndDelete`, `CompareAndSet` не повторяются.** Они
  идут вторым пулом того же сервера без повторов go-redis: ответ, не пришедший за
  таймаут, при повторе увидел бы эффект первой попытки — `SetNX` единственного
  писателя ответил бы «занято», `GetDel` — промахом, хотя значение уже снято.
  Вместо ложного ответа — ошибка; применилась ли первая попытка, неизвестно, как
  у любого таймаута. `Get`, `Set`, `Delete`, теги и `Ping` повторяются
  `REDIS_MAX_RETRIES` раз: их повтор даёт то же состояние.
- **`GetDel`, `SetNX` и `Compare*` атомарны на сервере** — используйте их для
  одноразовых state/refresh-токенов и резервов вместо `Get`+`Delete` /
  `Get`+`Set`.
- **Набор тега живёт не меньше самого долгоживущего члена.** Запись с бо́льшим
  TTL продлевает набор, с меньшим — не укорачивает, запись без TTL делает набор
  персистентным. Члены могут истечь раньше набора — `InvalidateByTag` просто
  удалит уже отсутствующие ключи.
- **Членство в теге переживает ключ.** Набор помнит имя ключа, а не запись:
  `Delete`, `GetDel`, истечение и перезапись без тега членство не снимают — ключ,
  записанный заново простым `Set`, `InvalidateByTag` удалит. Членство уходит с
  инвалидацией или с концом набора; набор с персистентным членом, который никто
  не инвалидирует, копит имена всех своих ключей.
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
