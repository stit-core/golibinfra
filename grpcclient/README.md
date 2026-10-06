# grpcclient

gRPC-клиент: `*grpc.ClientConn` к downstream-сервису с OTel-инструментацией,
access-логом, дедлайном по умолчанию, сквозным request-id, keepalive и
опциональным bearer-токеном в `authorization` на каждый вызов. Аналог
[`httpclient`](../httpclient/) для gRPC — транспортный примитив, на котором
потребитель строит сгенерированный типизированный stub.

## Назначение

Kit собирает `*grpc.ClientConn` через `grpc.NewClient` с набором дефолтных
dial-опций:

- **OTel** — `otelgrpc.NewClientHandler()` как `StatsHandler`: client-спаны и
  метрика длительности `rpc.client.*` без ручной разметки.
- **Request-id** — id вызова из `ctx` (его положил `httpkit` или `grpckit`)
  уезжает в metadata `x-request-id`: downstream логирует тот же идентификатор.
- **Дедлайн** — unary-вызов без собственного дедлайна ограничивается
  `GRPC_CLIENT_TIMEOUT`.
- **Access-log** — unary- и stream-интерсепторы пишут каждый вызов в
  [`logkit.Logger`](../logkit/) (метод, код, длительность; уровень — по
  gRPC-коду, см. § Observability).
- **Keepalive** — HTTP/2 ping'и держат соединение живым (в т.ч. без активных
  стримов).
- **Bearer** — `PerRPCCredentials` инжектит `authorization: Bearer <token>` в
  metadata каждого RPC, если токен задан.
- **TLS** — `credentials.NewTLS` (MinVersion 1.2) с опциональными CA и
  ServerName либо `insecure` транспорт.

`grpc.NewClient` ленивый: соединение устанавливается на первом RPC, а не в `Dial`.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/grpcclient"
```

Версия модуля и доступ к репозиторию — в [корневом README](../README.md).

## Быстрый старт

`Module` провайдит один `*grpc.ClientConn` к `GRPC_CLIENT_TARGET`. Конфиг из ENV
поднимается через `NewConfig` (или `config`-композицию ENV). Из `Conn`
строится сгенерированный stub.

```go
package main

import (
	"context"

	"go.uber.org/fx"
	"google.golang.org/grpc"

	"git.stit.tech/stit-core/golibinfra/grpcclient"
	"git.stit.tech/stit-core/golibinfra/logkit"

	pb "example.com/gen/billingpb" // protoc-gen-go-grpc stub
)

func main() {
	fx.New(
		logkit.Module,
		fx.Provide(logkit.NewConfig),    // Config логгера из ENV
		fx.Provide(grpcclient.NewConfig), // Config клиента из ENV
		grpcclient.Module,                // *grpc.ClientConn к GRPC_CLIENT_TARGET
		fx.Provide(func(conn *grpc.ClientConn) pb.BillingClient {
			return pb.NewBillingClient(conn)
		}),
		fx.Invoke(func(c pb.BillingClient) {
			_, _ = c.Charge(context.Background(), &pb.ChargeRequest{})
		}),
	).Run()
}
```

`Module` вешает на fx-lifecycle `OnStop`-хук, который закрывает соединение при
остановке приложения.

### Без fx / несколько таргетов

`Module` провайдит ровно один `*grpc.ClientConn`. Для нескольких downstream-целей
(или вне fx) — `Dial` напрямую:

```go
conn, err := grpcclient.Dial(cfg, log)
if err != nil {
	return err
}
defer conn.Close()
```

Дополнительные `grpc.DialOption` (load-balancing, service-config retry-policy)
прокидываются через `WithDialOptions`, опции otelgrpc — через
`WithStatsHandlerOptions`:

```go
conn, err := grpcclient.Dial(cfg, log,
	grpcclient.WithDialOptions(grpc.WithDefaultServiceConfig(`{...}`)),
	grpcclient.WithStatsHandlerOptions(otelgrpc.WithMessageEvents(otelgrpc.ReceivedEvents)),
)
```

Опции потребителя применяются последними и побеждают одноимённые опции кита.

### Сигнатуры

```go
func Dial(cfg Config, log logkit.Logger, opts ...Option) (*grpc.ClientConn, error)
func WithDialOptions(opts ...grpc.DialOption) Option
func WithStatsHandlerOptions(opts ...otelgrpc.Option) Option
func NewConfig() (Config, error)
func (c Config) Validate() error

