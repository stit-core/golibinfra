# golibinfra

Инфраструктурные библиотеки для Go-сервисов компании. Импортируется как Go-модуль из бизнес-репозиториев.

```go
import "git.stit.tech/akadmin/golibinfra/pgkit"
import "git.stit.tech/akadmin/golibinfra/httpkit"
import "git.stit.tech/akadmin/golibinfra/telemetry"
import "git.stit.tech/akadmin/golibinfra/config"
import "git.stit.tech/akadmin/golibinfra/testkit"
```

## Принципы

- **Никакой продуктовой специфики.** Если файл знает про конкретные роли, домены, имена сервисов — он не здесь.
- **Без скрытых зависимостей.** Каждый пакет можно подключать независимо.
- **fx-first.** Каждый пакет экспортирует `Module` для проводки.
- **Observability-by-default.** Все компоненты инструментированы OTel.

## Пакеты

| Пакет | Ответственность |
|---|---|
| [`pgkit`](pgkit/) | pgxpool, Transactor, migrate-runner, маппинг pg-ошибок |
| [`httpkit`](httpkit/) | Huma bootstrap, generic middlewares (recovery, request-id, auth-callback, otel) |
| [`telemetry`](telemetry/) | OTel init (trace/meter/log), slog с автокорреляцией trace_id, OTLP exporters |
| [`config`](config/) | Загрузчик конфигов: env + Vault с fallback |
| [`testkit`](testkit/) | testcontainers (postgres), httptest helpers |

## Версионирование

Семвер. Теги вида `v0.x.y`. До `v1.0.0` — API считается нестабильным, breaking changes допустимы в minor.

## Лицензия

Internal. Stit Tech.
