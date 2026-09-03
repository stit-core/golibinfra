# testkit

Тестовый инструментарий golibinfra: testcontainers-обёртки для интеграционных
тестов (Postgres / NATS / Redis) и per-kit test-helper'ы (fake-реализации
интерфейсов китов, recorder'ы, helper'ы для huma-хендлеров). Импортируется только
из `*_test.go` консьюмеров — в production-граф не попадает.

## Назначение

Сервис, собранный на китах golibinfra, тестируется на их интерфейсах. testkit
закрывает две задачи:

- **Интеграционный слой.** `StartPostgres` / `StartNATS` / `StartRedis` поднимают
  реальный контейнер на свободном порту и регистрируют `t.Cleanup` для остановки.
  Тест получает DSN/URL живого инстанса — без моков самой БД/брокера.
- **Юнит-слой.** Сабпакеты дают детерминированные fake-реализации интерфейсов
  китов (`auditkit.Logger`, `authnkit.Hasher`, `bruteforcekit.Guard`,
  `outboxkit.Publisher`, `pgkit.Transactor`) и recorder'ы, чтобы проверять
  бизнес-логику без поднятия инфраструктуры, а также helper'ы для тестов
  huma-хендлеров (`handlertest`).

## Импорт

Корневые testcontainers-helper'ы:

```go
import "git.stit.tech/stit-core/golibinfra/testkit"
```

Сабпакет (на примере `eventbus`):

```go
import "git.stit.tech/stit-core/golibinfra/testkit/eventbus"
```

Версию модуля и настройку `GOPRIVATE` / `go get` не дублируем — см.
[корневой README](../README.md).

## Быстрый старт

Контейнерные helper'ы принимают `*testing.T`, при ошибке вызывают `t.Fatalf`, а
остановку контейнера вешают на `t.Cleanup` — вручную ничего закрывать не нужно.

```go
func TestRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs Docker")
	}

	pg := testkit.StartPostgres(t,
		testkit.WithMigrations(auditpg.Migrations),   // миграции китов — прямо из embed.FS
		testkit.WithMigrationsDir("testdata/migrations"), // миграции приложения
	)
	db := pg.DB(t) // *pgkit.DB, пул закроется в t.Cleanup
	_ = pg.DSN     // DSN на случай прямого подключения

	nats := testkit.StartNATS(t, testkit.WithJetStream()) // без опции — core NATS
	_ = nats.URL // nats://host:port

	rdb := testkit.StartRedis(t)
	_ = rdb.URL // redis://host:port

	// ... тело теста на db / nats.URL / rdb.URL
}
```

Сигнатуры:

```go
func StartPostgres(t *testing.T, opts ...PostgresOption) *PostgresFixture
func WithMigrations(sources ...pgkit.Source) PostgresOption
func WithMigrationsDir(path string) PostgresOption
func (f *PostgresFixture) DB(t *testing.T) *pgkit.DB

func StartNATS(t *testing.T, opts ...NATSOption) *NATSFixture
func WithJetStream() NATSOption

func StartRedis(t *testing.T) *RedisFixture
```

`StartPostgres` применяет переданные источники через `pgkit.MigrateUpAll` — тем
же путём, что `pgkit.MigrateModule` в приложении (таблица версий на источник);
без опций миграции не прогоняются. `PostgresFixture.DB` открывает `pgxpool`,
оборачивает в `pgkit.NewDB` и закрывает пул через `t.Cleanup`. `WithJetStream`
запускает `nats-server -js` — нужен тестам `natskit.StreamBus` и `outboxkit`.

Юнит-helper из сабпакета — fake без контейнера:

```go
func TestPublishesEvent(t *testing.T) {
	bus := &eventbus.Recorder{} // реализует outboxkit.Publisher
	svc := NewService(bus)

	_ = svc.CreateUser(ctx, "u1")

	if bus.Count() != 1 {
		t.Fatalf("want 1 event, got %d", bus.Count())
	}
	if bus.Last("user.created") == nil {
		t.Fatal("user.created not published")
	}
}
```

## Конфигурация

Конфиг-структур (envconfig) у testkit нет — поведение задаётся аргументами
helper'ов. Образы и порты контейнеров зашиты в helper'ах: `testkit.PostgresImage`
= `postgres:17-alpine` (база/пользователь/пароль `test`), `testkit.NATSImage` =
`nats:2.11-alpine` (`4222/tcp`), `redis:7-alpine` (`6379/tcp`). Каждый helper
ждёт готовности по логам контейнера с таймаутом старта 60 секунд.

Требования к окружению для контейнерных helper'ов:

- доступный Docker-демон (или совместимый рантайм), к которому подключается
  [`testcontainers-go`](https://github.com/testcontainers/testcontainers-go);
- возможность тянуть указанные образы (локальный кэш либо доступ к registry).

Сабпакеты Docker не требуют — это чистый Go без внешних зависимостей рантайма.

## Сабпакеты

Соглашение об именах: `<kit без суффикса kit>test` (например, `auditkit` →
`audittest`). Исключения — `eventbus`, `handlertest`, `txkit`, имена которых
отражают обслуживаемый примитив, а не имя кита.

| Сабпакет | Обслуживает | Что даёт |
| --- | --- | --- |
| [`audittest`](audittest/) | `auditkit` | `Recorder` — in-memory `auditkit.Logger`; запросы `Entries`/`Actions`/`Has`/`Last`/`ByResource` + `Reset`. |
| [`authntest`](authntest/) | `authnkit` | `DeterministicHasher` — `authnkit.Hasher` без Argon2 (хеш = `"hash:"+plain`); `HashPrefix`, `StripHashPrefix`. |
| [`bruteforcetest`](bruteforcetest/) | `bruteforcekit` | `bruteforcekit.Guard`-фейки: `Permissive` (всегда пропускает), `Locked{Until}` (всегда заблокирован), `Recorder` (копит `Failures`/`Successes`). |
| [`eventbus`](eventbus/) | `outboxkit` | `Recorder` — in-memory `outboxkit.Publisher`; запросы `All`/`Published`/`Last`/`Count` + `Reset`. |
| [`handlertest`](handlertest/) | `httpkit` (huma) | `NewAPI` поднимает `humatest.TestAPI`; `DecodeAPIError`, `AssertStatus`, `AssertAPIError` для проверки ответов хендлеров. |
| [`txkit`](txkit/) | `pgkit` | `pgkit.Transactor`-фейки (`WithTx` и `WithTxOptions`): `Identity` (прогоняет `fn` без транзакции), `Recorder` (то же + счётчик `Calls`). |

## Observability

testkit ничего не эмитит: ни метрик, ни спанов, ни логов через `logkit`.
Контейнерные helper'ы и fake'и сообщают о проблемах только через `t.Fatalf` /
возвращаемые значения. Это тестовый код — он не часть рантайма сервиса.

## API

Корневой пакет `testkit`:

| Символ | Назначение |
| --- | --- |
| `StartPostgres(t, opts...) *PostgresFixture` | Контейнер `postgres:17-alpine`; опции применяют миграции. |
| `WithMigrations(sources...)` / `WithMigrationsDir(path)` | Источники миграций (`pgkit.Source`) / каталог приложения (источник `pgkit.AppSourceName`). |
| `PostgresFixture{DSN, Container}` | DSN живого инстанса + handle контейнера (`tcpostgres.PostgresContainer`). |
| `(*PostgresFixture).DB(t) *pgkit.DB` | Пул `pgxpool` поверх `DSN`, обёрнутый в `pgkit.NewDB`; закрывается в `t.Cleanup`. |
| `StartNATS(t, opts...) *NATSFixture` | Контейнер `nats:2.11-alpine`; `NATSFixture{URL, Container}`. |
| `WithJetStream()` | Запуск сервера с `-js`. |
| `StartRedis(t) *RedisFixture` | Контейнер `redis:7-alpine`; `RedisFixture{URL, Container}`. |
| `PostgresImage`, `NATSImage` | Константы образов — для собственных `ContainerRequest`. |

`Container` в каждой fixture доступен напрямую — для дополнительных операций над
контейнером поверх готовых helper'ов.

Соглашение об именовании сабпакетов — `<kit без суффикса kit>test`; перечень и
содержимое — в разделе [Сабпакеты](#сабпакеты).

## Тестирование

Helper'ы вызываются прямо из `*_test.go` консьюмера. Контейнерные тесты дороги и
требуют Docker, поэтому их гейтят `testing.Short()`:

```go
if testing.Short() {
	t.Skip("integration: needs Docker")
}
```

- `go test -short ./...` — пропускает тесты, которые сами вызвали `t.Skip` под
  `testing.Short()`; контейнеры не поднимаются.
- `go test ./...` — полный прогон, включая контейнерные тесты (нужен Docker).

testkit сам по себе условие `-short` не проверяет — за `t.Skip` отвечает тест
консьюмера. Сабпакетные fake'и Docker не требуют и под `-short` отрабатывают как
обычные unit-тесты.

## Инварианты и подводные камни

- **Docker обязателен** для `StartPostgres`/`StartNATS`/`StartRedis`. Без рантайма
  helper падает на `t.Fatalf` ещё на старте контейнера.
- **`-short` — ответственность теста.** testkit не знает о флаге; гейтьте
  контейнерные тесты через `testing.Short()` в самом тесте, иначе они уйдут в
  Docker и на unit-проходе CI.
- **Cleanup — через `t.Cleanup`, не закрывайте вручную.** Контейнеры и пул
  `PostgresFixture.DB` снимаются автоматически по завершении теста; ручной
  `Terminate`/`Close` не нужен и приведёт к двойному закрытию.
- **testkit — только для тестов.** Не импортируйте его и сабпакеты в
  production-граф (`fx.Module` сервиса): он тянет testcontainers и `testing` и
  существует ради проверки логики на интерфейсах китов.
- **Fake'и проверяются на соответствие интерфейсу через `var _`.** Каждый fake
  закреплён за конкретным интерфейсом кита (`var _ auditkit.Logger =
  (*audittest.Recorder)(nil)` и аналоги) — при изменении интерфейса кита fake
  перестанет компилироваться, что и есть сигнал обновить helper.
- **Образы и порты зашиты, теги — конкретные.** Версии (`postgres:17-alpine`,
  `nats:2.11-alpine`, `redis:7-alpine`) и таймаут старта (60s) — внутри
  helper'ов; `:latest` не используется, чтобы тесты не менялись от обновления
  registry. Другой образ — свой `ContainerRequest` поверх констант.
