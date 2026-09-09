# Архитектура grpckit

`grpckit` — серверный gRPC-примитив stit. Он владеет транспортом (HTTP/2 на своём
порту `GRPC_ADDR`), цепочкой интерсепторов, observability и lifecycle; доменные
gRPC-сервисы (gen-стабы + бизнес-логика) живут в сервис-репо и подключаются как
данные.

## Компоненты

| Тип | Файл | Роль |
|---|---|---|
| `Config` / `KeepaliveConfig` / `Validate` | `config.go` | Публичный env-контракт сервера и его проверка. |
| `Server` | `grpckit.go` | Обёртка над `*grpc.Server` + `*health.Server`; `New`/`Serve`/`Stop`/`SetServing`/`GRPC`/`Addr`. |
| `ServiceRegistrar` | `grpckit.go` | `func(*grpc.Server)` — порт регистрации доменного сервиса. |
| `ServerOption` | `grpckit.go` | Программная настройка `New`: auth, сервисы, health-пробы, свои интерсепторы, `grpc.ServerOption`, опции otelgrpc. |
| `Authenticator` | `auth.go` | Порт аутентификации (реализует потребитель). |
| Интерсепторы | `interceptors.go`, `auth.go` | request-id, access-log, recovery, опц. auth. |
| Health-loop | `health.go` | Периодическая переоценка проб группы `healthkit.probes` → overall-статус. |
| `RequestIDFrom` / `HeaderRequestID` | `requestid.go` | Чтение request-id из ctx + регистрация атрибута `request_id` в `logkit`. |
| `Module` / `NewConfig` / `newServer` | `module.go` | fx-сборка: build + lifecycle (listen + serve + graceful stop). |

## Поток сборки (`New`)

`New(cfg, log, opts...)` собирает `*grpc.Server`, но **не** слушает порт — старт и
стоп отдаются вызывающему (`Serve`/`Stop`) либо fx-lifecycle. Шаги:

1. **`Validate` + дефолты**: невалидный конфиг → `ErrConfig`; нулевые поля
   получают дефолты (см. [configuration.md](configuration.md)).
2. **grpclog → logkit**: `New` привязывает логгер графа к мосту grpclog
   (сам мост ставится при импорте кита, до первого обращения к grpc).
3. **`grpc.ServerOption`-набор**: `otelgrpc` StatsHandler, лимиты сообщений,
   `MaxConcurrentStreams` (если задан), keepalive (`ServerParameters` +
   `EnforcementPolicy{PermitWithoutStream: true}`).
4. **TLS** — если задана пара `TLSCertFile`/`TLSKeyFile`; иначе сервер слушает
   plaintext (TLS-терминацию обычно делает корп-mesh/ingress).
5. **Цепочки интерсепторов** — фиксированный порядок (см. ниже); auth и
   интерсепторы потребителя добавляются в хвост.
6. **Health-сервис** регистрируется всегда (`grpc.health.v1.Health`).
7. **Доменные сервисы** из `WithService`/fx-group применяются в порядке передачи.
8. **Reflection** регистрируется, только если `cfg.Reflection == true`.
9. **Опции потребителя** из `WithServerOptions` применяются последними и
   побеждают одноимённые опции кита.

## Цепочка интерсепторов

Порядок фиксирован в `New` и одинаков для unary и stream. Каждый интерсептор есть
в двух формах — `*Unary` и `*Stream`; stream-формы подменяют `ctx` через
`wrappedStream`, потому что `grpc.ServerStream.Context()` иначе read-only.

| # | Интерсептор | Назначение |
|---|---|---|
| 1 | `request-id` | Берёт `x-request-id` из metadata; невалидный (пустой, длиннее 128, не печатный ASCII) заменяет на UUID v7 (`idkit.NewV7`). Кладёт id под общий с `httpkit` ключ (`internal/requestid`) и отдаёт его клиенту в response-header `x-request-id`. |
| 2 | `access-log` | Меряет длительность, логирует исход одной записью; уровень — по gRPC-коду (см. [observability.md](observability.md)). |
| 3 | `recovery` | `recover()` вокруг нижележащих интерсепторов и handler'а; паника → лог `Error` (method + panic + stack) и `codes.Internal`. |
| 4 | `auth` (опц.) | Только если задан `Authenticator`. Извлекает bearer-токен, делегирует валидацию порту, обогащает `ctx` identity. Клиенту уходит обезличенный `Unauthenticated`, текст ошибки порта — только в `Warn`-лог. Health и reflection — exempt. |
| 5 | интерсепторы потребителя | `WithUnaryInterceptors` / `WithStreamInterceptors` — вплотную к handler'у: видят аутентифицированный `ctx`, их паники ловит recovery, их исход попадает в access-log. |

