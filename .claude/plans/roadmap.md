# Roadmap

Единственный артефакт планирования — этот файл. Подпапок, `<slug>.md`, архивов и
changelog'ов нет: они расходятся с правдой и мешают.

**Planning-first.** Изменение идёт через план — `### <slug>`-секцию под `## Active` ниже.
Прямая правка без плана допустима только для truly-atomic (опечатка, один очевидный файл) и
багфиксов (RED-first). Отклонение от плана — осознанное, проговаривается с юзером.

**Жизненный цикл.** `planning → in-progress → blocked? → done`. По `done` секция удаляется
целиком — история остаётся в `git log`. Незакрытый follow-up переезжает в `## Backlog` или
новой `### `-секцией, а не хвостом в удаляемой.

**Где это запускается.** Репо — библиотека, живого стека нет: гейт целиком локальный —
`golangci-lint run ./...` + `go test -short -race ./...` + `go build ./...` (как в
`.gitea/workflows/ci.yml`). Выпуск версии — тег `vX.Y.Z` (skill `/release`), консьюмеры
подтягивают `go get` сами.

## Active

### hubkit-customer-contour — второй контур identity (заказчик) в `hubkit`

**Создан:** 2026-07-27 · **Статус:** in-progress (код и доки готовы, гейт зелёный; открыт
только выпуск версии — A9, за оператором)

**Контекст.** Программа «customer-portal» (`stit-hub/infra/plan-customer/`, файл
`10-golibinfra.md`, волна 1) вводит второй контур сессии: заказчик логинится в
company-backend (`iss=https://api.hub.stit.tech`), тот выпускает свою ES256-сессию в
cookie `stit_customer` (claims `kind=customer`, `company_id`, `account_role`, `products`).
Сотруднический контур (`stit_session`, `iss=https://id.stit.tech`) не трогается. `hubkit` —
общая точка: он один знает про оба слота, продукты не сравнивают строки руками.

**Не входит в скоуп.** Выпуск customer-сессии (company-backend, волна 2), правило видимости
`customer_visible` (продукты, волны 4–6), правки в репо-потребителях, bump версии и тег.

**Атомы.**
- [x] **A1:** RED — тесты `hubkit/sso` на оба контура: employee-токен в customer-слоте →
  отказ, обе cookie → employee, протухший employee + валидный customer → customer,
  выключенный customer-контур → поведение как сегодня, `CanUseProduct` в обоих контурах.
  - Verify: `go test ./hubkit/...` красный по новым тестам · Зависит от: —
- [x] **A2:** `Identity` — `Kind`/`CompanyID`/`AccountRole`/`Products` + `IsEmployee`/
  `IsCustomer`/`CanUseProduct`; пустой claim `kind` = employee (уже выпущенные токены живут).
  - Verify: `go test ./hubkit/sso/...` зелёный по identity-тестам · Зависит от: A1
- [x] **A3:** `sso.Config` — customer-поля (`HUB_SSO_CUSTOMER_JWKS_URL|_ISSUER|_COOKIE|
  _LOGIN_URL`), `CustomerEnabled()`, `CustomerLoginURLFor`; пустой JWKS-URL = контур выключен.
  - Verify: тесты конфига зелёные · Зависит от: A1
- [x] **A4:** `Verifier` — контур-дискриминатор `kind` (employee-верификатор отвергает
  `kind=customer` и наоборот), `CustomerVerifier` с обязательным `iss`; claim-константы
  `ClaimKind`/`ClaimCompanyID`/`ClaimAccountRole`/`ClaimProducts`.
  - Verify: verifier-тесты обоих контуров зелёные · Зависит от: A2, A3
- [x] **A5:** `Extractor` — два слота с приоритетом employee (`NewDualExtractor`), `Module`
  разворачивает оба верификатора; `NewExtractor` остаётся однослотовым (совместимость).
  - Verify: extractor-тесты (приоритет, протухший employee, выключенный контур) зелёные ·
    Зависит от: A4
- [x] **A6:** Гейт-хелперы `sso.RequireEmployee` / `RequireCustomer` (+ `ErrNoIdentity`,
  `ErrWrongKind`).
  - Verify: тесты гейтов зелёные · Зависит от: A2
- [x] **A7:** `issuer` — имена access/refresh-cookie и `Path` refresh'а из конфига
  (`RefreshName`/`RefreshPath` + `RefreshCookie`/`ExpiredRefreshCookie`), чтобы второй issuer
  в соседнем процессе поднимался со своими именами.
  - Verify: issuer-тест на два экземпляра с разными именами зелёный · Зависит от: —
- [x] **A8:** Доки: `hubkit/README.md` — таблица двух контуров, правило приоритета,
  предупреждение «`Kind` проверяется до доменной политики»; env-таблица + корневой
  `README.md`.
  - Verify: `golangci-lint run ./...` + `go test -short -race ./...` + `go build ./...`
    зелёные · Зависит от: A5, A6, A7
- [ ] **A9:** Выпуск версии — коммит + тег minor (`v0.53.0`: последний тег `v0.52.0`) +
  push. За оператором (skill `/release`), рабочее дерево готово.
  - Verify: `go get git.stit.tech/stit-core/golibinfra@v0.53.0` в первом потребителе
    (company-backend, волна 2) · Зависит от: A8

**Открытые вопросы (эскалация оператору, не решаются здесь).**
- `Products []string` делает `sso.Identity` несравнимой (`==`/`!=`, ключ map). В репо это
  чинится (тесты переведены на `reflect.DeepEqual`); в потребителях сравнений `Identity`
  не найдено, но `tag-backend`/`company-backend` держат `type Identity = sso.Identity` —
  при bump'е версии проверить.
- `HUB_SSO_CUSTOMER_AUDIENCE` планом не предусмотрен: aud customer-контура не проверяется
  (у employee — опционально через `HUB_SSO_AUDIENCE`).
- План называет версию `v0.4x.0`, фактический последний тег — `v0.52.0`; minor-bump =
  `v0.53.0`.
- `issuer.Session` (запись refresh-стора) не расширена полями заказчика: по
  `00-architecture § 3` refresh заказчика и так ходит в БД за аккаунтом (там же ловится
  `status=blocked` и снятый продукт). Понадобится иначе — правка контракта в плане, не здесь.
