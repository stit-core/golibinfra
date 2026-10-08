# Тестирование grpckit

## testkit/grpctest — bufconn-харнесс

Сабпакет `git.stit.tech/stit-core/golibinfra/testkit/grpctest` поднимает
`grpckit.Server` in-process поверх `bufconn` (без реального порта) и отдаёт
готовый `*grpc.ClientConn`. Сервер, листенер и соединение закрываются в
`t.Cleanup`.

```go
func StartServer(t testing.TB, srv *grpckit.Server) *grpc.ClientConn
```

### Рецепт: тест потребителя

```go
func TestGreeter(t *testing.T) {
	srv, err := grpckit.New(
		grpckit.Config{ShutdownGrace: time.Second}, // остальное — дефолты
		logkit.Nop(),
		grpckit.WithService(func(s *grpc.Server) {
			greeterpb.RegisterGreeterServer(s, &greeterServer{})
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn := grpctest.StartServer(t, srv)
	client := greeterpb.NewGreeterClient(conn)
	// … вызовы client, проверки …
}
```

`grpckit.Config{}` валиден: лимиты сообщений, keepalive и грейс получают дефолты.
Короткий `ShutdownGrace` полезен тестам с висящими стримами — `t.Cleanup` не
ждёт полный дефолтный грейс.

Соединение использует `insecure` транспорт и passthrough-резолвер на bufconn —
TLS/реальный сетевой стек в тесте не задействуются.

## Тесты с auth

`WithAuthenticator` принимает любую реализацию порта — в тестах удобен стаб,
обогащающий `ctx`. Bearer-токен передаётся через outgoing metadata-ключ
`authorization`:

```go
ctx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer tok"))
```

## Проверка логов

Уровни и поля access-log проверяются `testkit/logtest` — боевой логгер
`logkit.New`, который пишет записи в память:

```go
rec := logtest.Capture()
conn := grpctest.StartServer(t, mustNewServer(t, rec))
// … вызов …
if e, ok := rec.Find(slog.LevelWarn, "grpc call failed"); !ok || e.Str("code") != "NotFound" {
	t.Fatalf("access log = %+v ok=%v", e, ok)
}
```

`request_id` и `trace_id` лежат в `Entry.KV`, как в строке лога: их добавляет
цепочка handler'ов logkit, через которую идёт и запись `logtest`.

## Health в тестах

Health-сервис доступен сразу: после `Serve` overall-статус = `SERVING`. Проверка —
стандартным health-клиентом:

```go
resp, _ := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
// resp.GetStatus() == healthpb.HealthCheckResponse_SERVING
```

Пробы зависимостей в тесте подключаются опцией `grpckit.WithHealthProbes(...)`
вместе с коротким `GRPC_HEALTH_INTERVAL` (`Config.HealthInterval`), чтобы
переоценка происходила за миллисекунды. Зависшую пробу ограничивает
`Config.HealthProbeTimeout` — тест на деградацию не ждёт следующего тика.

## Gate перед claim «готово»

Как в корневом CLAUDE.md — зелёные `golangci-lint run ./...`,
`go test -short -race ./...`, `go build ./...`. Тест keepalive-политики кита
идёт только в полном прогоне (`-short` его пропускает — он ждёт страйков пинга
десятки секунд).
