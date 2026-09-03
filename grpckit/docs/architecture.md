# Архитектура grpckit

`grpckit` — серверный gRPC-примитив stit. Он владеет транспортом (HTTP/2 на своём
порту `GRPC_ADDR`), цепочкой интерсепторов, observability и lifecycle; доменные
gRPC-сервисы (gen-стабы + бизнес-логика) живут в сервис-репо и подключаются как
данные.

## Компоненты

| Тип | Файл | Роль |
|---|---|---|
| `Config` / `KeepaliveConfig` | `config.go` | Публичный env-контракт сервера. |
| `Server` | `grpckit.go` | Обёртка над `*grpc.Server` + `*health.Server`; `New`/`Serve`/`Stop`/`SetServing`/`GRPC`. |
| `ServiceRegistrar` | `grpckit.go` | `func(*grpc.Server)` — порт регистрации доменного сервиса. |
| `ServerOption` (`WithAuthenticator`, `WithService`) | `grpckit.go` | Программная настройка `New` вне fx. |
| `Authenticator` | `auth.go` | Порт аутентификации (реализует потребитель). |
| Интерсепторы | `interceptors.go`, `auth.go` | recovery, request-id, access-log, опц. auth. |
| `Module` / `NewConfig` / `newServer` | `module.go` | fx-сборка: build + lifecycle (listen + serve + graceful stop). |

## Поток сборки (`New`)

`New(cfg, log, opts...)` собирает `*grpc.Server`, но **не** слушает порт — старт и
стоп отдаются вызывающему (`Serve`/`Stop`) либо fx-lifecycle. Шаги:

1. **`grpc.ServerOption`-набор**: `otelgrpc` StatsHandler, лимиты сообщений
   (`MaxRecvMsgSize`/`MaxSendMsgSize` = `cfg.Max*MiB << 20`), keepalive
   (`ServerParameters` + `EnforcementPolicy{PermitWithoutStream: true}`).
2. **TLS** — только если заданы оба `TLSCertFile` и `TLSKeyFile`; иначе сервер
   слушает plaintext (TLS-терминацию обычно делает корп-mesh/ingress).
3. **Цепочки интерсепторов** — фиксированный порядок (см. ниже), auth добавляется
   в хвост, только если передан `Authenticator`.
4. **Health-сервис** регистрируется всегда (`grpc.health.v1.Health`).
5. **Доменные сервисы** из `WithService`/fx-group применяются в порядке передачи.
6. **Reflection** регистрируется, только если `cfg.Reflection == true`.

## Цепочка интерсепторов

Порядок фиксирован в `New` и одинаков для unary и stream. Каждый интерсептор есть
в двух формах — `*Unary` и `*Stream`; stream-формы подменяют `ctx` через
`wrappedStream`, потому что `grpc.ServerStream.Context()` иначе read-only.

| # | Интерсептор | Назначение |
|---|---|---|
| 1 | `recovery` | `recover()` вокруг handler'а; паника → лог `Error` (method + panic + stack) + `codes.Internal`. Первый в цепочке, чтобы ловить паники нижележащих. |
| 2 | `request-id` | Берёт `x-request-id` из metadata; невалидный (пустой, длиннее 128, не печатный ASCII) заменяет на UUID v7 (`idkit.NewV7`). Кладёт id под общий с `httpkit` ключ (`internal/requestid`) — `request_id` коррелирует логи так же, как в HTTP — и отдаёт его клиенту в response-header `x-request-id`. |
| 3 | `access-log` | Меряет длительность, логирует исход одной записью; уровень — по gRPC-коду (см. [observability.md](observability.md)). |
| 4 | `auth` (опц.) | Только если задан `Authenticator`. Извлекает bearer-токен, делегирует валидацию порту, обогащает `ctx` identity. Клиенту уходит обезличенный `Unauthenticated`, текст ошибки порта — только в `Warn`-лог. Health и reflection — exempt. |

Auth стоит **после** request-id и access-log намеренно: отклонённые вызовы тоже
попадают в лог с корректным `request_id`.

## otelgrpc StatsHandler

Observability подключается одним `grpc.StatsHandler(otelgrpc.NewServerHandler())`
— не интерсептором. Handler сам ведёт server-span и эмитит метрику длительности
RPC (`rpc.server.*`), читая meter/tracer из глобальных OTel-провайдеров (без
зарегистрированного провайдера — no-op; экспорт настраивает потребитель).
Конкретные имена и единицы — в [observability.md](observability.md).

## fx value-group `grpc.services`

Доменные сервисы декаплированы от кита через value-group `grpc.services`. Каждый
сервис-репо провайдит `ServiceRegistrar` с тегом `group:"grpc.services"`; `Module`
собирает `[]ServiceRegistrar` и навешивает все стабы до `Serve`. Так `grpckit` не
импортирует ни одного `*.pb.go` — это держит границу «инфра-lib ↔ домен» из
корневого CLAUDE.md.

`Module` **не** provide'ит `Config` (та же asymmetry note, что в `pgkit`):
приложение само решает, как грузить конфиг. Дешёвый путь —
`fx.Provide(grpckit.NewConfig)` (читает ENV через `envconfig`). `Authenticator`
во fx помечен `optional:"true"` — нет реализации в графе, auth не навешивается.

## Lifecycle (fx)

`newServer` строит `Server` и вешает `fx.Hook`:

- **OnStart** — `net.Listen("tcp", cfg.Addr)`, затем `srv.Serve(ln)` в горутине
  (не блокирует fx-старт). Ошибка listen — фатальна для старта.
- **OnStop** — `srv.Stop(ctx)`: health → `NOT_SERVING` (`health.Shutdown`),
  `GracefulStop` в пределах `ShutdownGrace`, по таймауту или отмене `ctx` — форс
  `Stop`.

## Ключевые решения (WHY)

- **StatsHandler, а не otel-интерсептор** — StatsHandler видит сообщения и
  трейс-контекст на уровне транспорта; это рекомендованный otelgrpc-путь.
- **request-id под ключом `internal/requestid`** — единый `request_id` в логах
  HTTP и gRPC без импорта `httpkit` (huma/chi) в gRPC-сервер; входящий id
  валидируется, потому что попадает в каждую строку лога.
- **Обезличенный `Unauthenticated`** — текст ошибки порта может нести внутренние
  детали (имя ключа, причина истечения, состояние стора); клиенту это знать не
  нужно, оператору — да, поэтому деталь идёт в лог.
- **Уровень access-log по коду** — `Error` только там, где виноват сервер
  (`Internal`/`Unknown`/`DataLoss`/`Unavailable`/`Unimplemented`); клиентские
  ошибки и отказы — `Warn`, чтобы алерты на `Error` не шумели от невалидных
  запросов.
- **Health всегда, reflection — opt-in** — health нужен оркестратору всегда;
  reflection раскрывает схему API и по умолчанию выключен (см.
  [usage.md](usage.md) и инварианты).
- **Auth — порт, а не реализация** — `grpckit` владеет механикой интерсептора, но
  не знает про JWT/сессии; проверку токена отдаёт домену (напр. access-гейтвей).
