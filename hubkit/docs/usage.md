# hubkit — рецепты

Все примеры предполагают, что конфиги провайдит caller: `Module` кита ENV не
читает.

## Продукт: проверка hub-сессии

```go
package app

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

var Module = fx.Options(
	fx.Provide(logkit.NewConfig, httpclient.NewConfig, httpkit.NewConfig),
	logkit.Module,
	httpclient.Module, // опционален: без него verifier поднимет свой http.Client
	httpkit.Module,

	fx.Provide(sso.NewConfig), // HUB_SSO_* из ENV, с Validate
	sso.Module,                // Verifier + CustomerVerifier + Extractor + схемы + AuthExtractor
)
```

`sso.Module` кладёт в граф `httpkit.AuthExtractor`, поэтому huma-операции с
непустым `Security` начинают требовать сессию:

```go
huma.Register(api, huma.Operation{
	OperationID: "listDeals",
	Method:      http.MethodGet,
	Path:        "/api/deals",
	Security:    []map[string][]string{{cfg.SchemeName(): {}}},
}, handler)
```

Имена схем берутся из конфига (`cfg.SchemeName()` / `cfg.CustomerSchemeName()`),
дефолты — `EmployeeSession` / `CustomerSession`. Схема заказчика попадает в
OpenAPI только при включённом контуре.

### Составной extractor

`httpkit` принимает один `AuthExtractor`. Сервису, у которого кроме hub-сессии
есть свой способ аутентификации, нужен `sso.Verifiers` — то же самое без
привязки к `httpkit.AuthExtractor`:

```go
var Module = fx.Options(
	fx.Provide(sso.NewConfig),
	sso.Verifiers, // *Verifier, *CustomerVerifier, *Extractor, security-схемы

	fx.Provide(func(hub *sso.Extractor, tokens *TokenAuth) httpkit.AuthExtractor {
		return func(r *http.Request) (context.Context, error) {
			if r.Header.Get("X-Service-Token") != "" {
				return tokens.Extract(r)
			}
			return hub.Extract(r)
		}
	}),
	fx.Invoke(sso.RegisterIdentityLogAttr), // user_id в логах
)
```

### Гейты контура в доменном коде

```go
id, err := sso.RequireEmployee(ctx) // ErrNoIdentity → 401, ErrWrongKind → 403
if err != nil {
	return nil, err
}
if !id.CanUseProduct("deal") { // сотруднику — всегда true
	return nil, huma.Error403Forbidden("product not available")
}
```

Login-redirect: `cfg.LoginURLFor(returnURL)` и `cfg.CustomerLoginURLFor(returnURL)`
дают `<login>?rd=<url-encoded>`.

## Issuer: выпуск сессии

```go
var Module = fx.Options(
	fx.Provide(issuer.NewKeyConfig, issuer.NewCookieConfig, issuer.NewRefreshConfig),
	fx.Provide(cachekit.NewConfig),
	cachekit.Module, // cachekit.Cache для refresh-состояния
	issuer.Module,   // Signer + RefreshManager (+ JWKS-роут, если в графе есть huma.API)
)
```

`issuer.Module` вешает `GET /.well-known/jwks.json` сам, когда в графе есть
`huma.API`. Без huma на этом роуте — смонтируй `issuer.JWKSHandler(signer)` в
свой `http.ServeMux`; ответ несёт `Cache-Control: max-age` из
`HUB_SESSION_JWKS_MAX_AGE`.

Логин: собрать `Session`, подписать access-токен, выдать refresh. Контур
задаётся константой из `hubkit/hubclaims` — issuer'у не нужен `hubkit/sso`
(и через него весь клиентский контур верификации).

```go
sess := issuer.Session{
	SID:         idkit.New().String(),
	Subject:     account.Subject,
	Kind:        hubclaims.KindCustomer, // пусто = сотрудник
	CompanyID:   account.CompanyID,      // у заказчика обязателен
	AccountRole: account.Role,
	Products:    account.Products,
}

access, err := signer.Sign(sess.Claims(), cookieCfg.TTL) // iss/aud/exp/iat/nbf ставит Sign
if err != nil {
	return err
}
refresh, err := refreshMgr.Issue(ctx, sess)
if err != nil {
	return err
}
http.SetCookie(w, ptrkit.Of(issuer.SessionCookie(access, cookieCfg)))
http.SetCookie(w, ptrkit.Of(issuer.RefreshCookie(refresh, cookieCfg, refreshCfg.TTL)))
```

`Sign` отвергает `iss`/`aud`/`exp`/`nbf`/`iat`/`typ` в карте caller'а
(`ErrReservedClaim`), требует непустой строковый `sub` (`ErrNoSubject`) и не
выпускает `kind=customer` без `company_id` (`ErrNoCompany`); `Issue` проверяет
то же самое у `Session`.

