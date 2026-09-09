# oidckit

OIDC BFF-контур для SSO-входа: сервис сам ведёт Authorization-Code-flow
(+ PKCE S256), проверяет ID-token по JWKS и выдаёт собственную cookie-сессию —
браузер OIDC-токенов не видит.

Внутри два слоя одного пакета:

- **Relying-party** — `Verifier`: discovery `/.well-known/openid-configuration`,
  JWKS-кэш (refresh по неизвестному `kid` не чаще раза в минуту, параллельные
  промахи сливаются в один запрос), verify `iss`/`aud`/`azp`/`exp`/`nonce`
  (только асимметричные подписи: RS256/384/512, ES256/384), обмен authorization
  code; `PKCE()` / `RandomToken()`. Крипта — [`go-jose/v4`](https://github.com/go-jose/go-jose).
- **BFF-обвязка** — `BFF` + huma-роуты `GET <p>/login` (302 на idp),
  `GET <p>/callback` (exchange+verify → cookie-сессия), `GET <p>/me`,
  `POST <p>/logout`; `Extractor` (cookie → `authnkit.JWTParser` → `Identity`
  в ctx), совместимый с `httpkit.AuthExtractor`.

Сессия — HS256-JWT (`authnkit.HS256JWT`, секрет `JWT_SECRET`, не короче 32 байт)
c claim'ом `typ=oidc_session` в httpOnly-cookie (`__Host-access_token` при
`Secure`), TTL — `OIDC_SESSION_TTL` (default 12h). Короткоживущий state
логин-флоу (PKCE verifier / nonce / return-to; TTL 10m, one-time через атомарный
`GetDel`) живёт в `StateStore` поверх `cachekit.Cache`: Redis в проде,
`cachekit.NewMemoryCache()` в тестах и single-replica без Redis. Callback
привязан к браузеру flow-cookie `__Host-oidc_state`.

