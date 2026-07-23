# Конфигурация grpckit

## Config (ENV)

`grpckit.Config` — публичный env-контракт сервера. Загружается через
config-композицию (ENV) либо напрямую `grpckit.NewConfig()`
(`envconfig.Process("", &c)`).

> **Public API.** Имена полей и их `envconfig`/`default`-теги — часть стабильного
> контракта (`grpckit.Config` числится в [корневом README § Стабильность
> API](../../README.md)). Менять их = breaking change, идёт мажорным апгрейдом.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `GRPC_ADDR` | `Addr` | `string` | `:9090` | Bind-адрес gRPC-сервера (HTTP/2). «Шов» под split-топологию (см. [корневой README § Per-concern endpoints](../../README.md)). |
| `GRPC_MAX_RECV_MIB` | `MaxRecvMiB` | `int` | `4` | Лимит входящего сообщения, МиБ (`MaxRecvMsgSize = N << 20`). |
| `GRPC_MAX_SEND_MIB` | `MaxSendMiB` | `int` | `4` | Лимит исходящего сообщения, МиБ (`MaxSendMsgSize = N << 20`). |
| `GRPC_REFLECTION` | `Reflection` | `bool` | `false` | Включает server reflection. В prod держим выключенным (раскрывает схему API). |
| `GRPC_SHUTDOWN_GRACE` | `ShutdownGrace` | `time.Duration` | `15s` | Окно graceful-остановки; по истечении — форс `Stop`. |
| `GRPC_TLS_CERT_FILE` | `TLSCertFile` | `string` | — | PEM-сертификат сервера. TLS включается, только если заданы **оба** cert и key. |
| `GRPC_TLS_KEY_FILE` | `TLSKeyFile` | `string` | — | PEM-ключ сервера. Без пары cert/key сервер слушает plaintext. |

### KeepaliveConfig (вложенный)

`Config.Keepalive` — keepalive-параметры HTTP/2-транспорта (часть того же
публичного контракта).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `GRPC_KEEPALIVE_TIME` | `Keepalive.Time` | `time.Duration` | `2h` | Интервал серверных keepalive-ping'ов при простое соединения. |
| `GRPC_KEEPALIVE_TIMEOUT` | `Keepalive.Timeout` | `time.Duration` | `20s` | Ожидание ответа на ping перед разрывом соединения. |
| `GRPC_KEEPALIVE_MIN_TIME` | `Keepalive.MinTime` | `time.Duration` | `5m` | Минимальный интервал клиентских ping'ов (enforcement); чаще — соединение наказывается. `PermitWithoutStream` включён. |

## TLS

TLS навешивается (`grpc.Creds`), только когда заданы оба `GRPC_TLS_CERT_FILE` и
`GRPC_TLS_KEY_FILE`; ошибка чтения cert/key → `New` возвращает ошибку
`grpckit: tls creds: …`. В корп-топологии TLS обычно терминируется на mesh/ingress,
а сам сервис слушает plaintext — это дефолтный режим (оба поля пусты).

## Health

Health-сервис (`grpc.health.v1.Health`) регистрируется всегда, конфигом не
управляется. Overall-статус (`""`) ставится в `SERVING` при `Serve` и в
`NOT_SERVING` при `Stop`. Per-service статус — через `Server.SetServing(service, ok)`
(см. [usage.md](usage.md)).

## Reflection

Server reflection регистрируется, только если `GRPC_REFLECTION=true`. По умолчанию
выключен. Reflection — exempt от auth-интерсептора (как и health), поэтому его
включение в prod раскрывает список методов без аутентификации.