var Module = fx.Module("grpcclient", fx.Provide(newConn))
var ErrConfig = errors.New("grpcclient: invalid config")
```

`Dial` с `log == nil` подставляет `logkit.Nop()`.

## Конфигурация

`grpcclient.Config` — **part of public API** (README § Стабильность API). Имена
полей и `envconfig`-теги стабильны; их изменение — breaking change: выпускается
minor-релизом, помечается `!` в Conventional Commit и строкой миграции в
аннотации тега (см. [корневой README § Версионирование](../README.md)).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `GRPC_CLIENT_TARGET` | `Target` | `string` | — (`required`) | Адрес downstream gRPC-сервиса (gRPC name-resolver target, напр. `dns:///billing:9090`) |
| `GRPC_CLIENT_TOKEN` | `Token` | `string` | `""` | Bearer-токен в `authorization` на каждый RPC. Пусто → `PerRPCCredentials` не подключается |
| `GRPC_CLIENT_ALLOW_INSECURE_TOKEN` | `AllowInsecureToken` | `bool` | `false` | Разрешает токен поверх plaintext (внутренний mesh с mTLS на sidecar). Без него такая пара — `ErrConfig` |
| `GRPC_CLIENT_TLS` | `TLS` | `bool` | `false` | `true` → TLS-транспорт (MinVersion 1.2); `false` → `insecure` (plaintext) |
| `GRPC_CLIENT_TLS_CA_FILE` | `TLSCAFile` | `string` | `""` | PEM с доверенными корнями (внутренний PKI). Пусто → системный стор. Требует `GRPC_CLIENT_TLS=true` |
| `GRPC_CLIENT_TLS_SERVER_NAME` | `TLSServerName` | `string` | `""` | Имя в сертификате, если target адресует под по IP. Требует `GRPC_CLIENT_TLS=true` |
| `GRPC_CLIENT_TIMEOUT` | `Timeout` | `time.Duration` | `10s` | Дедлайн unary-вызова без дедлайна в `ctx`. `0` — дедлайн только из `ctx`; на стримы не влияет |
| `GRPC_CLIENT_MAX_RECV_MSG_SIZE` | `MaxRecvMsgSize` | `int` | `4194304` (4 MiB) | Лимит входящего сообщения, байты. `0` — дефолт |
| `GRPC_CLIENT_MAX_SEND_MSG_SIZE` | `MaxSendMsgSize` | `int` | `4194304` (4 MiB) | Лимит исходящего сообщения, байты. `0` — дефолт |
| `GRPC_CLIENT_KEEPALIVE_TIME` | `Keepalive.Time` | `time.Duration` | `30s` | Интервал HTTP/2 keepalive-ping'ов. `0` — дефолт; меньше `10s` — `ErrConfig` (grpc-go молча поднял бы до `10s`) |
| `GRPC_CLIENT_KEEPALIVE_TIMEOUT` | `Keepalive.Timeout` | `time.Duration` | `20s` | Сколько ждать ack на ping до признания соединения мёртвым. `0` — дефолт |

`GRPC_CLIENT_TARGET` — per-concern endpoint кита: разное значение = разный
downstream-хост (README § Per-concern endpoints).

### Правила Validate

`NewConfig` и `Dial` вызывают `Config.Validate()`; ошибка оборачивает `ErrConfig`
и падает на старте, а не на первом вызове.

| Правило | Почему |
|---|---|
| `Target` не пуст | Пустой target даёт успешный `Dial` и `Unavailable` на первом RPC. |
| `MaxRecvMsgSize`, `MaxSendMsgSize`, `Timeout` ≥ 0 | Отрицательные значения отбивали бы любой вызов. |
| `Keepalive.Time` ≥ `10s` (или `0`) | Меньший интервал grpc-go клампит молча — конфиг врал бы о поведении. |
| `Token` + plaintext → нужен `AllowInsecureToken` | Токен по незашифрованному каналу читается любым узлом на пути. |
| `TLSCAFile`/`TLSServerName` только при `TLS=true` | Иначе TLS-параметры заданы, а транспорт plaintext. |

Нулевые поля (кроме `Timeout`) заменяются дефолтами, поэтому
`grpcclient.Config{Target: …}` даёт рабочий клиент.

### Совместимость keepalive с grpckit

Дефолтный `GRPC_CLIENT_KEEPALIVE_TIME` (`30s`) не чаще серверного
`GRPC_KEEPALIVE_MIN_TIME` (`20s`) — связка китов «из коробки» не получает GOAWAY
`too_many_pings`. Уменьшая клиентский интервал, уменьшайте и серверный минимум:
после трёх слишком частых ping'ов сервер закрывает соединение вместе со всеми
стримами.

