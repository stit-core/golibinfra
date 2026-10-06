# testkit

Тестовый инструментарий golibinfra: testcontainers-обёртки для интеграционных
тестов (Postgres / NATS / Redis / MinIO) и per-kit test-helper'ы (fake-реализации
интерфейсов китов, recorder'ы, helper'ы для huma-хендлеров и gRPC). Импортируется
только из `*_test.go` консьюмеров — в production-граф не попадает.

## Назначение

Сервис, собранный на китах golibinfra, тестируется на их интерфейсах. testkit
закрывает две задачи:

- **Интеграционный слой.** `StartPostgres` / `StartNATS` / `StartRedis` /
  `StartMinIO` поднимают реальный контейнер на свободном порту и регистрируют
  `t.Cleanup` для остановки. Тест получает DSN/URL живого инстанса — без моков
  самой БД/брокера/хранилища. `MigrationLadder` проверяет обратимость миграций:
  up → down → up по версиям со сверкой схемы.
- **Юнит-слой.** Сабпакеты дают детерминированные fake-реализации интерфейсов
  китов (`auditkit.Logger`, `authnkit.Hasher`, `bruteforcekit.Guard`,
  `outboxkit.Publisher`, `pgkit.Transactor`, `anthropickit.API`,
  `natskit.Bus`/`StreamBus`, `storagekit.Storage`/`Presigner`, `logkit.Logger`,
  `session.Store` сессий `authnkit`), контрактные наборы, которые фейк и боевое
  хранилище проходят одними кейсами (`storagetest`, `sessiontest`),
  проверку записи метрик китов и бизнес-метрик (`metrictest`) и харнессы для
  тестов huma-хендлеров (`handlertest`) и gRPC-сервисов (`grpctest`).

## Импорт

Корневые testcontainers-helper'ы:

```go
import "git.stit.tech/stit-core/golibinfra/testkit"
```

Сабпакет (на примере `outboxtest`):

```go
import "git.stit.tech/stit-core/golibinfra/testkit/outboxtest"
```

Все тестовые сабпакеты живут под `testkit/` — второго корня импорта нет.
Версию модуля и настройку `GOPRIVATE` / `go get` не дублируем — см.
[корневой README](../README.md).

## Быстрый старт

Контейнерные helper'ы принимают `testing.TB`, при ошибке вызывают `t.Fatalf`, а
остановку контейнера вешают на `t.Cleanup` — вручную ничего закрывать не нужно.
Под `-short` они пропускают тест (`t.Skip`), поэтому собственный гейт
`testing.Short()` в тесте не нужен; без доступного Docker вне `-short` тест
падает (`t.Fatalf`), а не пропускается.

```go
func TestRepository(t *testing.T) {
	pg := testkit.StartPostgres(t,
		testkit.WithMigrations(auditpg.Migrations),      // миграции китов — прямо из embed.FS
		testkit.WithMigrationsDir("testdata/migrations"), // миграции приложения
	)
	db := pg.DB(t)         // *pgkit.DB поверх общего пула фикстуры
	tx := pg.Transactor(t) // pgkit.Transactor поверх того же пула
	_ = pg.DSN             // DSN на случай прямого подключения

	nats := testkit.StartNATS(t, testkit.WithJetStream()) // без опции — core NATS
	_ = nats.URL // nats://host:port

	rdb := testkit.StartRedis(t)
	_ = rdb.URL // redis://host:port

	s3 := testkit.StartMinIO(t)
	_ = s3.Endpoint // host:port для minio-go; бакеты создаёт тест

	// ... тело теста на db / tx / nats.URL / rdb.URL / s3
}
```

Сигнатуры:

```go
func RequireDocker(t testing.TB)

func StartPostgres(t testing.TB, opts ...Option) *PostgresFixture
func (f *PostgresFixture) Pool(t testing.TB) *pgxpool.Pool
func (f *PostgresFixture) DB(t testing.TB) *pgkit.DB
func (f *PostgresFixture) Transactor(t testing.TB) pgkit.Transactor
func (f *PostgresFixture) PoolConfig() pgkit.PoolConfig

func StartNATS(t testing.TB, opts ...Option) *NATSFixture
func StartRedis(t testing.TB, opts ...Option) *RedisFixture
func StartMinIO(t testing.TB, opts ...Option) *MinIOFixture

func MigrationLadder(t testing.TB, opts ...Option)

func WithImage(ref string) Option
func WithMigrations(sources ...pgkit.Source) Option
func WithMigrationsDir(path string) Option
func WithJetStream() Option
```

`StartPostgres` применяет переданные источники через `pgkit.MigrateUpAll` — тем
же путём, что `pgkit.MigrateModule` в приложении (таблица версий на источник,
порядок применения = порядок опций); без опций миграции не прогоняются.
`WithJetStream` запускает `nats-server -js` — нужен тестам `natskit.StreamBus` и
`outboxkit`.

`MigrationLadder` — лесенка источников миграций на своём чистом Postgres: каждая
версия — up → down → up, затем весь источник — down до нуля (остаются только
таблицы версий) и снова up. После каждого шага схема из `pg_catalog` сверяется со
снимком: down обязан вернуть её к состоянию до своего up, повторный up —
воспроизвести; при расхождении тест падает с разницей объектов. Источники идут
по порядку опций поверх предыдущих, как в `pgkit.MigrateUpAll`; расширения не
сравниваются (down не обязан удалять общее расширение). Тест на каждый источник —
одна строка:

```go
func TestMigrations_Ladder(t *testing.T) {
	testkit.MigrationLadder(t, testkit.WithMigrations(pgstore.Migrations))
}
```

Юнит-helper из сабпакета — fake без контейнера:

```go
func TestPublishesEvent(t *testing.T) {
	bus := &outboxtest.Recorder{} // outboxkit.Publisher; вне транзакции — outboxkit.ErrNoTx
	svc := NewService(pgtest.Identity{}, bus) // фейк Transactor помечает ctx транзакцией

	if err := svc.CreateUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if bus.Count() != 1 {
		t.Fatalf("want 1 event, got %d", bus.Count())
	}
	if bus.Last("user.created") == nil {
		t.Fatal("user.created not published")
	}
}
```

Метрики — `metrictest`. Путь выбирает то, откуда код под тестом берёт `Meter`:

- **`Meter` передают** (бизнес-метрики сервиса: `Meter` из графа, провайдер
  `telemetry`) — провайдер теста `NewProvider()`. Код получает его `Meter`, снимок
  `p.Collect(t)` видит только записи этого провайдера: без дельт, в параллельных
  подтестах и при повторе прогона (`-count`). Глобальный провайдер `NewProvider` не
  трогает.
- **Код пишет в глобальный провайдер** (киты, `otel.Meter`) — провайдер процесса.
  Его ставит первый `metrictest.Collect`, а не импорт пакета: тест снимает `before`
  до действия и сравнивает дельту `Sub`.

```go
func TestLogin_RecordsOutcome(t *testing.T) {
	t.Parallel()
	p := metrictest.NewProvider()
	svc := NewService(NewMetrics(p.Meter("test"))) // Meter, который сервис получает из графа
	if err := svc.Login(ctx, creds); err != nil {
		t.Fatal(err)
	}
	s := p.Collect(t).Get(t, "biz_auth_login_attempts_total", "result", "ok")
	if s.Kind != metrictest.Counter || s.Value != 1 {
		t.Fatalf("logins: %v (%s), want counter 1", s, s.Kind)
	}
}
```

Boot-тест графа с `telemetry` подменяет `MeterProvider` в `*telemetry.Providers`
провайдером теста (`fx.Decorate`, поле `p.MeterProvider`): метрики доменов попадают
в снимок, только если домены берут `Meter` у провайдера telemetry. Глобальный
провайдер в таком бинаре ставит `telemetry.Init`, а не metrictest, поэтому проверка
«граф поставил глобальный провайдер» остаётся честной; `metrictest.Collect` туда не
зовут — `telemetry.Init` после первого `Collect` подменяет провайдер процесса, и
следующий `Collect` падает.

