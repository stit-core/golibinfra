# hubkit

Клиент центрального хаба экосистемы stit: единая SSO-сессия на все сервисы,
выпуск этой сессии, типизированный доступ к hub API и pull-синк локальных
проекций. Сабпакеты подключаются независимо: у `sso`, `issuer`, `client` и
`sync` — свой `Module` (fx).

| Сабпакет | Роль | Кто использует |
|---|---|---|
| `hubkit/sso` | verify hub-session JWT обоих контуров (ES256/JWKS), `Identity` в ctx, extractor и security-схемы для `httpkit` | все продукты |
| `hubkit/issuer` | выпуск hub-session JWT (ES256, три слота ключей), JWKS-роут, cookie-слой, rotating-refresh с reuse-detection поверх `cachekit` | issuer'ы контуров: employee-backend (`id.stit.tech`), company-backend (`api.hub.stit.tech`) |
| `hubkit/client` | HTTP-клиент hub API поверх `httpclient` (companies / projects / employees / visibility / tags / entity-tags / departments + sync-фиды) | продукты и движок синка |
| `hubkit/sync` | движок pull-синка проекций: цикл `after=<cursor>`, порты `ProjectionStore`/`CursorStore`, метрики, `timerkit.Job` | продукты с локальными проекциями |
| `hubkit/projection` | эталон схемы проекций: миграции + sqlc-запросы (`embed.FS` и `pgkit.Source`) | продукты |
| `hubkit/hubclaims` | контракт hub-session JWT: имена claim'ов и контуры (`Kind*`); без зависимостей, поэтому issuer не тянет клиентский контур верификации | `sso` и `issuer`, свои issuer'ы контуров |

Сессия одна на всю экосистему: cookie с `Domain=.stit.tech`, которую выпускает
issuer контура и локально проверяет каждый продукт. По сети продукт ходит только
за JWKS (`sso`) и по hub API (`client`/`sync`) — на горячем пути хаба нет.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/hubkit/sso"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт (продукт)

```go
package main

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/client"
	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func main() {
	fx.New(
		fx.Provide(logkit.NewConfig, httpclient.NewConfig, httpkit.NewConfig),
		logkit.Module, httpclient.Module, httpkit.Module,

		fx.Provide(sso.NewConfig), // HUB_SSO_* из ENV + Validate на старте
		sso.Module,                // схемы обоих контуров, у каждой — extractor своего cookie

		fx.Provide(client.NewConfig), // HUB_BASE_URL, HUB_SERVICE_TOKEN, …
		client.Module,                // *client.Client поверх httpclient
	).Run()
}
```

Дальше операция huma объявляет `Security: []map[string][]string{{cfg.SchemeName(): {}}}`,
а доменный код берёт принципала гейтом контура:

```go
id, err := sso.RequireEmployee(ctx) // ErrNoIdentity → 401, ErrWrongKind → 403
if !id.CanUseProduct("deal") { /* заказчику продукт не открыт */ }
```

## Инварианты

- **Три барьера между контурами.** `iss`, `aud` и claim `kind` проверяются
  всегда; `HUB_SSO_ISSUER` и `HUB_SSO_AUDIENCE` обязательны, customer-токен без
  `company_id` отвергается. Токен чужого контура не проходит ни в один слот.
- **Только ES256.** Allowlist из одного алгоритма: продукт держит лишь публичный
  ключ. Токен без `exp` не принимается; `exp`/`nbf`/`iat` — с допуском
  `HUB_SSO_LEEWAY`.
- **Ротация ключа не рвёт сессии.** У issuer'а слоты previous / active / next,
  все публикуются в JWKS; на стороне продукта выведенный `kid` живёт ещё
  `HUB_SSO_KEY_GRACE`. Набор перечитывается по `Cache-Control: max-age` ответа
  JWKS, поэтому окно отзыва ключа ограничено:
  `HUB_SESSION_JWKS_MAX_AGE` + `HUB_SSO_KEY_GRACE`.
- **Refresh — rotating с reuse-detection.** В кэше только `sha256` токенов;
  повтор внутри `HUB_REFRESH_REUSE_GRACE` (минимум 1s) — параллельный обмен
  (`ErrRefreshInFlight`, семейство живо), позже — утечка (отзыв всей цепочки),
  в том числе для клиента, потерявшего ответ обмена. Ошибка кэша — отказ обмена;
  отзыв семейства переживает обрыв соединения.
- **Конфиг валидируется на старте.** У всех конфигов кита есть `Validate()` и
  sentinel `ErrConfig`: потерянная переменная деплоя роняет приложение, а не
  превращается в «хаб недоступен» и 401.
- **Проекции наполняются только синком.** Write-through идёт в hub API;
  курсор монотонен, апсерт идемпотентен по id, catch-up после простоя
  автоматический.
- **Health-проб кит не отдаёт** — осознанно: продукт обязан переживать простой
  хаба (см. [docs/observability.md](docs/observability.md)).

## Документация

| Документ | О чём |
|---|---|
| [docs/architecture.md](docs/architecture.md) | два контура, проверка токена, процедура ротации ключа, rotating refresh, retry-safety клиента, движок синка, расхождения шаблона проекций |
| [docs/configuration.md](docs/configuration.md) | все `HUB_*` с дефолтами и правилами `Validate` |
| [docs/observability.md](docs/observability.md) | метрики, labels, значения `result`, почему нет health-проб |
| [docs/usage.md](docs/usage.md) | рецепты: продукт, составной extractor, issuer, refresh-flow, клиент, синк, схема проекций |
| [docs/testing.md](docs/testing.md) | `testkit/hubtest`, детерминизм на `clockkit`, что покрыто регрессиями |

## Контракт claim'ов hub-session JWT

Общие: `sub`, `name`, `email`, `sid` + стандартные `iss`/`aud`/`exp`/`iat`/`nbf`.
Сотрудник: `sub` — Authentik subject, плюс `employee_id` (hub UUID), `username`,
`role`. Заказчик: `sub` — `customer_account_id`, плюс `kind=customer`,
`company_id` (hub UUID компании), `account_role` (`member|admin` внутри своей
компании), `products` (массив: `deal`, `telemost`, `task`, `calendar`).

`kind` — дискриминатор контура: у заказчика обязателен, у сотрудника
опционален (пустой читается как `employee`, `Identity.Kind` при этом всегда
заполнен); `kind=customer` без `company_id` не выпускается и не принимается.
Имена нестандартных claim'ов и значения `kind` — константы `hubkit/hubclaims`,
общие для стороны выпуска и стороны проверки; `sso.Claim*`/`sso.Kind*` —
их реэкспорт для продуктов, `issuer.Session.Claims()` собирает карту по ним.
