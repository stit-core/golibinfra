# hubkit

Клиент центрального хаба экосистемы stit: единая SSO-сессия на все сервисы,
типизированный доступ к hub API и pull-синк локальных проекций. Пять
подпакетов, подключаются независимо через `Module` (fx).

| Подпакет | Роль | Кто использует |
|---|---|---|
| `hubkit/sso` | verify hub-session JWT (ES256/JWKS), `Identity` в ctx, `AuthExtractor` для huma | все продукты |
| `hubkit/issuer` | выпуск hub-session JWT (ES256 + ротация kid), JWKS-handler, cookie-слой, rotating-refresh (Redis) | только employee-backend (`id.stit.tech`) |
| `hubkit/client` | типизированный HTTP-клиент hub API поверх `httpclient` (companies/projects/employees/visibility/sync) | продукты + движок синка |
| `hubkit/sync` | движок pull-синка проекций: цикл `after=<cursor>`, порты `ProjectionStore`/`CursorStore`, метрики | продукты с локальными проекциями |
| `hubkit/projection` | эталон схемы проекций (миграции + sqlc-запросы) как copy-paste-шаблон | продукты (не рантайм) |

Сквозная архитектура — `plan/00-architecture.md` (§3 SSO, §4 sync).

## SSO (продукт верифицирует локально)

```go
fx.Provide(sso.NewConfig)   // HUB_SSO_* из ENV
sso.Module                  // Verifier + Extractor + security-scheme HubSession
// httpkit подхватит sso.AuthExtractor; операции защищаются
//   Security: []map[string][]string{{sso.SecuritySchemeName: {}}}
```

Cookie `stit_session` → ES256-verify по JWKS `id.stit.tech` (кэш в памяти,
lazy-refresh по неизвестному kid) → `sso.Identity{EmployeeID, Subject, Username,
Name, Email, Role, SID}` в ctx. Ни одного похода по сети на запрос после
первичной загрузки JWKS — продукт живёт при недоступном хабе.

Login-redirect: `cfg.LoginURLFor(returnURL)` → `https://id.stit.tech/auth/login?rd=…`.

## Issuer (employee-backend)

```go
fx.Provide(issuer.NewKeyConfig, issuer.NewCookieConfig, issuer.NewRefreshConfig,
    issuer.NewCacheRefreshStore) // поверх cachekit.Cache
issuer.Module                    // Signer + RefreshManager + JWKS-роут
```

`Signer.Sign(claims, ttl)` подписывает ES256 активным ключом; `JWKS()` отдаёт
активный + next (ротация без разрыва сессий). Cookie: `Domain=.stit.tech`,
HttpOnly, Secure, SameSite=Lax, TTL 15 мин. `RefreshManager`: rotating refresh
(one-time токены) + tombstone по `sid` на логаут.

## Client (m2m)

```go
fx.Provide(client.NewConfig)
client.Module   // *client.Client поверх httpclient.Client
```

Bearer `HUB_SERVICE_TOKEN`. Ошибки типизированы: `ErrNotFound` / `ErrConflict` /
`ErrUnauthorized` / `ErrUnavailable` (последняя = деградация чтения на локальную
проекцию, не бизнес-ошибка).

## Sync (движок проекций)

Продукт реализует порты `ProjectionStore` (upsert батча в проекцию, идемпотентно
по id) и `CursorStore` (таблица `hub_sync_cursors`), собирает по `NewEngine` на
ресурс и кладёт их в group `hubkit.sync.engine`:

```go
fx.Provide(sync.NewConfig)
sync.Module            // HTTPFetcher + разворот движков в timer.job
timerkit.Module        // крутит job'ы с graceful shutdown
// продукт: fx.Provide(fx.Annotate(newCompaniesEngine, fx.ResultTags(`group:"hubkit.sync.engine"`)), …)
```

Тик догоняет фид страницами `after=<cursor>&limit=`; курсор монотонен, catch-up
после простоя автоматический.

## Projection (эталон схемы)

`templates/*.sql` (миграции `hub_companies`/`hub_projects`/`hub_employees`/
`hub_entity_visibility` + `hub_sync_cursors`) и `queries/*.sql` (sqlc-эталон:
upsert из фида + листинги) — копируются в продукт, чтобы схема проекций не
расходилась. Доступны и как `embed.FS` (`MigrationsFS`, `QueriesFS`).

## Конфигурация

| ENV | Подпакет | Назначение |
|---|---|---|
| `HUB_SSO_JWKS_URL` | sso | JWKS employee-backend (обязателен) |
| `HUB_SSO_COOKIE` | sso | имя cookie сессии (default `stit_session`) |
| `HUB_SSO_ISSUER` / `HUB_SSO_AUDIENCE` | sso | ожидаемые iss/aud (aud — если задан) |
| `HUB_SSO_LOGIN_URL` | sso | точка входа для login-redirect |
| `HUB_SESSION_PRIVATE_KEY[_FILE]` | issuer | активный ES256-ключ (PEM inline/файл) |
| `HUB_SESSION_NEXT_PRIVATE_KEY[_FILE]` | issuer | next-ключ на период ротации |
| `HUB_SESSION_COOKIE[_DOMAIN\|_SECURE]`, `HUB_SESSION_TTL` | issuer | cookie-слой |
| `HUB_REFRESH_TTL`, `HUB_REFRESH_TOMBSTONE_TTL` | issuer | rotating-refresh |
| `HUB_BASE_URL`, `HUB_EMPLOYEE_BASE_URL` | client | базовые URL company/employee backend |
| `HUB_SERVICE_TOKEN`, `HUB_SERVICE_NAME` | client | m2m Bearer + имя сервиса |
| `HUB_SYNC_INTERVAL`, `HUB_SYNC_LIMIT` | sync | период и размер страницы pull-тика |

## Observability

`hubkit/sync` эмитит `hub_sync_records_total{resource}`,
`hub_sync_errors_total{resource}`, `hub_sync_cursor{resource}`,
`hub_sync_last_success_timestamp_seconds{resource}`,
`hub_sync_lag_seconds{resource}`. JWKS-загрузки sso/issuer идут через
`httpclient` (его метрики). Без `MeterProvider` инструменты — no-op.

## Контракт claim'ов hub-session JWT

`sub` (Authentik subject), `employee_id` (hub UUID), `username`, `name`,
`email`, `role`, `sid`, стандартные `iss`/`aud`/`exp`/`iat`/`nbf`. Имена
нестандартных claim'ов — константы `sso.Claim*` (общий контракт issuer↔sso).
