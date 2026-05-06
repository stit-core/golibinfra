# telemetry

OTel (trace + metric + log), slog с автокорреляцией trace_id, OTLP-экспортёры.

## Что даёт

- `Init(ctx, cfg)` → `*Providers` с тремя провайдерами (trace/meter/log) и `Shutdown(ctx)`
- `NewSlogger(level)` → `*slog.Logger`, который сам кладёт `trace_id`/`span_id` в каждую запись из активного спана
- `Span(ctx, name)` → `(ctx, end)` — единственный санкционированный способ добавить вручную спан в бизнес-коде
- `Module` — fx-модуль: `*Providers`, `*slog.Logger`, `trace.Tracer`, `metric.Meter`

## Использование

```go
// в module.go домена
fx.Module("user",
    fx.Provide(NewUsecase),
    // ...
)

// в usecase.go
func (uc *Usecase) Register(ctx context.Context, in *Create) (u *User, err error) {
    ctx, end := telemetry.Span(ctx, "user.register",
        attribute.String("user.email", in.Email))
    defer end(&err)
    // ...
}
```

## Конфигурация

Через ENV (см. `Config`). Главное — `OTEL_EXPORTER_OTLP_ENDPOINT` (например, `jaeger:4318`).
Для локальной разработки без коллектора: `OTEL_STDOUT_ONLY=true`.
