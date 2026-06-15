# Тестирование grpckit

## testkit/grpctest — bufconn-харнесс

Сабпакет `git.stit.tech/stit-core/golibinfra/grpckit/testkit/grpctest` поднимает
`grpckit.Server` in-process поверх `bufconn` (без реального порта) и отдаёт
готовый `*grpc.ClientConn`. Сервер, листенер и соединение закрываются в
`t.Cleanup`.

```go
func StartServer(t *testing.T, srv *grpckit.Server) *grpc.ClientConn
```

### Рецепт: тест потребителя

```go
func TestGreeter(t *testing.T) {
	srv, err := grpckit.New(
		grpckit.Config{MaxRecvMiB: 4, MaxSendMiB: 4, ShutdownGrace: time.Second},
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

Соединение использует `insecure` транспорт и passthrough-резолвер на bufconn —
TLS/реальный сетевой стек в тесте не задействуются.

## Тесты с auth

`WithAuthenticator` принимает любую реализацию порта — в тестах удобен стаб,
обогащающий `ctx`. Bearer-токен передаётся через outgoing metadata-ключ
`authorization`:

```go
ctx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer tok"))
```

## Health в тестах

Health-сервис доступен сразу: после `Serve` overall-статус = `SERVING`. Проверка —
стандартным health-клиентом:

```go
resp, _ := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
// resp.Status == healthpb.HealthCheckResponse_SERVING
```

## Gate перед claim «готово»

Как в корневом CLAUDE.md — зелёные `golangci-lint run ./...`,
`go test -short -race ./...`, `go build ./...`.
