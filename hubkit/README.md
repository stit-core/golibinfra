# hubkit

Клиент центрального хаба экосистемы stit: единая SSO-сессия на все сервисы,
типизированный доступ к hub API и pull-синк локальных проекций. Пять
подпакетов, подключаются независимо через `Module` (fx).

| Подпакет | Роль | Кто использует |
|---|---|---|
| `hubkit/sso` | verify hub-session JWT обоих контуров (ES256/JWKS), `Identity` в ctx, `AuthExtractor` для huma | все продукты |
| `hubkit/issuer` | выпуск hub-session JWT (ES256 + ротация kid), JWKS-handler, cookie-слой, rotating-refresh с reuse-detection (поверх `cachekit`) | issuer'ы контуров: employee-backend (`id.stit.tech`), company-backend (`api.hub.stit.tech`) |
| `hubkit/client` | типизированный HTTP-клиент hub API поверх `httpclient` (companies/projects/employees/visibility/tags/entity-tags/departments + sync) | продукты + движок синка |
| `hubkit/sync` | движок pull-синка проекций: цикл `after=<cursor>`, порты `ProjectionStore`/`CursorStore`, метрики | продукты с локальными проекциями |
| `hubkit/projection` | эталон схемы проекций (миграции + sqlc-запросы) как copy-paste-шаблон | продукты (не рантайм) |

Сессия одна на всю экосистему: cookie с `Domain=.stit.tech`, которую выпускает
issuer контура и локально проверяет каждый продукт. Продукт ходит в хаб только
за JWKS (sso) и по hub API (client/sync); проекции наполняются pull-синком.

## SSO (продукт верифицирует локально)

```go
fx.Provide(sso.NewConfig)   // HUB_SSO_* из ENV
sso.Module                  // Verifier + CustomerVerifier + Extractor + security-scheme HubSession
// httpkit подхватит sso.AuthExtractor; операции защищаются
//   Security: []map[string][]string{{sso.SecuritySchemeName: {}}}
```

Cookie слота → ES256-verify по JWKS своего контура → `sso.Identity` в ctx.
По сети — только загрузка JWKS: лениво при первом запросе и по неизвестному
`kid`, не чаще раза в минуту на верификатор (параллельные промахи сливаются в
одну загрузку), с таймаутом 10s. Уже загруженные ключи работают при недоступном
хабе, поэтому продукт живёт при его простое.

### Два контура identity

| | Сотрудник | Заказчик |
|---|---|---|
| `Kind` | `employee` (пустой claim нормализуется в него) | `customer` |
| Issuer / `iss` | employee-backend, `https://id.stit.tech` | company-backend, `https://api.hub.stit.tech` |
| JWKS | `HUB_SSO_JWKS_URL` | `HUB_SSO_CUSTOMER_JWKS_URL` |
| `aud` | `HUB_SSO_AUDIENCE` | `HUB_SSO_CUSTOMER_AUDIENCE` |
| Cookie | `stit_session` (`HUB_SSO_COOKIE`) | `stit_customer` (`HUB_SSO_CUSTOMER_COOKIE`) |
| Ключ подписи | ES256 employee-backend | свой ES256 company-backend |
| Поля `Identity` | `EmployeeID`, `Username`, `Role` | `CompanyID`, `AccountRole`, `Products` |
| Вход | Authentik (OIDC BFF) | логин + пароль на `hub.stit.tech/customer/login` |

Общее: `Domain=.stit.tech`, HttpOnly, Secure, SameSite=Lax, access TTL ~15 мин.

**`aud` обязателен.** Сессия одна на все продукты, поэтому `aud` — идентификатор
контура, а не продукта: `HUB_SSO_AUDIENCE` равен `HUB_SESSION_AUDIENCE`
issuer'а сотрудника (employee-backend), `HUB_SSO_CUSTOMER_AUDIENCE` —
`HUB_SESSION_AUDIENCE` issuer'а заказчика (company-backend). Токен без `aud` или с
чужим `aud` отвергается; верификатор без audience в конфиге не собирается
(`ErrAudience` / `ErrCustomerAudience`).

**Приоритет — за сотрудником.** Слоты независимы (сотрудник и заказчик могут
оказаться в одном браузере — общая cookie молча выбивала бы один вход другим);
при обеих заполненных выигрывает `stit_session`. Протухший employee-токен не
отменяет валидную сессию заказчика — берётся customer-слот. Чтобы посмотреть на
мир глазами заказчика, сотрудник использует отдельный профиль браузера.