WHY такого порядка:

- **request-id первым** — `request_id` есть в `ctx` у всех остальных, включая
  access-log и лог паники: без него самый важный класс инцидентов не
  коррелируется с вызовом клиента.
- **access-log снаружи recovery** — паника превращается в `codes.Internal` до
  возврата в access-log, поэтому у паники есть и запись `grpc call failed` с
  кодом, и отдельная `grpc panic recovered` со стеком.
- **recovery снаружи auth и интерсепторов потребителя** — их код тоже
  потребительский и тоже может паниковать.
- **auth после access-log** — отклонённые вызовы попадают в лог с корректным
  `request_id`.

## otelgrpc StatsHandler

Observability подключается одним `grpc.StatsHandler(otelgrpc.NewServerHandler(...))`
— не интерсептором. Handler сам ведёт server-span и эмитит метрику длительности
RPC (`rpc.server.*`), читая meter/tracer из глобальных OTel-провайдеров (без
зарегистрированного провайдера — no-op; экспорт настраивает потребитель).
Опции хендлера (фильтр health-вызовов, message events, свой provider) — через
`WithStatsHandlerOptions`. Конкретные имена и атрибуты — в
[observability.md](observability.md).

## fx value-group `grpckit.services`

Доменные сервисы декаплированы от кита через value-group `grpckit.services`
(константа `grpckit.ServicesGroup`). Каждый сервис-репо провайдит
`ServiceRegistrar` с этим тегом; `Module` собирает `[]ServiceRegistrar` и
навешивает все стабы до `Serve`. Так `grpckit` не импортирует ни одного `*.pb.go`
— это держит границу «инфра-lib ↔ домен» из корневого CLAUDE.md.

`Module` **не** provide'ит `Config` (та же asymmetry note, что в `pgkit`):
приложение само решает, как грузить конфиг. Дешёвый путь —
`fx.Provide(grpckit.NewConfig)`. `Authenticator` во fx помечен `optional:"true"` —
нет реализации в графе, auth не навешивается. Пробы из группы `healthkit.probes`
(`healthkit.Group`) подхватываются автоматически.

## Lifecycle (fx)

`Module` содержит `fx.Invoke(func(*Server) {})`: fx строит конструкторы лениво, и
без этого вызова сервер не создавался бы, если в графе никто не зависит от
`*Server` — приложение стартовало бы с незанятым портом.

`newServer` строит `Server` и вешает `fx.Hook`:

- **OnStart** — `net.Listen("tcp", cfg.Addr)`, затем `srv.Serve(ln)` в горутине
  (не блокирует fx-старт). Ошибка listen — фатальна для старта.
- **OnStop** — `srv.Stop(ctx)`: health-loop останавливается, health →
  `NOT_SERVING` (`health.Shutdown`), `GracefulStop` в пределах `ShutdownGrace`,
  по таймауту или отмене `ctx` — форс `Stop`.

## Ключевые решения (WHY)

- **StatsHandler, а не otel-интерсептор** — StatsHandler видит сообщения и
  трейс-контекст на уровне транспорта; это рекомендованный otelgrpc-путь.
- **request-id под ключом `internal/requestid`** — единый `request_id` в логах
  HTTP и gRPC без импорта `httpkit` (huma/chi) в gRPC-сервер; сам атрибут
  `request_id` кит регистрирует в `logkit` при импорте, поэтому gRPC-only сервис
  получает поле в каждой записи. Входящий id валидируется, потому что попадает в
  каждую строку лога.
- **Обезличенный `Unauthenticated`** — текст ошибки порта может нести внутренние
  детали (имя ключа, причина истечения, состояние стора); клиенту это знать не
  нужно, оператору — да, поэтому деталь идёт в лог.
- **Уровень access-log по коду** — `Error` только там, где виноват сервер
  (`Internal`/`Unknown`/`DataLoss`/`Unavailable`/`Unimplemented`); клиентские
  ошибки и отказы — `Warn`, чтобы алерты на `Error` не шумели от невалидных
  запросов. Политика общая с `grpcclient` (`internal/grpcstatus`).
- **Health всегда, reflection — opt-in** — health нужен оркестратору всегда;
  reflection раскрывает схему API и по умолчанию выключен (см.
  [usage.md](usage.md) и инварианты).
- **Health по пробам, а не только по факту `Serve`** — иначе оркестратор видит
  SERVING у сервиса с недоступной БД; пробы берутся из общей fx-группы `healthkit.probes`,
  той же, что читает `/ready` в `httpkit`.
- **Auth — порт, а не реализация** — `grpckit` владеет механикой интерсептора, но
  не знает про JWT/сессии; проверку токена отдаёт домену (напр. access-гейтвею).
