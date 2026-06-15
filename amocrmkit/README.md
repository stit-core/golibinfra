# amocrmkit

Чистый транспортный клиент к amoCRM: REST API v4 (Bearer), amoJo Chat API
(HMAC-SHA1), OAuth2, per-account rate-limit и таксономия ошибок. Stateless: ни
persistence, ни NATS, ни outbox, ни бизнес-логики, ни мультиаккаунт-оркестрации —
это слой gateway-сервиса. Один `Client` обслуживает все аккаунты: координаты
аккаунта (subdomain/tld/token, channel-secret, client_id/secret) приходят
аргументами вызовов, а не глобальной конфигурацией. Обёртка над `httpclient`
(retry + circuit-breaker + OTel), `clockkit` и `logkit`.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/amocrmkit"
```

Версия и `go get` — см. [корневой README](../README.md) § Использование в проекте.

## Быстрый старт

Через fx: `Module` provide'ит `*Client`, забирая из графа `Config`,
`*httpclient.Client`, `clockkit.Clock` и `logkit.Logger`. `Config` регистрирует
caller (Module его не provide'ит).

```go
app := fx.New(
    httpclient.Module,
    clockkit.Module,
    logkit.Module,
    amocrmkit.Module,
    fx.Provide(amocrmkit.NewConfig),
    fx.Invoke(func(c *amocrmkit.Client) {
        acc := amocrmkit.Account{Subdomain: "mycompany", TLD: "ru", Token: accessToken}
        page, err := c.ListLeads(ctx, acc, amocrmkit.ListParams{Limit: 50})
        _ = page
        _ = err
    }),
)
```

Прямой `New` без fx:

```go
c, err := amocrmkit.New(amocrmkit.Config{RateRPS: 7}, hc, clockkit.Real{}, logkit.Nop())
if err != nil { /* ... */ }
acc := amocrmkit.Account{Subdomain: "mycompany", TLD: "ru", Token: accessToken}
leads, err := c.CreateLeads(ctx, acc, []amocrmkit.LeadInput{{Name: "Сделка"}})
```

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство, контуры (REST v4 /
  amoJo / OAuth / Drive), generic-обёртки, потоки данных, ключевые решения.
- [docs/configuration.md](docs/configuration.md) — справочник `Config` и
  envconfig-тегов (public API), тюнинг.
- [docs/observability.md](docs/observability.md) — единый источник правды по
  метрикам и спанам.
- [docs/usage.md](docs/usage.md) — рецепты: подключение через fx и `New`, REST/amoJo/
  OAuth/Drive-сценарии, обработка ошибок, верификация вебхуков.
- [docs/api.md](docs/api.md) — карта экспортируемых типов и методов по контурам.
- [docs/testing.md](docs/testing.md) — как тестировать консьюмера поверх kit'а.