## Observability

`Dial` ставит `otelgrpc.NewClientHandler()` как `grpc.StatsHandler` —
инструментация целиком на стороне `go.opentelemetry.io/contrib/.../otelgrpc`. Kit
не определяет собственных метрик и спанов.

| Сигнал | Источник | Что |
|---|---|---|
| Client span | `otelgrpc` StatsHandler | SpanKind `CLIENT` на каждый RPC; имя = `package.Service/Method`; trace-context уходит в metadata |
| `rpc.client.call.duration` (histogram, `s`) | `otelgrpc` StatsHandler | Длительность RPC — единственная метрика StatsHandler; при `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/old` — `rpc.client.duration` (`ms`), `rpc/dup` — обе. Размеры сообщений в метрики не попадают (только событиями в спан, и только с `otelgrpc.WithMessageEvents`) |

Атрибуты метрики и спана зависят от `OTEL_SEMCONV_STABILITY_OPT_IN`:

| Режим | Атрибуты |
|---|---|
| по умолчанию | `rpc.system.name` = `grpc`, `rpc.method` = полное имя (`package.Service/Method`), `rpc.response.status_code` = канонический код в верхнем регистре (OK, NOT_FOUND, …), `server.address`/`server.port` |
| `rpc/old` | `rpc.system` = `grpc`, `rpc.service`, `rpc.method` = короткое имя, `rpc.response.status_code`, `server.address`/`server.port` |
| `rpc/dup` | объединение обоих наборов; `rpc.method` — полное имя |

Атрибута `rpc.grpc.status_code` эта версия otelgrpc не эмитит ни в одном режиме.
Спаны/метрики уходят через глобальные OTel-провайдеры; без зарегистрированного
провайдера — no-op (экспорт настраивает потребитель своей проводкой OTel SDK).

Access-log идёт отдельно, через интерсепторы и `logkit.Logger`:

| Исход | Уровень | Сообщение | Поля |
|---|---|---|---|
| `OK` | `Debug` | `grpc client call` | `method`, `code`, `duration_ms` |
| `Internal`, `Unknown`, `DataLoss`, `Unavailable`, `Unimplemented` | `Error` | `grpc client call failed` | `method`, `code`, `duration_ms`, `error` |
| остальные не-OK коды (`InvalidArgument`, `NotFound`, `Unauthenticated`, `PermissionDenied`, `Canceled`, `DeadlineExceeded`, …) | `Warn` | `grpc client call failed` | `method`, `code`, `duration_ms`, `error` |

Стрим логируется одной записью в момент завершения (EOF, ошибка или отмена,
замеченные на Recv/Send) — до этого исхода у вызова ещё нет. `trace_id`/`span_id`
и `request_id` в строки лога инжектит `logkit`. `Dial` с токеном при `TLS=false`
пишет `Warn` `grpcclient: bearer token over plaintext transport` (`target`).

Внутренние сообщения grpc-go (GOAWAY, keepalive, резолвер) уходят в `logkit`
записью `grpc` с полем `msg`: info и verbose → `Debug`, warning → `Warn`, error →
`Error`. Мост ставится при импорте кита (до первого обращения к grpc), а `Dial`
привязывает к нему логгер графа; действует последний привязанный — логгер у
процесса один. Пока логгер не привязан (в том числе после `Dial(cfg, nil)`, где
мост остаётся свободным), записи идут дефолтным логгером grpc-go: ERROR в stderr,
`GRPC_GO_LOG_SEVERITY_LEVEL` работает как обычно.

## API

| Символ | Сигнатура | Назначение |
| --- | --- | --- |
| `Config` | `struct{ Target, Token string; AllowInsecureToken, TLS bool; TLSCAFile, TLSServerName string; Timeout time.Duration; MaxRecvMsgSize, MaxSendMsgSize int; Keepalive KeepaliveConfig }` | Public env-контракт клиента |
| `KeepaliveConfig` | `struct{ Time, Timeout time.Duration }` | Параметры HTTP/2 keepalive |
| `Config.Validate` | `func() error` | Проверка контракта конфига; ошибка оборачивает `ErrConfig` |
| `Dial` | `func(Config, logkit.Logger, ...Option) (*grpc.ClientConn, error)` | Сборка `*grpc.ClientConn` с дефолтными dial-опциями |
| `WithDialOptions` | `func(...grpc.DialOption) Option` | Доп. `grpc.DialOption` поверх дефолтных |
| `WithStatsHandlerOptions` | `func(...otelgrpc.Option) Option` | Опции otelgrpc-хендлера (фильтр, message events, свой provider) |
| `Option` | `func(*options)` | Функциональная опция для `Dial` |
| `NewConfig` | `func() (Config, error)` | `Config` из ENV + `Validate` |
| `Module` | `fx.Options` | Провайдит один `*grpc.ClientConn`; lifecycle-aware (`OnStop` → `Close`) |
| `ErrConfig` | `error` | Sentinel невалидной конфигурации |