Серия несёт имя, тип, unit, labels и значение; `Get` — серия с ровно этими labels,
`Find`/`Value` — labels как фильтр, `Collisions` — разные инструменты с одним именем
в Prometheus. Дельта провайдера процесса видит записи всего процесса: такой тест не
идёт параллельно с тестами тех же серий (`t.Parallel`) или разводит их своими labels.
Серии процесса переживают прогон: при повторе (`-count`) серия UpDownCounter прошлого
прогона после `+1 −1` не меняется, и `Sub` её отбрасывает — тест такой серии берёт
labels, уникальные на прогон.

## Конфигурация

Конфиг-структур (envconfig) у testkit нет: поведение задаётся аргументами
helper'ов, ENV-переопределений образов нет тоже. Образ каждого контейнера —
экспортированная константа, а `WithImage(ref)` подменяет её точечно (расширения
вроде pgvector, другая мажорная версия):

| Константа | Значение | Порты |
| --- | --- | --- |
| `PostgresImage` | `postgres:17-alpine` | `5432/tcp`, база/пользователь/пароль `test` |
| `NATSImage` | `nats:2.14.6-alpine` | `4222/tcp` |
| `RedisImage` | `redis:7-alpine` | `6379/tcp` |
| `MinIOImage` | `minio/minio:RELEASE.2025-01-20T14-49-07Z` | креды в `MinIOFixture` |

Готовность контейнера ждётся по логу **и** по слушающему порту, таймаут старта —
60 секунд.

Требования к окружению для контейнерных helper'ов:

