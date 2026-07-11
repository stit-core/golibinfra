# oidckit

Authentik/OIDC BFF-контур для SSO-входа сотрудника: сервис сам ведёт
Authorization-Code-flow (+ PKCE S256), проверяет ID-token по JWKS и выдаёт
собственную cookie-сессию — браузер OIDC-токенов не видит.

Внутри два слоя одного пакета:

- **Relying-party** — `Verifier`: discovery `/.well-known/openid-configuration`,
  JWKS-кэш с lazy-refresh по неизвестному `kid`, verify iss/aud/exp/nonce
  (только асимметричные подписи: RS256/384/512, ES256/384), обмен
  authorization code; `PKCE()` / `RandomToken()`. Крипта —
  [`go-jose/v4`](https://github.com/go-jose/go-jose).
- **BFF-обвязка** — `BFF` + huma-роуты `GET <p>/login` (302 на idp),
  `GET <p>/callback` (exchange+verify → cookie-сессия), `GET <p>/me`,
  `POST <p>/logout`; `Extractor` (cookie → `authnkit.JWTParser` → `Identity`
  в ctx), совместимый с `httpkit.AuthExtractor`.

Сессия — HS256-JWT (`authnkit.HS256JWT`, секрет `JWT_SECRET`) в httpOnly-cookie
`access_token` (`SameSite=Strict`, `Secure` по `COOKIE_SECURE`), TTL —
`OIDC_SESSION_TTL` (default 12h). Короткоживущий state логин-флоу
(PKCE verifier / nonce / return-to; TTL 10m, one-time) живёт в `StateStore`:
`NewCacheStateStore` (Redis через `cachekit`) или `NewMemoryStateStore`
(тесты / single-replica без Redis).

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
        authnkit.NewJWTConfig,               // JWT_SECRET
        oidckit.NewConfig,                   // OIDC_* + OIDC_SESSION_TTL
        oidckit.NewCookieConfig,             // COOKIE_SECURE
        fx.Annotate(oidckit.NewCacheStateStore, fx.As(new(oidckit.StateStore))), // поверх cachekit.Cache
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
| `COOKIE_SECURE` | `CookieConfig.Secure` | `false` | флаг Secure сессионной cookie |

Секрет подписи сессии — `JWT_SECRET` (`authnkit.JWTConfig`, kit получает готовые
`JWTSigner`/`JWTParser`).

## API

- `NewVerifier(ctx, RPConfig, *http.Client) (*Verifier, error)`;
  `(*Verifier).AuthCodeURL / Exchange / Verify` → `Claims{Subject, Username, Name, Email}`.
- `PKCE() (verifier, challenge, error)`, `RandomToken() (string, error)`.
- `StateStore` (`Set`/`GetDel`, one-time), `NewCacheStateStore(cachekit.Cache)`,
  `NewMemoryStateStore()`.
- `NewBFF(Config, authnkit.JWTSigner, StateStore, CookieConfig, Hooks, logkit.Logger) *BFF`,
  `RegisterRoutes(api, *BFF)`.
- `Hooks{SessionClaims, OnLogin}` — доп-claims сессии / JIT-провижининг (ошибка
  `OnLogin` отменяет вход).
- `NewExtractor(authnkit.JWTParser) *Extractor`; `Extract` совместим с
  `httpkit.AuthExtractor`; `Identity` + `WithIdentity`/`IdentityFrom`/`MustIdentityFrom`.
- `SecuritySchemeName` (`"Cookie"`), `NewSecurityScheme()`, `SessionCookie`,
  `ExpiredSessionCookies`, `RegisterIdentityLogAttr`.

## Observability

Собственных метрик kit не эмитит: HTTP-сигналы дают middleware `httpkit`
(RED + access-log), verify/parse сессии учитывается счётчиками `authnkit`
(`authn_jwt_verify_total`), запросы к idp — спанами обвязки. `Extractor`
пробрасывает `user_id` в каждый log-record (`RegisterIdentityLogAttr`,
`Module` регистрирует сам).

## Инварианты

- ID-token принимается только с асимметричной подписью (RS*/ES*) — HS-подпись
  idp не принимается даже валидная.
- `state` одноразовый: повторный callback с тем же `state` → 400.
- `return_to` — только внутренний absolute-path; внешние и protocol-relative
  URL молча заменяются на `OIDC_POST_LOGIN_URL` (anti open-redirect).
- Discovery ленивый (первый login/callback), недоступность idp не мешает старту
  приложения и не задевает живые cookie-сессии (verify по `JWT_SECRET` локально).