### `*grpc.ClientConn`

Возврат `Dial`/`Module` — стандартный `*grpc.ClientConn` из
`google.golang.org/grpc`. Поверх него строятся сгенерированные stub'ы
(`pb.NewXxxClient(conn)`). Соединение ленивое; повторное использование одного
`Conn` для всех вызовов к таргету — норма (мультиплексирование HTTP/2).

### `PerRPCCredentials` (bearer)

Bearer-кредл — приватная реализация `credentials.PerRPCCredentials`: на каждый RPC
кладёт `authorization: Bearer <token>`. Подключается только при непустом `Token`.
`RequireTransportSecurity()` возвращает значение `TLS`: под TLS креды требуют
защищённого транспорта, а plaintext допускается только с явным
`GRPC_CLIENT_ALLOW_INSECURE_TOKEN=true`.

## Тестирование

`grpc.NewClient` ленив, поэтому `Dial` собирает `*grpc.ClientConn` без реального
соединения — сборку клиента и dial-опции можно проверять без сервера:

```go
conn, err := grpcclient.Dial(grpcclient.Config{Target: "passthrough:///x"}, nil)
```

Схема `passthrough:///` отдаёт target резолверу как есть, без DNS-запроса. Для
полного round-trip с настоящим stub'ом используйте `google.golang.org/grpc/test/bufconn`:
поднимите `*grpc.Server` на in-memory listener и передайте его dialer в
`WithDialOptions`:

```go
lis := bufconn.Listen(1 << 20)
// … grpc.NewServer + Serve(lis) …
conn, err := grpcclient.Dial(
	grpcclient.Config{Target: "passthrough:///bufnet", Token: "tok", AllowInsecureToken: true}, log,
	grpcclient.WithDialOptions(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})),
)
```

Уровни access-log проверяются `testkit/logtest` (`logtest.Capture()` —
`logkit.Logger`, копящий записи в памяти).

## Инварианты и подводные камни

- **Ленивое соединение.** `Dial`/`Module` не открывают TCP-сокет — он
  устанавливается на первом RPC. Успешный `Dial` не гарантирует доступность
  таргета; ошибки connect'а приходят на вызове. Для немедленной проверки —
  `conn.Connect()` + `conn.WaitForStateChange`.
- **Lifecycle соединения.** Под `Module` закрытие делает fx (`OnStop` → `conn.Close()`).
  При прямом `Dial` закрывать соединение — обязанность вызывающего (`defer conn.Close()`).
- **Дедлайн только у unary.** Интерсептор ставит `GRPC_CLIENT_TIMEOUT`, лишь
  когда в `ctx` дедлайна нет; стримы живут по `ctx` — их длительность легитимно
  не ограничена.
- **Bearer из env.** Токен берётся из `GRPC_CLIENT_TOKEN`. Per-RPC creds
  подключаются только при непустом токене.
- **Bearer поверх plaintext — риск.** При `GRPC_CLIENT_TLS=false` транспорт
  plaintext и токен едет по незашифрованному каналу: его читает любой узел на
  пути. Кит требует явного `GRPC_CLIENT_ALLOW_INSECURE_TOKEN=true` и всё равно
  пишет `Warn` при `Dial`. За пределами доверенной сети ставьте
  `GRPC_CLIENT_TLS=true`.
- **Один target на `Module`.** `Module` провайдит ровно один `*grpc.ClientConn` к
  `GRPC_CLIENT_TARGET`. Несколько downstream-целей — `Dial` напрямую (fx не
  различает несколько `*grpc.ClientConn` без именованных групп).
- **Keepalive.** `PermitWithoutStream: true` — ping'и идут даже без активных
  стримов; держит соединение живым через идл-таймауты промежуточных прокси.
  Слишком агрессивный `GRPC_CLIENT_KEEPALIVE_TIME` сервер отобьёт политикой
  `EnforcementPolicy` (`too_many_pings`) — см. § Совместимость keepalive.