- доступный Docker-демон (или совместимый рантайм), к которому подключается
  [`testcontainers-go`](https://github.com/testcontainers/testcontainers-go);
  адрес берётся из `DOCKER_HOST` / `TESTCONTAINERS_HOST_OVERRIDE` по правилам
  testcontainers-go;
- возможность тянуть указанные образы (локальный кэш либо доступ к registry).

Сабпакеты Docker не требуют — это чистый Go без внешних зависимостей рантайма.

Часть фейков живёт в самих китах, а не в testkit, — там, где реализация полезна и
в проде: `cachekit.NewMemoryCache()` закрывает `cachekit.Cache` без Redis,
`storagekit.NewMemoryStorage()` — `storagekit.Storage`/`Presigner` без S3
(`storagetest.RunContract` прогоняет по ним тот же контракт, что по боевому
адаптеру), `clockkit.NewFixed`/`NewTickable` — часы теста.

## Сабпакеты

Соглашение об именах: `<kit без суффикса kit>test` (`auditkit` → `audittest`,
`outboxkit` → `outboxtest`). Исключения обслуживают не один кит: `handlertest` —
huma-хендлеры поверх `httpkit`, `metrictest` — метрики всех китов и бизнес-метрики сервиса.

| Сабпакет | Обслуживает | Что даёт |
| --- | --- | --- |
| [`anthropictest`](anthropictest/) | `anthropickit` | `Stub` — настраиваемый `anthropickit.API` с безопасными дефолтами; `ScriptStream`, `ScriptStreamErr` (обрыв посреди стрима), `ErrStream`. |
| [`audittest`](audittest/) | `auditkit` | `Recorder` — in-memory `auditkit.Logger`: `Log` как pgstore берёт пустые `RequestID` и `IP` из ctx (`Entry.Enrich`), валидирует `Entry` (`auditkit.ErrInvalidEntry`) и хранит копию `Metadata`; запросы `Entries`/`Actions`/`Has`/`Last`/`ByResource` + `Reset`. |
| [`authntest`](authntest/) | `authnkit` | `DeterministicHasher` — `authnkit.Hasher` без Argon2 (хеш = `"hash:"+plain`); `HashPrefix`, `StripHashPrefix`. |
| [`bruteforcetest`](bruteforcetest/) | `bruteforcekit` | `bruteforcekit.Guard`-фейки, все держат контракт pgstore: ключ `(subject, ip)` (`bruteforcekit.ErrInvalidKey`) и `Attempt` вне транзакции (`bruteforcekit.ErrInTx`): `Permissive` (всегда пропускает), `Locked{Until}` (всегда заблокирован), `Recorder` (копит вызовы с парами ключей — `Attempts()`/`Successes()`), `NewCounting(cfg, clock)` (политика pgstore в памяти: «N попыток → lock», попытка под блокировкой ничего не меняет, истечение окна, `RecordSuccess` стирает историю; единственный, кто сворачивает ключ как pgstore — subject в нижнем регистре, IPv6 по `Config.IPv6Prefix`, — и реализует `bruteforcekit.Cleaner`). |
| [`grpctest`](grpctest/) | `grpckit` | `StartServer` — bufconn-харнесс: `grpckit.Server` in-process и `*grpc.ClientConn` к нему. |
| [`handlertest`](handlertest/) | `httpkit` (huma) | `NewAPI` поднимает `humatest.TestAPI` (алиас `API`) поверх API сервера `httpkit` — контракт ошибок как в проде (`request_id`, `Retry-After`, 404/405, коды `ErrorMap.Declare` в OpenAPI), `HumaAuth` до маршрутов (без `WithAuth` защищённая операция — `500 AUTH_NOT_CONFIGURED`), проверка маршрутов старта `Module`, конфиг `httpkit.NewConfig()` (docs, как у сервера, по умолчанию выключены); опции `WithAuth`, `WithConfig`, `WithLogger`, `WithSecuritySchemes`; `DecodeAPIError` → `httpkit.APIError`, `AssertStatus`, `AssertAPIError` (статус, problem+json, `code`, `request_id` = `X-Request-ID`). |
| [`hubtest`](hubtest/) | `hubkit` | `NewIssuer(t)`/`NewCustomerIssuer(t)` — ES256-ключи + JWKS на `httptest`: `EmployeeToken`, `CustomerToken`, `Sign`, `SignHS256`, `Rotate`/`RotateKeepingPrevious`, опции токена (`WithExpiry`, `WithoutClaim`, `WithKID`, …); `SSOConfig`, `Verifier`, `Extractor`, `WithEmployee`/`WithCustomer` для ctx; in-memory порты синка `Feed`/`Projection`/`Cursors`. |
| [`logtest`](logtest/) | `logkit` | `Capture`/`CaptureAt` — `logkit.Logger`, копящий записи в памяти: `Entries`, `Find`, `FindFunc`, `Has`, `Reset`; `Entry.Str`/`Entry.Value` читают поля записи. |
| [`metrictest`](metrictest/) | метрики китов и бизнес-метрики сервиса | `NewProvider` — провайдер теста для кода, которому передают `Meter` (поле `MeterProvider` — подмена провайдера `telemetry`), его `Collect` — снимок только его серий; `Collect` — снимок провайдера процесса, глобального `MeterProvider` китов (ставит первый вызов; подмена после него — падение `Collect`); `Snapshot.Sub` — дельта (gauge — текущее значение), `Get` — серия с ровно этими labels, `Find`/`Value` — labels как фильтр, `All`; `Series{Scope, Name, Kind, Unit, Labels, Value, Count, Bounds}`; `Collisions` и `PromName` — имена после трансляции в Prometheus (`UnderscoreEscapingWithSuffixes` с таблицами единиц otlptranslator; совпадение с рядами гистограммы `_count`, `_sum`, `_bucket` `Collisions` не ищет). |
| [`natstest`](natstest/) | `natskit` | `Recorder` — in-memory `natskit.Bus`/`StreamBus` с синхронной доставкой подписчикам: `Publish`/`PublishStream`/`PublishStreamBatch`, `Subscribe`/`SubscribeQueue`/`SubscribeStream`, запросы `Published`/`PublishedOn`/`Subscriptions`/`HandlerErrors`, инжекторы `Deliver` (сообщение «из шины») и `SetPublishError` + `Reset`. |
| [`oidctest`](oidctest/) | `oidckit` | `New(t)` поднимает stub OIDC-провайдера (discovery, JWKS, `/authorize`, `/token`, RS256/ES256): `SetClaims`, `SetStatus`, `SetTokenBody`, `SetDiscoveryOverride`, `Hits`, `LastAuthorize`; токены для негативных тестов — `IDToken`, `IDTokenWithKID`, `IDTokenHS256`, `IDTokenUnsigned`. |
| [`outboxtest`](outboxtest/) | `outboxkit` | `Recorder` — in-memory `outboxkit.Publisher`: `Publish` вне транзакции (`pgkit.InTx`) отвечает `outboxkit.ErrNoTx` и событие не копит, как pgstore с `OUTBOX_REQUIRE_TX=true`; запросы `All`/`Published`/`Last`/`Count` + `Reset`. |
| [`pgtest`](pgtest/) | `pgkit` | `pgkit.Transactor`-фейки (`WithTx` и `WithTxOptions`): `Identity` (прогоняет `fn` без транзакции), `Recorder` (то же + счётчик `Calls`). Оба помечают `ctx` через `pgkit.WithTestTx`, поэтому код с гейтом на `pgkit.InTx` проходит без Postgres. |
| [`sessiontest`](sessiontest/) | `authnkit/session` | `New(cfg, clock)` — in-memory `session.Store` с семантикой `session/pgstore`: те же сроки (`IdleTTL`, абсолютный срок), ротация, отзыв сессии при повторе токена (`*session.ReusedError`), `session.ErrInTx` у `Rotate` в транзакции (`pgkit.InTx`); `Active(subject)` — число открытых сессий; откат транзакции фейк не видит. `Run(t, newStore)` — контрактный набор `session.Store`: время двигает набор через переданные часы, его проходят и фейк, и pgstore. |
| [`storagetest`](storagetest/) | `storagekit` | `RunContract(t, newSubject)` — общий контрактный набор для `storagekit.Storage`/`Presigner`: боевой адаптер и in-memory проверяются одними кейсами; `Subject{Storage, Presigner, Keys}` описывает проверяемую реализацию. |

## Observability

testkit ничего не эмитит: ни метрик, ни спанов, ни логов через `logkit`;
`metrictest` только ставит первым `Collect` глобальный `MeterProvider` процесса теста
(`NewProvider` глобальный провайдер не трогает).
Контейнерные helper'ы и fake'и сообщают о проблемах только через `t.Fatalf` /
возвращаемые значения. Диагностика testcontainers идёт в `t.Log` — видна с `-v`
и при падении теста, а не в общий stderr. Это тестовый код — он не часть рантайма
сервиса.

## API

Корневой пакет `testkit`:

| Символ | Назначение |
| --- | --- |
| `RequireDocker(t)` | `t.Skip` под `-short`, `t.Fatalf`, если Docker недоступен; вызывается всеми `Start*`. |
| `StartPostgres(t, opts...) *PostgresFixture` | Контейнер Postgres; `WithMigrations`/`WithMigrationsDir` применяют миграции. |
| `PostgresFixture{DSN, Container}` | DSN живого инстанса + handle контейнера (`tcpostgres.PostgresContainer`). |
| `(*PostgresFixture).Pool(t)` | Общий пул фикстуры через `pgkit.NewPool` (session-таймауты, otelpgx); открывается один раз, закрывается в `t.Cleanup`. |
| `(*PostgresFixture).DB(t)` / `.Transactor(t)` | `*pgkit.DB` и `pgkit.Transactor` поверх того же пула. |
| `(*PostgresFixture).PoolConfig()` | `pgkit.PoolConfig` пула фикстуры — для собственной проводки поверх DSN. |
| `StartNATS(t, opts...) *NATSFixture` | Контейнер NATS; `NATSFixture{URL, Container}`. |
| `StartRedis(t, opts...) *RedisFixture` | Контейнер Redis; `RedisFixture{URL, Container}`. |
| `StartMinIO(t, opts...) *MinIOFixture` | Контейнер MinIO; `MinIOFixture{Endpoint, AccessKey, SecretKey, Container}`. |
| `MigrationLadder(t, opts...)` | Лесенка источников из `WithMigrations`/`WithMigrationsDir` (up → down → up по версиям, down до нуля и up) со сверкой схемы; `WithImage` — образ Postgres. |
| `WithImage(ref)` | Подмена образа для любого `Start*`. |
| `WithMigrations(sources...)` / `WithMigrationsDir(path)` | Источники миграций (`pgkit.Source`) / каталог приложения (источник `pgkit.AppSourceName`); только `StartPostgres` и `MigrationLadder`. |
| `WithJetStream()` | Запуск NATS с `-js`; только `StartNATS`. |
| `PostgresImage`, `NATSImage`, `RedisImage`, `MinIOImage` | Константы образов по умолчанию. |

`Option` — общий тип опций всех `Start*` и `MigrationLadder`. Опция, не
применимая к вызванному helper'у (`WithJetStream` в `StartRedis`), обрывает тест
`t.Fatalf`, а не игнорируется молча.

`Container` в каждой fixture доступен напрямую — для дополнительных операций над
контейнером поверх готовых helper'ов.

## Тестирование

Helper'ы вызываются прямо из `*_test.go` консьюмера.

- `go test -short ./...` — контейнеры не поднимаются: `Start*` вызывают
  `RequireDocker`, который под `-short` делает `t.Skip`.
- `go test ./...` — полный прогон, включая контейнерные тесты (нужен Docker).
  Если демон недоступен, контейнерные тесты падают, а не пропускаются: зелёный
  полный прогон значит, что integration действительно прошёл.

Сабпакетные fake'и Docker не требуют и под `-short` отрабатывают как обычные
unit-тесты. Собственные тесты testkit устроены так же: сабпакеты — unit,
корневые helper'ы — контейнерные.

## Инварианты и подводные камни

- **`-short` = без Docker, без `-short` Docker обязателен.** Условие проверяет
  сам helper; дублировать `if testing.Short() { t.Skip(...) }` в тесте не нужно.
  Тестам, которые строят контейнер сами, тот же контракт даёт
  `testkit.RequireDocker(t)`. Собственный
  `testing.Short()` остаётся оправданным только там, где Docker ни при чём, а
  тест сам по себе долгий (десятки секунд стенного времени) — тогда в `t.Skip`
  пишется именно эта причина.
- **Cleanup — через `t.Cleanup`, не закрывайте вручную.** Контейнеры снимаются
  `testcontainers.CleanupContainer` (ошибка `Terminate` валит тест, а не
  теряется), пул `PostgresFixture` закрывается там же; ручной
  `Terminate`/`Close` не нужен и приведёт к двойному закрытию.
- **Аварийный выход процесса подчищает Ryuk.** Контейнер-реапер testcontainers
  снимает всё, что осталось после panic/`os.Exit`;
  `TESTCONTAINERS_RYUK_DISABLED=true` его выключает — тогда упавший прогон
  оставляет контейнеры висеть до ручного `docker rm`.
- **Пул фикстуры один на фикстуру.** `Pool`/`DB`/`Transactor` отдают один и тот
  же `*pgxpool.Pool` с prod-таймаутами `pgkit` (`statement_timeout` 30s,
  `lock_timeout` 5s, `idle_in_transaction_session_timeout` 60s) — код под тестом
  ведёт себя как под `pgkit.NewPool`. Пул живёт до конца теста, стартовавшего
  контейнер, поэтому его безопасно брать из подтестов.
- **testkit — только для тестов.** Не импортируйте его и сабпакеты в
  production-граф (`fx.Module` сервиса): он тянет testcontainers и `testing` и
  существует ради проверки логики на интерфейсах китов.
- **Fake'и проверяются на соответствие интерфейсу через `var _`.** Каждый fake
  закреплён за конкретным интерфейсом кита (`var _ auditkit.Logger =
  (*audittest.Recorder)(nil)` и аналоги) — при изменении интерфейса кита fake
  перестанет компилироваться, что и есть сигнал обновить helper.
- **Теги образов конкретные.** `:latest` не используется, чтобы тесты не
  менялись от обновления registry; другой образ — `WithImage(ref)`.
