# Конфигурация grpckit

## Config (ENV)

`grpckit.Config` — публичный env-контракт сервера. Загружается через
config-композицию (ENV) либо напрямую `grpckit.NewConfig()`
(`envconfig.Process("", &c)` + `Validate`).

> **Public API.** Имена полей и их `envconfig`/`default`-теги — часть стабильного
> контракта (`grpckit.Config` числится в [корневом README § Стабильность
> API](../../README.md)). Менять их = breaking change: выпускается minor-релизом,
> помечается `!` в Conventional Commit и строкой миграции в аннотации тега
> (корневой README § Версионирование).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `GRPC_ADDR` | `Addr` | `string` | `:9090` | Bind-адрес gRPC-сервера (HTTP/2). «Шов» под split-топологию (см. [корневой README § Per-concern endpoints](../../README.md)). |
| `GRPC_MAX_RECV_MSG_SIZE` | `MaxRecvMsgSize` | `int` | `4194304` (4 MiB) | Лимит входящего сообщения, байты (`grpc.MaxRecvMsgSize`). `0` — дефолт. |
| `GRPC_MAX_SEND_MSG_SIZE` | `MaxSendMsgSize` | `int` | `4194304` (4 MiB) | Лимит исходящего сообщения, байты (`grpc.MaxSendMsgSize`). `0` — дефолт. |
| `GRPC_MAX_CONCURRENT_STREAMS` | `MaxConcurrentStreams` | `uint32` | `0` | Лимит одновременных стримов на соединение. `0` — без лимита (дефолт grpc-go). |
| `GRPC_REFLECTION` | `Reflection` | `bool` | `false` | Включает server reflection. В prod держим выключенным (раскрывает схему API). |
| `GRPC_SHUTDOWN_GRACE` | `ShutdownGrace` | `time.Duration` | `10s` | Окно graceful-остановки; по истечении — форс `Stop`. `0` — дефолт. |
| `GRPC_HEALTH_INTERVAL` | `HealthInterval` | `time.Duration` | `10s` | Период переоценки health-проб из fx-группы `healthkit.probes`. `0` — переоценка выключена. |
| `GRPC_HEALTH_PROBE_TIMEOUT` | `HealthProbeTimeout` | `time.Duration` | `5s` | Дедлайн одной health-пробы в цикле переоценки. `0` — дефолт. |
| `GRPC_TLS_CERT_FILE` | `TLSCertFile` | `string` | — | PEM-сертификат сервера. TLS включается, только если заданы **оба** cert и key. |
| `GRPC_TLS_KEY_FILE` | `TLSKeyFile` | `string` | — | PEM-ключ сервера. Без пары cert/key сервер слушает plaintext. |

### KeepaliveConfig (вложенный)

`Config.Keepalive` — keepalive-параметры HTTP/2-транспорта (часть того же
публичного контракта).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `GRPC_KEEPALIVE_TIME` | `Keepalive.Time` | `time.Duration` | `2h` | Интервал серверных keepalive-ping'ов при простое соединения. `0` — дефолт. |
| `GRPC_KEEPALIVE_TIMEOUT` | `Keepalive.Timeout` | `time.Duration` | `20s` | Ожидание ответа на ping перед разрывом соединения. `0` — дефолт. |
| `GRPC_KEEPALIVE_MIN_TIME` | `Keepalive.MinTime` | `time.Duration` | `20s` | Минимальный интервал клиентских ping'ов (enforcement); чаще — страйк, после третьего соединение получает GOAWAY. `PermitWithoutStream` включён. |

## Правила Validate

`NewConfig` и `New` вызывают `Config.Validate()`; ошибка оборачивает
`grpckit.ErrConfig` и падает на старте приложения, а не на первом запросе.

| Правило | Почему |
|---|---|
| `MaxRecvMsgSize`, `MaxSendMsgSize` ≥ 0 | Отрицательный лимит отбивает любое сообщение с `ResourceExhausted`. |
| `ShutdownGrace` ≥ 0 | Отрицательный грейс означал бы мгновенный форс-стоп при каждом рестарте. |
| `HealthInterval` ≥ 0 | `0` — переоценка проб выключена; отрицательное значение бессмысленно. |
| `HealthProbeTimeout` ≥ 0 | Дедлайн пробы; `0` заменяется дефолтом, отрицательный обрывал бы каждую пробу до старта. |
| `Keepalive.Time`, `Keepalive.Timeout`, `Keepalive.MinTime` ≥ 0 | Отрицательные интервалы grpc-go принимает молча и ведёт себя непредсказуемо. |
| `TLSCertFile` и `TLSKeyFile` — оба или ни одного | Одна половина пары иначе означала бы «TLS задумывался, но сервер слушает plaintext». |

Нулевое поле — не ошибка: `New` подставляет дефолт из таблицы выше, поэтому
`grpckit.Config{}` даёт рабочий сервер (удобно в тестах). Исключения —
`HealthInterval` и `MaxConcurrentStreams`, где `0` означает «выключено» и «без
лимита».

## Совместимость keepalive с grpcclient

Серверный `GRPC_KEEPALIVE_MIN_TIME` (`20s`) не строже клиентского
`GRPC_CLIENT_KEEPALIVE_TIME` (`30s`) — связка китов «из коробки» не получает
GOAWAY `too_many_pings` на idle-стримах. Ужесточая `GRPC_KEEPALIVE_MIN_TIME`,
поднимайте и клиентский интервал: у ping'ов чаще политики есть только три
страйка, после чего сервер закрывает соединение вместе со всеми стримами.

## TLS

TLS навешивается (`grpc.Creds`), когда заданы оба `GRPC_TLS_CERT_FILE` и
`GRPC_TLS_KEY_FILE`; ошибка чтения cert/key → `New` возвращает
`grpckit: tls creds: …`. В корп-топологии TLS обычно терминируется на
mesh/ingress, а сам сервис слушает plaintext — это дефолтный режим (оба поля
пусты). mTLS (проверка клиентских сертификатов) настраивается через
`grpckit.WithServerOptions(grpc.Creds(...))` — см. [usage.md](usage.md).

## Health

Health-сервис (`grpc.health.v1.Health`) регистрируется всегда. Overall-статус
(`""`) ставится в `SERVING` при `Serve` и в `NOT_SERVING` при `Stop` (до
graceful-остановки, чтобы оркестратор успел увести трафик). При непустой
fx-группе `healthkit.probes` и `GRPC_HEALTH_INTERVAL > 0` overall переоценивается по
пробам каждые `GRPC_HEALTH_INTERVAL`. Per-service статус — через
`Server.SetServing(service, ok)` (см. [usage.md](usage.md)).

Дедлайн одной пробы — отдельный `GRPC_HEALTH_PROBE_TIMEOUT` (`5s`), а не период
тика: цикл прогоняет пробы последовательно и на дефолтах укладывается в
`GRPC_HEALTH_INTERVAL` даже с зависшей зависимостью. Держите
`GRPC_HEALTH_PROBE_TIMEOUT × число проб` в пределах `GRPC_HEALTH_INTERVAL` —
иначе переоценка идёт реже заявленного периода, и оркестратор дольше видит
устаревший статус.

## Reflection

Server reflection регистрируется, только если `GRPC_REFLECTION=true`. По
умолчанию выключен. Reflection — exempt от auth-интерсептора (как и health),
поэтому его включение в prod раскрывает список методов без аутентификации.