Обмен refresh-токена:

```go
sess, next, err := refreshMgr.Rotate(ctx, cookieValue)
switch {
case err == nil:
	access, _ := signer.Sign(sess.Claims(), cookieCfg.TTL)
	http.SetCookie(w, ptrkit.Of(issuer.SessionCookie(access, cookieCfg)))
	http.SetCookie(w, ptrkit.Of(issuer.RefreshCookie(next, cookieCfg, refreshCfg.TTL)))
case errors.Is(err, issuer.ErrRefreshInFlight):
	// Параллельный обмен той же вкладки/клиента: cookie победителя уже выдана,
	// повтор запроса пройдёт с новым токеном. Сессию не гасим.
	return huma.Error409Conflict("refresh in flight")
case errors.Is(err, issuer.ErrRefreshReused), errors.Is(err, issuer.ErrRefreshRevoked):
	// Утечка или логаут: гасим cookie и отправляем на вход.
	http.SetCookie(w, ptrkit.Of(issuer.ExpiredSessionCookie(cookieCfg)))
	http.SetCookie(w, ptrkit.Of(issuer.ExpiredRefreshCookie(cookieCfg)))
	return huma.Error401Unauthorized("session revoked")
default:
	return huma.Error401Unauthorized("refresh failed")
}
```

Клиенту, который отправил обмен и не получил ответ (обрыв на пути назад),
ретрай помогает только внутри `HUB_REFRESH_REUSE_GRACE`: там он получит 409 и
дождётся cookie победителя. Позже тот же токен считается утёкшим, семейство
отзывается, и клиент идёт на логин — размен зафиксирован в
[architecture.md § Rotating refresh](architecture.md).

Логаут — `refreshMgr.Revoke(ctx, sid)` + гашение обеих cookie.

## Клиент hub API

```go
var Module = fx.Options(
	fx.Provide(client.NewConfig),
	client.Module, // *client.Client поверх httpclient.Client (опционален в графе)
)
```

```go
company, err := hub.GetCompany(ctx, id)
switch {
case err == nil:
case errors.Is(err, client.ErrNotFound):
	return nil, huma.Error404NotFound("company not found")
case errors.Is(err, client.ErrUnavailable):
	return projection.GetCompany(ctx, id) // деградация на локальную проекцию
default:
	return nil, err
}
```

Реквизиты компании асимметричны: `Company` (read) отдаёт их плоскими полями,
`CompanyInput` (write) принимает только вложенными (`Requisites`). Схема
create/update у хаба строгая (`additionalProperties: false`) — лишнее поле
верхнего уровня даёт 4xx.

Write-through-методы принадлежности (`ReplaceEntityTags`,
`ReplaceEmployeeDepartments`) при `nil` наборе шлют `[]`, а не `null` — это
снятие всех связей.

## Sync проекций

Продукт реализует два порта и собирает по движку на ресурс:

```go
type store struct{ db *pgkit.DB }

func (s *store) Upsert(ctx context.Context, resource string, items []json.RawMessage) error {
	for _, raw := range items {
		var c client.Company
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		if err := s.upsertCompany(ctx, c); err != nil { // INSERT … ON CONFLICT DO UPDATE
			return err
		}
	}
	return nil
}

func (s *store) Load(ctx context.Context, resource string) (int64, error) {
	cursor, err := s.q.GetSyncCursor(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil // нового ресурса ещё нет в таблице — начинаем с начала фида
	}
	return cursor, err
}
```

```go
var Module = fx.Options(
	fx.Provide(client.NewConfig, sync.NewConfig),
	client.Module,
	sync.Module,    // HTTPFetcher + разворот движков в timerkit.Group
	timerkit.Module, // крутит job'ы с graceful shutdown

	fx.Provide(fx.Annotate(newCompaniesEngine, fx.ResultTags(`group:"hubkit.sync.engine"`))),
	fx.Provide(fx.Annotate(newProjectsEngine, fx.ResultTags(`group:"hubkit.sync.engine"`))),
)

func newCompaniesEngine(f sync.Fetcher, st *store, cfg sync.Config, log logkit.Logger) (*sync.Engine, error) {
	return sync.NewEngine(client.ResourceCompanies, f, st, st, cfg, log)
}
```

Ресурсы фида — константы `client.Resource*`: `companies`, `projects`,
`visibility`, `entity-tags` (company base), `employees`, `departments`
(employee base), `tags` (tag base).

## Схема проекций

```go
pgkit.ProvideMigrations(projection.Migrations) // применить эталон как есть
```

либо скопировать `templates/*.sql` в каталог миграций продукта и `queries/*.sql`
в свой sqlc-пакет. Расхождения шаблона с моделями `client` перечислены в
[architecture.md § Projection](architecture.md).
