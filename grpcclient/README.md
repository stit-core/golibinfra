# grpcclient

gRPC-клиент: `*grpc.ClientConn` к downstream-сервису с OTel-инструментацией,
access-логом, keepalive и опциональным bearer-токеном в `authorization` на каждый
вызов. Аналог [`httpclient`](../httpclient/) для gRPC — транспортный примитив, на
котором потребитель строит сгенерированный типизированный stub.

## Назначение

Kit собирает `*grpc.ClientConn` через `grpc.NewClient` с набором дефолтных
dial-опций:

- **OTel** — `otelgrpc.NewClientHandler()` как `StatsHandler`: client-спаны и
  метрика длительности `rpc.client.*` без ручной разметки.
- **Access-log** — unary-интерсептор пишет каждый вызов в [`logkit.Logger`](../logkit/)
  (метод, код, длительность; уровень — по gRPC-коду, см. § Observability).
- **Keepalive** — HTTP/2 ping'и держат соединение живым (в т.ч. без активных
  стримов).
- **Bearer** — `PerRPCCredentials` инжектит `authorization: Bearer <token>` в
  metadata каждого RPC, если токен задан.
- **TLS** — `credentials.NewTLS` (MinVersion 1.2) либо `insecure` транспорт;
  bearer поверх plaintext логируется как риск (см. § Инварианты).

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
		fx.Provide(grpcclient.NewConfig), // Config из ENV
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
прокидываются через `WithDialOptions`:

```go
conn, err := grpcclient.Dial(cfg, log,
	grpcclient.WithDialOptions(grpc.WithDefaultServiceConfig(`{...}`)),
)
```

### Сигнатуры

```go
func Dial(cfg Config, log logkit.Logger, opts ...Option) (*grpc.ClientConn, error)
func WithDialOptions(opts ...grpc.DialOption) Option
func NewConfig() (Config, error)

var Module = fx.Module("grpcclient", fx.Provide(newConn))
```

`Dial` с `log == nil` подставляет `logkit.Nop()`.

## Конфигурация

`grpcclient.Config` — **part of public API** (README § Стабильность API). Имена
полей и `envconfig`-теги стабильны; их изменение — breaking change и идёт
мажорным апгрейдом.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `GRPC_CLIENT_TARGET` | `Target` | `string` | — (`required`) | Адрес downstream gRPC-сервиса (gRPC name-resolver target, напр. `dns:///billing:9090`) |
| `GRPC_CLIENT_TOKEN` | `Token` | `string` | `""` | Bearer-токен в `authorization` на каждый RPC. Пусто → `PerRPCCredentials` не подключается |
| `GRPC_CLIENT_TLS` | `TLS` | `bool` | `false` | `true` → TLS-транспорт (MinVersion 1.2); `false` → `insecure` (plaintext); с токеном при `false` `Dial` пишет `Warn` |
| `GRPC_CLIENT_KEEPALIVE_TIME` | `Keepalive.Time` | `time.Duration` | `30s` | Интервал HTTP/2 keepalive-ping'ов |
| `GRPC_CLIENT_KEEPALIVE_TIMEOUT` | `Keepalive.Timeout` | `time.Duration` | `20s` | Сколько ждать ack на ping до признания соединения мёртвым |

`GRPC_CLIENT_TARGET` — per-concern endpoint кита: разное значение = разный
downstream-хост (README § Per-concern endpoints).

`Token` читается из `GRPC_CLIENT_TOKEN`.

## Observability

`Dial` ставит `otelgrpc.NewClientHandler()` как `grpc.StatsHandler` —
инструментация целиком на стороне `go.opentelemetry.io/contrib/.../otelgrpc`. Kit
не определяет собственных метрик и спанов.

| Сигнал | Источник | Что |
|---|---|---|
| Client span | `otelgrpc` StatsHandler | SpanKind `CLIENT` на каждый RPC; атрибуты `rpc.system`, `rpc.service`, `rpc.method`, `rpc.grpc.status_code`, `server.address`; trace-context уходит в metadata |
| `rpc.client.call.duration` (histogram, `s`) | `otelgrpc` StatsHandler | Длительность RPC — единственная метрика StatsHandler; при `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/old` — `rpc.client.duration` (`ms`), `rpc/dup` — обе. Размеры сообщений в метрики не попадают (только событиями в спан) |

Атрибуты метрики: `rpc.system=grpc`, `rpc.service`, `rpc.method`,
`rpc.response.status_code`. Спаны/метрики эмитятся через глобальные
OTel-провайдеры; без зарегистрированного провайдера — no-op (экспорт настраивает
потребитель своей проводкой OTel SDK).

Access-log идёт отдельно, через unary-интерсептор и `logkit.Logger` (стримы в
access-log не попадают):

