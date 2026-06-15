# cachekit

Тонкая обёртка над Redis для сервисов stit. Интерфейс `Cache` даёт
байтовый key-value доступ (`Get`/`Set`/`Delete`/`Ping`) поверх
[`go-redis/v9`](https://github.com/redis/go-redis); типизированные helper'ы
`GetJSON[T]`/`SetJSON[T]` сериализуют значения через `encoding/json`. Поверх
этого — `InvalidateByTag` (групповая инвалидация по Redis Set с тегом) и
`IdempotencyStore` — адаптер, структурно совместимый с `httpkit.IdempotencyStore`
для idempotency-middleware.

Kit — про доступ к Redis и сериализацию; политику ключей, TTL и тегирование
определяет потребитель.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/cachekit"
```

Версия модуля — см. [`../README.md`](../README.md) (§ Версионирование).

## Быстрый старт

fx-проводка. `Module` предоставляет `Cache`; `Config` потребитель провайдит сам
(`cachekit.NewConfig` читает `REDIS_*` из ENV):

```go
fx.New(
    fx.Provide(cachekit.NewConfig), // REDIS_* конфиг (caller провайдит явно)
    cachekit.Module,                // Cache (Redis-backed)

    fx.Invoke(func(c cachekit.Cache) {
        ctx := context.Background()

        type Profile struct {
            Name string `json:"name"`
            Age  int    `json:"age"`
        }

        // Запись с TTL.
        _ = cachekit.SetJSON(ctx, c, "user:42", Profile{Name: "alice", Age: 30}, 5*time.Minute)

        // Чтение: (value, found, error) — miss это found=false, не ошибка.
        p, found, err := cachekit.GetJSON[Profile](ctx, c, "user:42")
        _, _, _ = p, found, err

        // Групповая инвалидация по тегу.
        _ = c.InvalidateByTag(ctx, "user:42:deps")
    }),
)
```

Сигнатуры дженериков (`c cachekit.Cache` передаётся явным аргументом — методами
типа они быть не могут):

```go
func GetJSON[T any](ctx context.Context, c Cache, key string) (T, bool, error)
func SetJSON[T any](ctx context.Context, c Cache, key string, value T, ttl time.Duration) error
```

`GetJSON` на miss возвращает нулевое значение `T`, `found == false`, `err == nil`.

## Конфигурация

`cachekit.Config` — **part of public API** (см. `../README.md` § Стабильность
API). Имена полей и их envconfig-теги менять = breaking change, идёт мажорным
апгрейдом.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `REDIS_URL` | `URL` | `string` | `redis://localhost:6379/0` | Адрес Redis (cache + idempotency store); парсится через `redis.ParseURL` |
| `REDIS_POOL_SIZE` | `PoolSize` | `int` | `10` | Размер пула соединений |
| `REDIS_DIAL_TIMEOUT` | `DialTimeout` | `time.Duration` | `5s` | Таймаут установления соединения |
| `REDIS_READ_TIMEOUT` | `ReadTimeout` | `time.Duration` | `3s` | Таймаут чтения команды |
| `REDIS_WRITE_TIMEOUT` | `WriteTimeout` | `time.Duration` | `3s` | Таймаут записи команды |

`REDIS_URL` — per-concern endpoint: разное значение = разный хост, Redis можно
развести на отдельный сервер, не трогая код (см. `../README.md` § Per-concern
endpoints). DB и параметры из URL (например `redis://host:6379/0`) применяются как
есть; `PoolSize`/`DialTimeout`/`ReadTimeout`/`WriteTimeout` накладываются поверх
разобранного URL.

## Observability

`cachekit` **ничего не эмитит** — собственных метрик и спанов у kit'а нет, в
`../README.md` § Что эмитит каждый пакет он отсутствует осознанно. Трейсинг
команд Redis на уровне kit'а не инструментирован.

Health-probe есть, но как функция, а не auto-wired метрика:

```go
func Probe(c Cache) func(ctx context.Context) error
```

`Module` намеренно **не** provide'ит `Probe`: его тип `func(context.Context) error`
коллизит с другими health-probe (например `natskit.Probe`). Регистрирует
потребитель — обычно в health-группу:

```go
fx.Provide(fx.Annotate(cachekit.Probe, fx.ResultTags(`group:"health"`)))
```

`Probe` под капотом дёргает `Cache.Ping` (Redis `PING`).

## API

### `Cache`

```go
type Cache interface {
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    Delete(ctx context.Context, keys ...string) error
    InvalidateByTag(ctx context.Context, tag string) error
    Ping(ctx context.Context) error
}
```

- `Get` — `(value, found, error)`. Промах (`redis.Nil`) это `found == false`,
  `err == nil`, не ошибка.
- `Set` — записывает байты с `ttl`. `ttl == 0` означает ключ без срока жизни
  (поведение Redis `SET` без `EX`).
- `Delete` — переменное число ключей; пустой вызов (`len == 0`) — no-op, без
  обращения к Redis.
- `Ping` — health-check соединения.

Конструктор без fx:

```go
func NewRedisCache(cfg Config) (Cache, error)
```

### `GetJSON[T]` / `SetJSON[T]`

`SetJSON` маршалит `value` через `json.Marshal` и кладёт байты через `Cache.Set`.
`GetJSON` читает байты и анмаршалит в `T`; ошибка `json.Unmarshal` возвращается с
`found == false`.

### `InvalidateByTag`

```go
func (c Cache) InvalidateByTag(ctx context.Context, tag string) error
```

Групповая инвалидация: kit ожидает, что под ключом `tag:<tag>` лежит Redis Set с
именами ключей, относящихся к тегу. `InvalidateByTag` читает членов набора
(`SMEMBERS tag:<tag>`), удаляет их (`DEL`) и затем удаляет сам tag-ключ.
Отсутствующий набор (`redis.Nil`) — не ошибка. Наполнение набора (`SADD`) —
ответственность потребителя; kit его не пишет.

### `IdempotencyStore`

```go
func IdempotencyStore(cache Cache, ttl time.Duration) interface {
    Get(ctx context.Context, key string) ([]byte, int, bool, error)
    Set(ctx context.Context, key string, response []byte, status int) error
}
```

Возвращает Redis-backed store, структурно удовлетворяющий интерфейсу
`httpkit.IdempotencyStore`. Хранит JSON-запись `{response, status}` под ключом
`idempotency:<key>` c переданным `ttl`. Подключение в `httpkit` — через
`optional`-зависимость `IdempotencyStore` (см. `httpkit/README.md` и
`../README.md` § Per-concern endpoints, строка `REDIS_URL`). Пример проводки:

```go
fx.Provide(func(c cachekit.Cache) httpkit.IdempotencyStore {
    return cachekit.IdempotencyStore(c, 24*time.Hour)
})
```

Когда store подан в граф, `httpkit` навешивает idempotency-middleware и эмитит
`http_idempotency_total` (метрика принадлежит `httpkit`, не `cachekit`).

## Тестирование

Интеграционные тесты поднимают реальный Redis через
[`testkit.StartRedis`](../testkit/) (testcontainers, образ `redis:7-alpine`).
Тесты пропускаются в `-short` (они не unit'ы):

```go
func TestCache(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test in short mode")
    }
    fix := testkit.StartRedis(t) // *RedisFixture{URL, Container}; контейнер терминируется в t.Cleanup
    cache, err := cachekit.NewRedisCache(cachekit.Config{
        URL:         fix.URL,
        PoolSize:    5,
        DialTimeout: 5 * time.Second,
        ReadTimeout: 3 * time.Second,
    })
    // ...
}
```

`Cache` — интерфейс, так что для unit-тестов потребителя его легко подменить
in-memory фейком (реализовать пять методов поверх `map[string][]byte`); реальный
Redis при этом не нужен.

## Инварианты и подводные камни

- **TTL.** `Set`/`SetJSON` пишут с TTL, который передан явно; `ttl == 0` = ключ
  без срока жизни. Срок — целиком на потребителе, дефолтного TTL kit не навязывает.
- **Tag-инвалидация — ручная.** `InvalidateByTag` только *читает и удаляет* набор
  `tag:<tag>`. Наполнять его (`SADD tag:<tag> <key>` при записи) должен сам
  потребитель — иначе инвалидировать будет нечего. Записи в наборе не привязаны к
  TTL значений: ключ может истечь по своему TTL, а его имя остаться в наборе до
  ближайшего `InvalidateByTag`.
- **Miss это не ошибка.** Во всех путях чтения промах — `found == false`,
  `err == nil`. Не путайте `!found` с ошибкой.
- **Сериализация — `encoding/json`.** `GetJSON[T]`/`SetJSON[T]` и
  `IdempotencyStore` гоняют значения через stdlib `json`. Применяются обычные
  правила тегов (`json:"..."`) и ограничения JSON (например, `map` с не-string
  ключами не сериализуется). Несовместимая по форме запись в Redis приведёт к
  ошибке `unmarshal` на чтении.
- **Префиксы ключей служебные.** `InvalidateByTag` владеет namespace `tag:`,
  `IdempotencyStore` — `idempotency:`. Не используйте эти префиксы для своих
  ключей, чтобы не пересечься с групповой инвалидацией и idempotency-записями.
- **`IdempotencyStore` ↔ `httpkit`.** Store возвращается как анонимный интерфейс,
  совместимый с `httpkit.IdempotencyStore` структурно (по сигнатурам методов), а
  не через явный импорт `httpkit`. Метрику idempotency (`http_idempotency_total`)
  эмитит `httpkit`, store лишь хранит response/status.
- **`Module` не provide'ит `Config` и `Probe`.** `Config` потребитель провайдит
  сам (`fx.Provide(cachekit.NewConfig)`); `Probe` регистрируется отдельно с
  health-аннотацией — иначе его тип коллизит с другими probe в графе.
