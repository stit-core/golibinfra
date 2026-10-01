# hubkit — тестирование

## Тесты продукта: `testkit/hubtest`

Сабпакет даёт тестовый issuer hub-сессии (ES256-ключи + JWKS на `httptest`),
фабрики токенов обоих контуров, готовые verifier'ы/extractor и in-memory порты
движка синка. Собственный `ecdsa.GenerateKey` + JWKS-сервер в тесте продукта не
нужен.

```go
import "git.stit.tech/stit-core/golibinfra/testkit/hubtest"

func TestHandler_RequiresEmployee(t *testing.T) {
	iss := hubtest.NewIssuer(t)          // ключ + JWKS-сервер, снимается в t.Cleanup
	v := iss.Verifier(t)                 // sso.Verifier, нацеленный на этот JWKS

	id, err := v.Verify(t.Context(), iss.EmployeeToken(t))
	// id.EmployeeID == hubtest.DefaultEmployeeID
}
```

Оба контура и extractor:

```go
emp := hubtest.NewIssuer(t)
cust := hubtest.NewCustomerIssuer(t)

cfg := hubtest.SSOConfig(emp, cust)      // валидный sso.Config под эти issuer'ы
e := hubtest.Extractor(t, emp, cust)     // *sso.Extractor обоих контуров

req := hubtest.Request(sso.DefaultCookie, emp.EmployeeToken(t))
ctx, err := e.Extract(req)
```

Негативные кейсы собираются опциями токена:

| Опция | Кейс |
|---|---|
| `WithExpiry(time.Now().Add(-time.Hour))` | истёкший токен |
| `WithoutClaim("exp")`, `WithoutClaim("sub")` | токен без обязательного claim'а |
| `WithoutClaim(sso.ClaimCompanyID)` | customer без компании (`ErrNoCompany`) |
| `WithClaim("aud", []string{"other"})` | чужая audience |
| `WithKID("unknown")` | подпись своим ключом, чужой `kid` (`ErrUnknownKey`) |
| `SignHS256(t, claims)` | key confusion: публичный ключ как HMAC-секрет |
| `Rotate(t)` / `RotateKeepingPrevious(t)` | ротация ключа issuer'а без previous-слота и с ним |

Хендлеры, которым нужен только принципал, обходятся без HTTP-слоя:

```go
ctx := hubtest.WithEmployee(t.Context())
ctx = hubtest.WithCustomer(t.Context(), func(i *sso.Identity) { i.Products = nil })
```

Движок синка тестируется на in-memory портах:

```go
feed, proj, cur := hubtest.NewFeed(), hubtest.NewProjection(), hubtest.NewCursors()
feed.Add(t, client.ResourceCompanies, 1, client.Company{ID: "c1", UpdatedSeq: 1})
cur.Set(client.ResourceCompanies, 0)

e, _ := sync.NewEngine(client.ResourceCompanies, feed, proj, cur, cfg, logkit.Nop())
_ = e.Run(t.Context())

// proj.Items(client.ResourceCompanies), cur.Cursor(...), feed.Calls()
// feed.FailWith(resource, err) / proj.FailWith(err) / cur.FailLoad(err) — error-ветки
```

## Детерминизм

Часы инъектируются во всех трёх местах, где кит смотрит на время:

```go
clock := clockkit.NewTickable(time.Now())
v, _ := sso.NewVerifier(cfg, nil, sso.WithClock(clock))          // сроки токена + троттлинг JWKS
s, _ := issuer.NewSigner(keyCfg, issuer.WithClock(clock))        // iat/nbf/exp
m, _ := issuer.NewRefreshManager(cache, refCfg, issuer.WithClock(clock)) // grace-окно reuse
e, _ := sync.NewEngine(res, f, st, cur, syncCfg, log, sync.WithClock(clock)) // отметка успеха
```

`clock.Advance(d)` двигает время без `time.Sleep`; `cachekit.NewMemoryCache(cachekit.WithClock(clock))`
синхронно истекает записи refresh-контура.

JWKS-сервер `hubtest` отдаёт `Cache-Control: max-age=300`, как настоящий issuer,
и `hubkit/sso` берёт из него окно свежести набора. Поэтому после
`iss.Rotate(t)`/`iss.RotateKeepingPrevious(t)` тест, который ждёт нового `kid`,
двигает часы больше чем на 5 минут — иначе verifier обоснованно продолжает
работать с прежним набором. Движок синка, собранный в тесте вручную, стоит
закрывать (`t.Cleanup(func() { _ = e.Close() })`): иначе его серии видны
следующему прогону (`-count>1`).

## Тесты самого кита

```bash
go test -race -count=1 ./hubkit/...          # unit; тесты projection требуют Docker
go test -short -race -count=1 ./hubkit/...   # без контейнеров
```

`hubkit/projection` поднимает Postgres через `testkit.StartPostgres`: применяет
`projection.Migrations`, проверяет наличие таблиц, `PREPARE` каждого эталонного
запроса против схемы (валидность sqlc-шаблона), маппинг «нет строки курсора → 0»
и поведение FK `hub_projects.company_id`. Под `-short` эти тесты пропускаются,
без Docker — падают.

Регрессии, которые тесты держат явно:

- ES256-allowlist: HS256 с публичным ключом как секретом и `alg=none` отвергаются.
- Токен без `exp` и без `sub` отвергается; leeway берётся из конфига.
- Ротация ключа: токен предыдущего `kid` проверяется после того, как issuer
  вывел его из JWKS (`HUB_SSO_KEY_GRACE`), и после переноса в previous-слот.
- 3000 параллельных пар `Rotate` одним токеном: 0 отзывов семейства, проигравший
  получает `ErrRefreshInFlight`, токен победителя остаётся рабочим.
- Недоступный JWKS даёт `httpkit.ErrAuthUnavailable` (503), а не 401.
- Отмена ctx в тике синка не инкрементирует `hub_sync_errors_total`.
- `Engine.Close()` и `OnStop` модуля снимают observable-callback: серии
  закрытого движка исчезают (набор переживает `-count>1`).
- Часы issuer'а инъектируются: токен проверяется против реального JWKS-сервера
  и отвергается часами за `exp`.
- `kind=customer` без `company_id` не выпускается ни `Issue`, ни `Sign`.
- `HUB_REFRESH_REUSE_GRACE=0` роняет старт (`ErrConfig`).
- Набор JWKS перечитывается по истечении окна свежести даже при известном
  `kid` (отзыв ключа), а неудачное перечитывание не отзывает известный ключ.