| Исход | Уровень | Сообщение | Поля |
|---|---|---|---|
| `OK` | `Debug` | `grpc client call` | `method`, `code`, `duration_ms` |
| `Internal`, `Unknown`, `DataLoss`, `Unavailable`, `Unimplemented` | `Error` | `grpc client call failed` | `method`, `code`, `duration_ms`, `error` |
| остальные не-OK коды (`InvalidArgument`, `NotFound`, `Unauthenticated`, `PermissionDenied`, `Canceled`, `DeadlineExceeded`, …) | `Warn` | `grpc client call failed` | `method`, `code`, `duration_ms`, `error` |

`trace_id`/`span_id` в строки лога инжектит `logkit`. `Dial` с токеном при
`TLS=false` пишет `Warn` `grpcclient: bearer token over plaintext transport`
(`target`).

## API

| Символ | Сигнатура | Назначение |
|---|---|---|
| `Config` | `struct{ Target, Token string; TLS bool; Keepalive KeepaliveConfig }` | Public env-контракт клиента |
| `KeepaliveConfig` | `struct{ Time, Timeout time.Duration }` | Параметры HTTP/2 keepalive |
| `Dial` | `func(Config, logkit.Logger, ...Option) (*grpc.ClientConn, error)` | Сборка `*grpc.ClientConn` с дефолтными dial-опциями |
| `WithDialOptions` | `func(...grpc.DialOption) Option` | Доп. `grpc.DialOption` поверх дефолтных |
| `Option` | `func(*options)` | Функциональная опция для `Dial` |
| `NewConfig` | `func() (Config, error)` | `Config` из ENV (`envconfig.Process`) |
| `Module` | `fx.Options` | Провайдит один `*grpc.ClientConn`; lifecycle-aware (`OnStop` → `Close`) |

### `*grpc.ClientConn`

Возврат `Dial`/`Module` — стандартный `*grpc.ClientConn` из
`google.golang.org/grpc`. Поверх него строятся сгенерированные stub'ы
(`pb.NewXxxClient(conn)`). Соединение ленивое; повторное использование одного
`Conn` для всех вызовов к таргету — норма (мультиплексирование HTTP/2).

### `PerRPCCredentials` (bearer)

Bearer-кредл — приватная реализация `credentials.PerRPCCredentials`: на каждый RPC
кладёт `authorization: Bearer <token>`. Подключается только при непустом `Token`.
`RequireTransportSecurity()` возвращает значение `TLS` — то есть при `TLS=false`
per-RPC creds разрешены поверх plaintext (внутренний mesh, где TLS терминируется
на sidecar).

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
	grpcclient.Config{Target: "passthrough:///bufnet", Token: "tok"}, log,
	grpcclient.WithDialOptions(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})),
)
```

Так тесты кита проверяют, что `authorization: Bearer tok` доезжает до серверного
интерсептора, и уровни access-log по коду ответа.

## Инварианты и подводные камни

- **Ленивое соединение.** `Dial`/`Module` не открывают TCP-сокет — он
  устанавливается на первом RPC. Успешный `Dial` не гарантирует доступность
  таргета; ошибки connect'а приходят на вызове. Для немедленной проверки —
  `conn.Connect()` + `conn.WaitForStateChange`.
- **Lifecycle соединения.** Под `Module` закрытие делает fx (`OnStop` → `conn.Close()`).
  При прямом `Dial` закрывать соединение — обязанность вызывающего (`defer conn.Close()`).
- **Bearer из env.** Токен берётся из `GRPC_CLIENT_TOKEN`. Per-RPC creds
  подключаются только при непустом токене.
- **Bearer поверх plaintext — риск.** При `GRPC_CLIENT_TLS=false` транспорт
  plaintext, а `RequireTransportSecurity()` = `false` — токен едет по
  незашифрованному каналу и читается любым узлом на пути. Кит допускает это
  только для внутреннего mesh с mTLS на sidecar и напоминает об этом `Warn`-логом
  при `Dial`. За пределами доверенной сети ставьте `GRPC_CLIENT_TLS=true`.
- **Один target на `Module`.** `Module` провайдит ровно один `*grpc.ClientConn` к
  `GRPC_CLIENT_TARGET`. Несколько downstream-целей — `Dial` напрямую (fx не
  различает несколько `*grpc.ClientConn` без именованных групп).
- **Keepalive.** `PermitWithoutStream: true` — ping'и идут даже без активных
  стримов; держит соединение живым через идл-таймауты промежуточных прокси.
  Слишком агрессивный `GRPC_CLIENT_KEEPALIVE_TIME` сервер может отбить политикой
  `EnforcementPolicy` (`too_many_pings`) — согласуйте с серверным `grpckit`.