Kit не трактует роли и не знает продуктовых пользователей: маппинг claims →
роль/доп-claims сессии и JIT-провижининг — хуки консьюмера (`Hooks`).

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/oidckit"
```

Версия модуля — см. [`../README.md`](../README.md) (§ Версионирование).

## Быстрый старт

fx-проводка. `Module` — drop-in BFF; caller провайдит `Config`/`CookieConfig`
(env), `StateStore`, authnkit-подпись и `*httpclient.Client` (походы в idp идут
через него: retry, breaker на хост, спаны; без него граф не собирается):

```go
fx.New(
    fx.Provide(
        authnkit.NewJWTConfig,               // JWT_SECRET (+ JWT_LEEWAY)
        cachekit.NewConfig,                  // REDIS_* — под StateStore
        httpclient.NewConfig,                // HTTPCLIENT_*
        httpkit.NewConfig,                   // HTTP_*
        logkit.NewConfig,                    // LOG_*
        oidckit.NewConfig,                   // OIDC_*
        oidckit.NewCookieConfig,             // COOKIE_INSECURE, COOKIE_SAMESITE
        oidckit.NewCacheStateStore,          // StateStore поверх cachekit.Cache
        func() oidckit.Hooks {               // опционально
            return oidckit.Hooks{
                SessionClaims: func(c oidckit.Claims) map[string]any {
                    return map[string]any{"role": roleForGroups(c.Groups)}
                },
                OnLogin: func(ctx context.Context, c oidckit.Claims) error {
                    return users.SyncOnLogin(ctx, c) // JIT-провижининг
                },
            }
        },
    ),
    authnkit.Module,
    cachekit.Module,
    httpclient.Module,
    httpkit.Module,
    logkit.Module,
    oidckit.Module, // роуты /auth/*, security-scheme Cookie, AuthExtractor, health-проба, user_id в логах
)
```

Конфиг каждого модуля провайдит caller — граф выше самодостаточен и собирается
как есть (`fx.ValidateApp`); в приложении с App-структурой те же значения
приходят из неё.

Роль маппится по группам IdP, а не по логину: `Claims.Groups` — claim `groups`
ID-token'а, `Claims.Raw` — все проверенные claims для нестандартных полей.

Защищаемая операция ссылается на схему:

```go
huma.Operation{ ..., Security: []map[string][]string{{oidckit.SecuritySchemeName: {}}} }
```

Хендлер читает principal из ctx:

```go
id, ok := oidckit.IdentityFrom(ctx) // Identity{Subject, Username, Name, Email, Role}
```

### Составной authn (несколько схем)

`httpkit` принимает один `AuthExtractor`. Сервису, где cookie-сессия — лишь одна
из схем (например плюс bearer сервис-токены), `Module` не подходит: собери
проводку из конструкторов (`NewBFF`, `NewExtractor`, `NewSecurityScheme`,
`RegisterRoutes`, `RegisterIdentityLogAttr`, `(*BFF).Probe`) и предоставь свой
композитный `httpkit.AuthExtractor`, который делегирует `(*Extractor).Extract`
для cookie-ветки.

### Тесты потребителя

`testkit/oidctest` поднимает stub-провайдера (discovery, JWKS, `/authorize`,
`/token`, ключи RS256/ES256, подстановка ошибок на любом endpoint'е):

```go
idp := oidctest.New(t)                      // idp.URL() — issuer, idp.Client() — http-клиент
idp.SetClaims(map[string]any{"groups": []any{"admins"}})
idp.SetStatus(oidctest.EndpointToken, 500)  // ветка ошибки exchange
tok := idp.IDToken(map[string]any{"exp": nil}) // токен без exp для негативного теста
```

Stub поднимается по http, поэтому тестовому `Config` нужен
`AllowInsecureIssuer: true` (`OIDC_ALLOW_INSECURE_ISSUER=true`) — иначе
`Validate` отвергнет issuer, а discovery — endpoint'ы.

## Конфигурация

| Env | Поле | Default | Назначение и правило `Validate` |
|---|---|---|---|
| `OIDC_ISSUER` | `Config.Issuer` | — | issuer idp; обязателен, абсолютный `https`-URL без query (хвостовой `/` снимается) |
| `OIDC_CLIENT_ID` | `Config.ClientID` | — | OIDC client id; обязателен |
| `OIDC_CLIENT_SECRET` | `Config.ClientSecret` | — | client secret (token endpoint) |
| `OIDC_REDIRECT_URL` | `Config.RedirectURL` | — | callback-URL сервиса (redirect_uri); обязателен, абсолютный URL |
| `OIDC_POST_LOGIN_URL` | `Config.PostLoginURL` | `/` | redirect после входа, если нет `return_to`; только локальный путь |
| `OIDC_SESSION_TTL` | `Config.SessionTTL` | `12h` | срок жизни cookie-сессии; `> 0` |
| `OIDC_LEEWAY` | `Config.Leeway` | `1m` | допуск часов при проверке `exp`/`nbf`/`iat` ID-token'а; `>= 0` |
| `OIDC_ALLOW_INSECURE_ISSUER` | `Config.AllowInsecureIssuer` | `false` | разрешить issuer и endpoint'ы по http — только dev-стенд |
| `OIDC_DISCOVERY_ON_START` | `Config.DiscoveryOnStart` | `true` | требовать успешного discovery на старте (fail-fast) |
| — | `Config.RoutePrefix` | `/auth` | префикс BFF-роутов (программный, не env) |
| `COOKIE_INSECURE` | `CookieConfig.Insecure` | `false` | снять `Secure` (и префикс `__Host-`) — только локальная разработка по http |
| `COOKIE_SAMESITE` | `CookieConfig.SameSite` | `Lax` | `SameSite` session-cookie: `Lax`\|`Strict`\|`None`; `None` требует `Secure` |

Частичный конфиг не «выключает» OIDC молча: `NewConfig`/`NewCookieConfig`
валидируют значения и возвращают `ErrConfig`, приложение падает на старте.
Zero value `CookieConfig{}` безопасен: `Secure`, `SameSite=Lax`.

Секрет подписи сессии — `JWT_SECRET` (`authnkit.JWTConfig`, kit получает готовые
`JWTSigner`/`JWTParser`).

## API

- `NewVerifier(ctx, RPConfig, Doer) (*Verifier, error)` — discovery и первичная
  загрузка JWKS в конструкторе; nil `Doer` → `http.Client` с таймаутом 10s;
  `(*Verifier).AuthCodeURL / Exchange / Verify / KeysLoaded` →
  `Claims{Subject, Username, Name, Email, Groups, Raw}`.
- `Doer` — `Do(*http.Request) (*http.Response, error)`: `*httpclient.Client` или `*http.Client`.
- `PKCE() (verifier, challenge, error)`, `RandomToken() (string, error)`.
- `StateStore` (`Set`/`GetDel`, one-time), `NewCacheStateStore(cachekit.Cache)`.
- `NewBFF(Config, Doer, authnkit.JWTSigner, StateStore, CookieConfig, Hooks, logkit.Logger) (*BFF, error)`,
  `RegisterRoutes(api, *BFF)`, `(*BFF).Warmup(ctx)`, `(*BFF).Probe()` (`healthkit.Probe`).
- `Hooks{SessionClaims, OnLogin}` — доп-claims сессии / JIT-провижининг (ошибка
  `OnLogin` отменяет вход; служебный `typ` хук переопределить не может).
- `NewExtractor(authnkit.JWTParser, CookieConfig) *Extractor`; `Extract` совместим
  с `httpkit.AuthExtractor`; `Identity` + `WithIdentity`/`IdentityFrom`/`MustIdentityFrom`.
- `SecuritySchemeName` (`"Cookie"`), `NewSecurityScheme(CookieConfig)`, `SessionCookie`,
  `StateCookie`, `ExpiredSessionCookies`, `RegisterIdentityLogAttr`, `SessionTokenType`.
- Sentinel'ы: `ErrConfig`, `ErrRPConfig`, `ErrIssuerMismatch`, `ErrNoJWKS`,
  `ErrNoEndpoints`, `ErrInvalidToken`, `ErrUnknownKey`, `ErrNoSubject`,
  `ErrNoExpiry`, `ErrAudience`, `ErrNonceMismatch`, `ErrNoIDToken`, `ErrExchange`,
  `ErrNoSessionCookie`, `ErrInvalidSession`.

## Observability

Meter `git.stit.tech/stit-core/golibinfra/oidckit`; без `MeterProvider` — no-op.
Идентификаторов пользователя в label'ах нет — только конечные множества.

| Метрика | Labels | Назначение |
|---|---|---|
| `oidc_verifications_total` | `result` | исход проверки ID-token'а (`Verify`/`Exchange`) |
| `oidc_jwks_refresh_total` | `result` = `ok` \| `error` | фактические загрузки JWKS (первичная и по неизвестному `kid`) |
| `oidc_logins_total` | `stage`, `result` | исход шага логин-флоу |
| `oidc_discovery_duration_seconds` | `result` = `ok` \| `error` | длительность discovery + первичной загрузки JWKS |

Значения `result` у `oidc_verifications_total`: `ok`, `malformed`, `unknown_key`,
`jwks_error`, `signature_invalid`, `invalid_claims` (iss/aud/azp/exp/sub),
`nonce_mismatch`.

Значения `stage`/`result` у `oidc_logins_total`: `login` → `ok`,
`discovery_error`, `state_store_error`; `callback` → `ok`, `invalid_request`,
`csrf_mismatch`, `unknown_state`, `corrupt_state`, `state_store_error`,
`discovery_error`, `exchange_error`, `hook_error`, `sign_error`;
`logout` → `ok`, `cross_site`.

Спаны и HTTP-метрики исходящих запросов к idp даёт `httpclient` (`Doer` из
графа); разбор session-cookie в `Extractor` учитывает `authnkit`
(`jwt_verifications_total{result}`); HTTP-сигналы входящих запросов —
middleware `httpkit`. `Extractor` пробрасывает `user_id` в каждый log-record
(`RegisterIdentityLogAttr`, `Module` регистрирует сам). Готовность OIDC —
`healthkit.Probe` с именем `oidckit` (discovery выполнен, ключи загружены);
`Module` кладёт её в группу `healthkit.probes`.

## Инварианты

- ID-token принимается только с асимметричной подписью (RS*/ES*) — HS-подпись
  idp не принимается даже валидная, `alg=none` отвергается при разборе.
- `exp` обязателен; `exp`/`nbf`/`iat` проверяются с допуском `OIDC_LEEWAY`
  (default 1m): токен, просроченный меньше чем на leeway, ещё принимается.
- `aud` обязан содержать `OIDC_CLIENT_ID`; при нескольких `aud` обязателен
  `azp` = `OIDC_CLIENT_ID`, а заданный `azp` всегда сверяется с ним.
- `iss` токена сверяется с issuer'ом, который опубликовал сам idp в
  discovery-документе, и тот обязан совпасть с `OIDC_ISSUER`.
- **Сессия stateless.** Session-JWT проверяется локально по `JWT_SECRET`;
  серверного списка сессий нет. `POST /logout` только сбрасывает cookie — уже
  выданный токен остаётся валидным до `exp`. Отсюда короткий `OIDC_SESSION_TTL`
  и revocation-список на стороне сервиса, если выход обязан действовать мгновенно.
- Сессией считается только токен с `typ=oidc_session` и непустым `sub`: access-
  и refresh-токены других схем под тем же `JWT_SECRET` в гейт не проходят.
- `state` одноразовый (атомарный `GetDel`) и привязан к браузеру flow-cookie
  (`__Host-oidc_state`, `SameSite=Lax`, TTL 10m): callback без неё или с чужой
  → 400 ещё до похода в store. Два параллельных входа в одном браузере
  затирают flow-cookie друг друга — завершится последний.
- `return_to` — только локальный absolute-path без управляющих символов,
  обратных слешей и `//`-префикса, не длиннее 2048 (`state` — 128); всё
  остальное молча заменяется на `OIDC_POST_LOGIN_URL` (anti open-redirect).