**Токен чужого контура не принимается никогда.** Верификатор слота сверяет `iss`,
`aud` и claim `kind`: employee-токен в `stit_customer` (и наоборот) отвергается,
даже если подпись сошлась.

**`Kind` проверяется до применения любой доменной политики** — правила,
написанные под сотрудника, к заказчику не применимы. Гейты вместо сравнения
строк руками:

```go
id, err := sso.RequireEmployee(ctx) // ErrNoIdentity → 401, ErrWrongKind → 403
id, err := sso.RequireCustomer(ctx)
if !id.CanUseProduct("deal") { … }  // сотруднику — всегда true
```

Контур заказчика включается заданием `HUB_SSO_CUSTOMER_JWKS_URL` (тогда
обязательны `HUB_SSO_CUSTOMER_ISSUER` и `HUB_SSO_CUSTOMER_AUDIENCE`); пусто —
продукт работает ровно как без второго контура (продукты входят в него не
одновременно). Login-redirect: `cfg.LoginURLFor(returnURL)` /
`cfg.CustomerLoginURLFor(returnURL)` → `…?rd=…`.

## Issuer (employee-backend, company-backend)

```go
fx.Provide(issuer.NewKeyConfig, issuer.NewCookieConfig, issuer.NewRefreshConfig)
cachekit.Module                  // cachekit.Cache для refresh-состояния (Redis)
issuer.Module                    // Signer + RefreshManager + JWKS-роут
```

`Signer.Sign(claims, ttl)` подписывает ES256 активным ключом (только P-256;
другая кривая — `ErrNotEC` на старте), всегда ставит `iss`/`aud`
(`HUB_SESSION_AUDIENCE` обязателен — `ErrNoAudience`) и `exp`/`iat`/`nbf`.
`JWKS()` отдаёт активный + next (ротация без разрыва сессий); next-ключ можно
задать публичным PEM (`openssl ec -in next.pem -pubout`) — приватный ключ
следующего kid инстансу не нужен. JWKS-ответ (`RegisterJWKS` / `JWKSHandler`)
несёт `Cache-Control: public, max-age=300`.

`SessionCookie`: `Domain=.stit.tech`, HttpOnly, Secure, SameSite=Lax, TTL 15 мин;
`RefreshCookie` — host-only (Domain пуст) на своём `Path`, чтобы refresh-токен
видели только роуты своего issuer'а. Имена cookie и `Path` refresh'а — из
`CookieConfig`, не константы: контуров два, и issuer заказчика поднимается со
своими (`stit_customer` / `stit_customer_refresh`, `Path=/auth/customer`).

`RefreshManager` (поверх `cachekit.Cache`, `NewRefreshManager(cache, cfg)`):
rotating refresh — `Rotate` атомарно гасит старый токен (`GetDel`) и выдаёт
новый; в кэше лежат только `sha256` токенов. Повторное предъявление уже
погашенного токена — признак утечки: сессия (`sid`) отзывается целиком, включая
свежий токен, и `Rotate` возвращает `ErrRefreshReused`. Логаут — `Revoke(sid)`
(tombstone), после него любой refresh цепочки → `ErrRefreshRevoked`.
`HUB_REFRESH_TOMBSTONE_TTL` обязан быть ≥ `HUB_REFRESH_TTL` (`ErrRefreshConfig`).
Гонка двух одновременных `Rotate` с одним токеном: проходит один, второй получает
`ErrRefreshInvalid` (маркер reuse ещё не записан) — оба исхода отказные.

## Client (m2m)

```go
fx.Provide(client.NewConfig)
client.Module   // *client.Client поверх httpclient.Client (опционален в графе)
```

Bearer `HUB_SERVICE_TOKEN`. Без `*httpclient.Client` в графе клиент собирает
свой: таймаут 10s, retry только идемпотентных методов, breaker на хост. Каждый
запрос идёт с дедлайном: ctx без дедлайна получает 30s на все попытки. Ошибки
типизированы: `ErrNotFound` / `ErrConflict` / `ErrUnauthorized` / `ErrUnavailable`
(последняя = деградация чтения на локальную проекцию, не бизнес-ошибка).

