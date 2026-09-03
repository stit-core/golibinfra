# oidckit

Authentik/OIDC BFF-контур для SSO-входа сотрудника: сервис сам ведёт
Authorization-Code-flow (+ PKCE S256), проверяет ID-token по JWKS и выдаёт
собственную cookie-сессию — браузер OIDC-токенов не видит.

Внутри два слоя одного пакета:

- **Relying-party** — `Verifier`: discovery `/.well-known/openid-configuration`,
  JWKS-кэш (refresh по неизвестному `kid` не чаще раза в минуту, параллельные
  промахи сливаются в один запрос), verify iss/aud/exp/nonce (только
  асимметричные подписи: RS256/384/512, ES256/384), обмен authorization code;
  `PKCE()` / `RandomToken()`. Крипта — [`go-jose/v4`](https://github.com/go-jose/go-jose).
- **BFF-обвязка** — `BFF` + huma-роуты `GET <p>/login` (302 на idp),
  `GET <p>/callback` (exchange+verify → cookie-сессия), `GET <p>/me`,
  `POST <p>/logout`; `Extractor` (cookie → `authnkit.JWTParser` → `Identity`
  в ctx), совместимый с `httpkit.AuthExtractor`.

Сессия — HS256-JWT (`authnkit.HS256JWT`, секрет `JWT_SECRET`, не короче 32 байт)
в httpOnly-cookie `access_token` (`SameSite=Strict`, `Secure` по `COOKIE_SECURE`),
TTL — `OIDC_SESSION_TTL` (default 12h). Короткоживущий state логин-флоу
(PKCE verifier / nonce / return-to; TTL 10m, one-time через атомарный `GetDel`)
живёт в `StateStore` поверх `cachekit.Cache`: Redis в проде,
`cachekit.NewMemoryCache()` в тестах и single-replica без Redis.

Kit не трактует роли и не знает продуктовых пользователей: маппинг claims →
роль/доп-claims сессии и JIT-провижининг — хуки консьюмера (`Hooks`).

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/oidckit"
```

Версия модуля — см. [`../README.md`](../README.md) (§ Версионирование).

## Быстрый старт

fx-проводка. `Module` — drop-in BFF; caller провайдит `Config`/`CookieConfig`
(env), `StateStore` и authnkit-подпись:

```go
fx.New(
    fx.Provide(
        authnkit.NewJWTConfig,               // JWT_SECRET (+ JWT_LEEWAY)
        oidckit.NewConfig,                   // OIDC_* + OIDC_SESSION_TTL
        oidckit.NewCookieConfig,             // COOKIE_SECURE
        oidckit.NewCacheStateStore,          // StateStore поверх cachekit.Cache
        func() oidckit.Hooks {               // опционально
            return oidckit.Hooks{
                SessionClaims: func(c oidckit.Claims) map[string]any {
                    return map[string]any{"role": roleFor(c.Username)}
                },
                OnLogin: func(ctx context.Context, c oidckit.Claims) error {
                    return employees.SyncOnLogin(ctx, c) // JIT-провижининг
                },
            }
        },
    ),
    authnkit.Module,
    cachekit.Module,
    httpkit.Module,
    oidckit.Module, // роуты /auth/*, security-scheme Cookie, AuthExtractor, user_id в логах
)
```

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
`RegisterRoutes`, `RegisterIdentityLogAttr`) и предоставь свой композитный
`httpkit.AuthExtractor`, который делегирует `(*Extractor).Extract` для
cookie-ветки.

## Конфигурация

| Env | Поле | Default | Назначение |
|---|---|---|---|
| `OIDC_ISSUER` | `Config.Issuer` | — | issuer idp; пусто → login/callback отвечают 503 |
| `OIDC_CLIENT_ID` | `Config.ClientID` | — | OIDC client id |
| `OIDC_CLIENT_SECRET` | `Config.ClientSecret` | — | client secret (token endpoint) |
| `OIDC_REDIRECT_URL` | `Config.RedirectURL` | — | callback-URL сервиса (redirect_uri) |
| `OIDC_POST_LOGIN_URL` | `Config.PostLoginURL` | `/` | redirect после входа (если нет `return_to`) |
| `OIDC_SESSION_TTL` | `Config.SessionTTL` | `12h` | срок жизни cookie-сессии |
| — | `Config.RoutePrefix` | `/auth` | префикс BFF-роутов (программный, не env) |
| `COOKIE_SECURE` | `CookieConfig.Secure` | `true` | флаг Secure сессионной cookie; `false` — только локальная разработка по http |

Секрет подписи сессии — `JWT_SECRET` (`authnkit.JWTConfig`, kit получает готовые
`JWTSigner`/`JWTParser`). Литерал `CookieConfig{}` даёт `Secure=false` —
в коде заполняй поле явно, дефолт `true` действует только через envconfig.

## API

- `NewVerifier(ctx, RPConfig, *http.Client) (*Verifier, error)` — discovery и
  первичная загрузка JWKS в конструкторе; nil-клиент → `http.Client` с таймаутом 10s;
  `(*Verifier).AuthCodeURL / Exchange / Verify` → `Claims{Subject, Username, Name, Email}`.
- `PKCE() (verifier, challenge, error)`, `RandomToken() (string, error)`.
- `StateStore` (`Set`/`GetDel`, one-time), `NewCacheStateStore(cachekit.Cache)`.
- `NewBFF(Config, authnkit.JWTSigner, StateStore, CookieConfig, Hooks, logkit.Logger) *BFF`,
  `RegisterRoutes(api, *BFF)`.
- `Hooks{SessionClaims, OnLogin}` — доп-claims сессии / JIT-провижининг (ошибка
  `OnLogin` отменяет вход).
- `NewExtractor(authnkit.JWTParser) *Extractor`; `Extract` совместим с
  `httpkit.AuthExtractor`; `Identity` + `WithIdentity`/`IdentityFrom`/`MustIdentityFrom`.
- `SecuritySchemeName` (`"Cookie"`), `NewSecurityScheme()`, `SessionCookie`,
  `ExpiredSessionCookies`, `RegisterIdentityLogAttr`.

## Observability

Meter `git.stit.tech/stit-core/golibinfra/oidckit`; без `MeterProvider` — no-op.

| Метрика | Labels | Назначение |
|---|---|---|
| `oidc_verifications_total` | `result` | исход проверки ID-token'а (`Verify`/`Exchange`) |
| `oidc_jwks_refresh_total` | `result` = `ok` \| `error` | фактические загрузки JWKS (первичная и по неизвестному `kid`) |

Значения `result` у `oidc_verifications_total`: `ok`, `malformed`, `unknown_key`,
`jwks_error`, `signature_invalid`, `invalid_claims` (iss/aud/exp/nbf/sub),
`nonce_mismatch`.

Разбор session-cookie в `Extractor` учитывает `authnkit`
(`jwt_verifications_total{result}`); HTTP-сигналы дают middleware `httpkit`.
`Extractor` пробрасывает `user_id` в каждый log-record (`RegisterIdentityLogAttr`,
`Module` регистрирует сам).

## Инварианты

- ID-token принимается только с асимметричной подписью (RS*/ES*) — HS-подпись
  idp не принимается даже валидная.
- **Сессия stateless.** Session-JWT проверяется локально по `JWT_SECRET`;
  серверного списка сессий нет. `POST /logout` только сбрасывает cookie — уже
  выданный токен остаётся валидным до `exp`. Отсюда короткий `OIDC_SESSION_TTL`
  и revocation-список на стороне сервиса, если выход обязан действовать мгновенно.
- `state` одноразовый (атомарный `GetDel`): повторный callback с тем же `state` → 400.
- `return_to` — только внутренний absolute-path; внешние и protocol-relative
  URL молча заменяются на `OIDC_POST_LOGIN_URL` (anti open-redirect).
- Discovery ленивый (первый login/callback), недоступность idp не мешает старту
  приложения и не задевает живые cookie-сессии. Ошибки discovery и state-store
  уходят в лог; в тело 502 попадает только общий текст без URL и адресов.
- JWKS: неизвестный `kid` ведёт к загрузке не чаще раза в минуту (singleflight на
  параллельные промахи); при недоступном idp продолжают работать уже загруженные
  ключи. Все походы в idp — с таймаутом 10s.