- `SameSite=Lax` (default) отправляет session-cookie при переходе по внешней
  ссылке (deep-link из почты/мессенджера); `Strict` — не отправляет, и
  пользователь увидит login-редирект при живой сессии. `None` требует `Secure` и
  снимает браузерную защиту от CSRF (cookie уходит и с чужой страницы): бери его
  только под встраивание в сторонний контекст и прикрывай изменяющие операции
  CSRF-токеном — `Sec-Fetch-Site`-проверка кита стоит только на logout.
- Cookie — `HttpOnly`, `Path=/`, без `Domain`, `Max-Age` = `OIDC_SESSION_TTL`;
  при `Secure` действует префикс `__Host-` (её не подменит сосед по домену).
- `POST /logout` отвергает cross-site запрос (`Sec-Fetch-Site: cross-site` или
  чужой `Origin`) → 403; ответы callback, `/me` и logout идут с `Cache-Control: no-store`.
- Discovery ленивый, параллельные попытки сливаются в одну, а неудача 5s
  отвечает из отрицательного кэша — недоступный idp не собирает очередь горутин.
  При `OIDC_DISCOVERY_ON_START=true` (default) неуспех discovery роняет старт
  приложения.
- JWKS: неизвестный `kid` ведёт к загрузке не чаще раза в минуту (singleflight на
  параллельные промахи) — при плановой ротации новый ключ подхватывается в
  пределах минуты, а выведенный продолжает проверять подписи ещё 10 минут;
  при недоступном idp продолжают работать уже загруженные ключи. Ключи с
  `use != "sig"`, симметричные и неизвестного `kty` в набор не попадают.
- Таймаут походов в idp задаёт инжектированный `Doer` (`HTTPCLIENT_TIMEOUT`,
  default 10s); жёстко 10s — только у собственного клиента, который кит заводит
  при `Doer = nil`. Одна попытка discovery+JWKS ограничена 10s и дедлайном
  вызывающего: на старте это `fx.StartTimeout` (default 15s), в `/ready` —
  `HTTP_READY_PROBE_TIMEOUT`. Ошибки discovery и state-store уходят в лог, в
  тело 502 попадает только общий текст без URL и адресов.
- `GET <p>/login` — неаутентифицированный: каждый запрос кладёт запись в
  `StateStore` на 10m. Публичному сервису стоит навесить на него rate-limit
  (`httpkit.RateLimit`).