Базы роутятся по ресурсу (единый клиент, три backend'а):

| Метод(ы) | Backend (env) | Контракт |
|---|---|---|
| `ListCompanies`/`GetCompany`/`CreateCompany`/… `*Project*` / `*Visibility*` | company (`HUB_BASE_URL`) | CRUD + write-through видимости |
| `GetEntityTags(kind,id)` / `ReplaceEntityTags(kind,id,names)` | company (`HUB_BASE_URL`) | глобальная принадлежность тегам company/project (`GET/PUT /api/tags/{kind}/{id}`, replace) |
| `ListEmployees`/`GetEmployee` | employee (`HUB_EMPLOYEE_BASE_URL`) | чтение сотрудников; `Employee.DepartmentIDs[]` — snapshot M2M |
| `GetEmployeeDepartments(id)` / `ReplaceEmployeeDepartments(id,names)` | employee (`HUB_EMPLOYEE_BASE_URL`) | департаменты сотрудника (`GET/PUT /api/employees/{id}/departments`, replace) |
| `UpsertDepartment(name)` | employee (`HUB_EMPLOYEE_BASE_URL`) | write-through в глобальный словарь департаментов (`POST /api/departments`, upsert по norm) |
| `UpsertTag(name)` | tag (`HUB_TAG_BASE_URL`) | write-through в глобальный словарь тегов |

Write-through-методы принадлежности (`ReplaceEntityTags`/`ReplaceEmployeeDepartments`)
шлют `[]` при `nil` наборе (снятие всех связей), не `null`.

Реквизиты компании асимметричны: `Company` (read) отдаёт их плоскими полями,
`CompanyInput` (write) принимает только вложенными — `Requisites
*CompanyRequisitesInput` → ключ `requisites`. Схема create/update у хаба строгая
(`additionalProperties:false`), лишнее поле верхнего уровня → 4xx.

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

Ресурсы (`client.Resource*`, роутятся на свой backend): `companies` / `projects`
/ `visibility` / `entity-tags` (company base), `employees` / `departments`
(employee base), `tags` (tag base). Движок generic по `json.RawMessage` — типа
элемента не знает; продукт `Upsert`'ит батч, unmarshal'я элемент в нужный тип
(`Company`, `EntityTagsSnapshot`, `Department`, …). Курсор двигается по
`updated_seq` (есть у всех ресурсов), так что snapshot-ресурсы работают тем же
движком без расширений.

**Snapshot-per-entity (`entity-tags`).** Элемент — `EntityTagsSnapshot{EntityKind,
EntityID, TagIDs[], UpdatedSeq}`: ключ `(kind,id)`, не `id`; **replace-семантика**
— продукт в `Upsert` заменяет весь набор тегов сущности на `TagIDs` (пустой
набор `[]` снимает все). `department_ids[]` сотрудника — тоже snapshot (replace
набора в проекции employees).

## Projection (эталон схемы)

`templates/*.sql` (миграции `hub_companies`/`hub_projects`/`hub_employees`/
`hub_entity_visibility` + `hub_sync_cursors`) и `queries/*.sql` (sqlc-эталон:
upsert из фида + листинги) — копируются в продукт, чтобы схема проекций не
расходилась. Доступны и как `embed.FS` (`MigrationsFS`, `QueriesFS`). Шаблон
покрывает companies/projects/employees/visibility; проекции `departments`,
`tags` и `entity-tags` продукт описывает сам по типам `client.*`.

## Конфигурация

| ENV | Подпакет | Назначение |
|---|---|---|
| `HUB_SSO_JWKS_URL` | sso | JWKS employee-backend (обязателен) |
| `HUB_SSO_COOKIE` | sso | имя cookie сессии сотрудника (default `stit_session`) |
| `HUB_SSO_ISSUER` | sso | ожидаемый iss сотрудника (default `https://id.stit.tech`) |
| `HUB_SSO_AUDIENCE` | sso | ожидаемый aud сотрудника (обязателен; = `HUB_SESSION_AUDIENCE` employee-backend) |
| `HUB_SSO_LOGIN_URL` | sso | точка входа для login-redirect |
| `HUB_SSO_CUSTOMER_JWKS_URL` | sso | JWKS company-backend; **пусто → контур заказчика выключен** |
| `HUB_SSO_CUSTOMER_COOKIE` | sso | имя cookie сессии заказчика (default `stit_customer`) |
| `HUB_SSO_CUSTOMER_ISSUER` | sso | ожидаемый iss заказчика (default `https://api.hub.stit.tech`; при включённом контуре обязателен) |
| `HUB_SSO_CUSTOMER_AUDIENCE` | sso | ожидаемый aud заказчика (при включённом контуре обязателен; = `HUB_SESSION_AUDIENCE` company-backend) |
| `HUB_SSO_CUSTOMER_LOGIN_URL` | sso | экран входа заказчика (default `https://hub.stit.tech/customer/login`) |
| `HUB_SESSION_PRIVATE_KEY`, `HUB_SESSION_PRIVATE_KEY_FILE` | issuer | активный ES256-ключ P-256: PEM inline либо путь к файлу (файл приоритетнее) |
| `HUB_SESSION_NEXT_PRIVATE_KEY`, `HUB_SESSION_NEXT_PRIVATE_KEY_FILE` | issuer | next-ключ на период ротации: приватный или публичный PEM (inline/файл) |
| `HUB_SESSION_ISSUER`, `HUB_SESSION_AUDIENCE` | issuer | iss/aud выпускаемых токенов (aud обязателен) |
| `HUB_SESSION_COOKIE`, `HUB_SESSION_COOKIE_DOMAIN`, `HUB_SESSION_COOKIE_SECURE`, `HUB_SESSION_TTL` | issuer | cookie-слой access-сессии (default `stit_session` / `.stit.tech` / `true` / `15m`) |
| `HUB_SESSION_REFRESH_COOKIE`, `HUB_SESSION_REFRESH_COOKIE_PATH` | issuer | имя и `Path` refresh-cookie (default `stit_refresh` / `/auth`) |
| `HUB_REFRESH_TTL`, `HUB_REFRESH_TOMBSTONE_TTL` | issuer | rotating-refresh (tombstone ≥ TTL) |
| `HUB_BASE_URL`, `HUB_EMPLOYEE_BASE_URL`, `HUB_TAG_BASE_URL` | client | базовые URL company/employee/tag backend (пусто → падают на `HUB_BASE_URL`) |
| `HUB_SERVICE_TOKEN`, `HUB_SERVICE_NAME` | client | m2m Bearer + имя сервиса |
| `HUB_SYNC_INTERVAL`, `HUB_SYNC_LIMIT` | sync | период и размер страницы pull-тика |

## Observability

Без `MeterProvider` инструменты — no-op.

| Метрика | Подпакет | Labels | Назначение |
|---|---|---|---|
| `hub_sso_verifications_total` | sso | `contour` = `employee` \| `customer`, `result` | исход `Verify` слота |
| `hub_sso_jwks_refresh_total` | sso | `contour`, `result` = `ok` \| `error` | фактические загрузки JWKS |
| `hub_sync_records_total` | sync | `resource` | записи, применённые в проекцию |
| `hub_sync_errors_total` | sync | `resource` | неудачные тики |
| `hub_sync_cursor` | sync | `resource` | текущий курсор |
| `hub_sync_last_success_timestamp_seconds` | sync | `resource` | время последнего успешного catch-up |
| `hub_sync_lag_seconds` | sync | `resource` | время с последнего успешного catch-up |

Значения `result` у `hub_sso_verifications_total`: `ok`, `malformed`,
`unknown_key`, `jwks_error`, `signature_invalid`, `invalid_claims`
(iss/aud/exp/nbf/sub), `wrong_kind`. Запросы `client` идут через `httpclient`
(его метрики и спаны).

## Контракт claim'ов hub-session JWT

Общие: `sub`, `name`, `email`, `sid`, стандартные `iss`/`aud`/`exp`/`iat`/`nbf`.
Сотрудник: `sub` — Authentik subject, плюс `employee_id` (hub UUID), `username`,
`role`. Заказчик: `sub` — `customer_account_id`, плюс `kind=customer`,
`company_id` (hub UUID компании), `account_role` (`member|admin` внутри своей
компании), `products` (массив: `deal`, `telemost`, `task`, `calendar`).

`kind` — дискриминатор контура: у заказчика обязателен, у сотрудника
опционален (пустой читается как `employee`, `Identity.Kind` при этом всегда
заполнен). Имена нестандартных claim'ов — константы `sso.Claim*` (общий контракт
issuer↔sso).
