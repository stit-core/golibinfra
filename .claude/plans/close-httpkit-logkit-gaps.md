---
status: in-progress
parent: go-backend-template:reaudit-to-nine
blocked-by: —
---

# close-httpkit-logkit-gaps — 429 лимитера в контракте httpkit, логгер команды из LOG_* в logkit

## Цель
Закрыть в китах два пункта бэклога шаблона `go-backend-template` (программа `reaudit-to-nine`, шаг S9, трек
g-kits): 429 общего лимитера httpkit описан в контракте каждой операции, а короткоживущая команда вне fx получает
логгер одной строкой `logkit` с тем же разбором `LOG_*`, что у графа с `logkit.Module`. Потребитель получает оба
тегом `v1.6.0`.

## Контекст
- Программа — `go-backend-template/.claude/plans/reaudit-to-nine.md` (S9, раздел «golibinfra — S9»): ветка
  `nine/g-kits` от `main` = `v1.5.0` (`90adb36`); коммиты — на ветке, слияние, push и тег — дирижёр. Параллельно
  идут g-auth (`authnkit/`, `testkit/{authntest,sessiontest}`) и g-arch (`archkit/`). N =
  `/home/mm/.cache/stit-audit-program/nine`; журнал трека — `N/g-kits/progress.md`.
- `golibinfra-httpkit-429`. `RateLimit` стоит в цепочке сервера на всех маршрутах (`httpkit/server.go`, `chain`) и
  при `HTTP_RATE_LIMIT_RPS > 0` (по умолчанию 100) отвечает `429 RATE_LIMITED` с `Retry-After: 1`
  (`httpkit/ratelimit.go`, `writeAPIError`). Хук `describeErrors` (`httpkit/errormap.go`) описывает только коды
  `Declare` и 401 операции с `Security`: на `90adb36` 429 в OpenAPI есть лишь у операции, которая объявила его своей
  таблицей, и ни у одного 429 нет `Retry-After`.
- Шаблон описывает 429 строгого лимита `/api/auth/*` сам: `authlimit.Rule` (`RATE_LIMITED`, sentinel без
  хендлера) в таблицах восьми операций (`internal/platform/authlimit`, `internal/domain/{auth,user,verification}/
  delivery/http`); у `logoutUser` и `requestPasswordReset` таблица — из него одного. `TestHTTPOperations`
  (`cmd/api/api_test.go`) держит «429 `RATE_LIMITED` — ровно под `/api/auth/`» и «у операций `/api/` нет
  `default`»; `openapi.json` пишет `TestOpenAPISpec -update` (`make gen`) при `HTTP_*` по умолчанию.
- `golibinfra-logkit-env`. `newLog` в `cmd/migrate/main.go` и `cmd/seed-admin/main.go` шаблона —
  `logkit.NewConfig()` и `logkit.New(cfg)`: `LOG_REDACT_*` он не читает (граф `cmd/api` берёт и
  `NewRedactConfig`), а при `LOG_SILENT=true` у команды без OTel-sink'а пропадает и причина отказа (у `FxLogger` на
  этот случай — `WithStderrFallback`). `TestNewLog` обеих команд: окружение по умолчанию — логгер без ошибки,
  `LOG_LEVEL=loud` — `logkit.ErrConfig`; `TestMainOnlyEnters` шаблона держит `main` без логики — логгер даёт один
  вызов.
- Источники пунктов: `N/validation-s7-code.md`, `N/conductor.md` (поиск «429», «newLog»).

## Не входит
- Правки шаблона (`go.mod`, `authlimit.Rule`, `TestHTTPOperations`, `newLog`, `openapi.json`, `mutants.txt`) —
  трек g-upgrade после тега; здесь — проверка на временном `go.work` (AC5) и раздел «Потребителю».
- Прочие ответы кита вне таблиц операций — `413 BODY_TOO_LARGE`, `503 REQUEST_TIMEOUT`, `WWW-Authenticate` у 401
  — не пункт шаблона: хвост в бэклог (S3).
- `httpkit.Healthcheck` остаётся на `logkit.Config{}`: его контракт — причина отказа в stdout при любом `LOG_*`.
- Пункты бэклога golibinfra рядом с треком без пересечения: `httpkit-ratelimit-overflow-bucket` (поведение
  лимитера, а не контракт), `logkit-redact-disabled-literal` (путь литерала; помощник идёт путём ENV, где
  `NewRedactConfig` проверяет и выключенный redact), `kit-form-leftovers` (`ServiceConfig` помощник, как `Module`,
  не читает).

## Критерии приёмки
- [ ] **AC1** — При включённом лимитере сервера (`HTTP_RATE_LIMIT_RPS > 0`) у каждой операции OpenAPI — публичной
  и под `Security`, с таблицей `Declare` и без, `/health` и `/ready`, добавленной в OpenAPI мимо `huma.Register` —
  ответ 429 со схемой `APIError` (`application/problem+json`), кодом `RATE_LIMITED` в описании и заголовком
  `Retry-After` (целое от 1, обязательный); код и заголовок — те, что лимитер отвечает на этой операции. При
  `HTTP_RATE_LIMIT_RPS=0` 429 у операции — только из её таблицы. Проверка: `go test -race -run
  TestErrorMap_DescribesServerRateLimit ./httpkit/` → PASS; на `90adb36` — FAIL (у операции без таблицы 429 нет)
- [ ] **AC2** — 429 не задваивается: таблица с 429 `RATE_LIMITED` (свой лимитер потребителя, как `authlimit.Rule`)
  при включённом лимитере даёт описание `Too Many Requests: RATE_LIMITED`, с другим кодом 429 — `Too Many Requests:
  AUTH_LOCKED, RATE_LIMITED` (коды таблицы — первыми); `Retry-After` — у каждого 429 операции. Проверка: тот же
  тест → PASS; на `90adb36` — FAIL (нет `Retry-After`)
- [ ] **AC3** — `logkit.NewFromEnv()` собирает `Logger` команды из `LOG_*` и `LOG_REDACT_*` тем же ридером ENV,
  что граф `logkit.Module` с `NewConfig` и `NewRedactConfig`: при одном окружении записи двух логгеров совпадают
  (формат, уровень, маски, замена, `source`); негодные `LOG_LEVEL`, `LOG_FORMAT`, `LOG_SILENT`,
  `LOG_REDACT_PATTERNS` (и при `LOG_REDACT_DISABLED=true`), `LOG_REDACT_REPLACE_WITH` — `logkit.ErrConfig`; при
  `LOG_SILENT=true` stdout пуст, записи `ERROR` — в stderr. Проверка: `go test -race -run TestNewFromEnv
  ./logkit/` → PASS; на `90adb36` — не собирается (нет `NewFromEnv`)
- [ ] **AC4** — Доки называют новое: README httpkit и `docs/` (контракт ошибок, `ErrorMap`, `RateLimitConfig`,
  тестирование), README logkit (без fx, API, `LOG_SILENT`, инварианты), строки индекса корневого README; сниппет
  `NewFromEnv` держит `readme_test.go`. Проверка: `make envdoc-check` → OK; `go test -run TestReadme ./logkit/` →
  PASS
- [ ] **AC5** — Шаблон на ветке (временный `go.work` в `N/wt-g-kits-t` от `main` шаблона): `go build ./...` → OK;
  `openapi.json` по `make gen` (`go test ./cmd/api -run '^TestOpenAPISpec$' -args -update`) отличается от
  закоммиченного только 429: новые 429 `RATE_LIMITED` с `Retry-After` у операций без них, у `/api/auth/*` — прежнее
  описание без повтора кода и `Retry-After`; правки «Потребителю» на пробу (без коммита) дают зелёные
  `TestOpenAPISpec`, `TestHTTPOperations`, тесты `authlimit`, archtest и сборку команд на `logkit.NewFromEnv`.
  Проверка: прогоны в `N/wt-g-kits-t`, итог — в «Подтверждено»

## Шаги
### Киты
- [ ] **S1** — httpkit: 429 лимитера сервера и `Retry-After` в контракте операций (`describeErrors`),
  регрессионный тест, доки ошибок и OpenAPI httpkit; коммит `fix(httpkit)!:` с `BREAKING CHANGE:`
  - Проверка: новый тест на `90adb36` → FAIL, на ветке → PASS; `go test -short -race ./httpkit/...
    ./testkit/handlertest/...` → PASS; `flock N/gate.lock make check` → `✓ check clean (./...)`
  - Закрывает: AC1, AC2, AC4 · Зависит от: —
- [ ] **S2** — logkit: `NewFromEnv` — тесты паритета с `Module`, ошибок конфига и `LOG_SILENT`; README logkit,
  строка индекса корневого README
  - Проверка: `go test -short -race ./logkit/...` → PASS; `make envdoc-check` → OK; `flock N/gate.lock make
    check` → `✓ check clean (./...)`
  - Закрывает: AC3, AC4 · Зависит от: —
### Потребитель
- [ ] **S3** — проверка на шаблоне через временный `go.work`; «Потребителю» и строки аннотации сверены с итогом,
  хвост — в бэклог
  - Проверка: прогоны AC5 → как в критерии; `flock N/gate.lock make check` → `✓ check clean (./...)`
  - Закрывает: AC5 · Зависит от: S1, S2

## Решения
- Строки аннотации `v1.6.0` и «Потребителю» — подразделы `## Handoff`: так их берёт `/release` (профиль плана),
  других разделов формат планов не знает.

## Handoff
### Аннотация v1.6.0 — строки трека
Breaking:
- httpkit: при включённом лимитере (`HTTP_RATE_LIMIT_RPS > 0`) каждая операция в OpenAPI объявляет ответ
  `429 RATE_LIMITED` (схема `APIError`), у каждого 429 — заголовок `Retry-After` → перегенерировать контракт
  (`openapi.json`); своё объявление 429 лимитера в таблицах снять — его описывает кит; тесты, которые держат набор
  ответов операций, — под новый набор.

Изменения:
- logkit: `NewFromEnv()` — `Logger` команды вне fx из `LOG_*` и `LOG_REDACT_*`, как граф с `Module`; при
  `LOG_SILENT=true` записи `ERROR` — в stderr.

### Потребителю — go-backend-template (трек g-upgrade)
1. `go get git.stit.tech/stit-core/golibinfra@v1.6.0 && go mod tidy`; `make gen` — `openapi.json`: 429
   `RATE_LIMITED` с `Retry-After` у каждой операции.
2. 429 строгого лимита описывает кит (тот же код и `Retry-After`): `authlimit.Rule` и `errLimited` удалить,
   правило — из таблиц операций `/api/auth/*`; таблицы из него одного (`logoutErrors`,
   `requestPasswordResetErrors`) — вместе с `Declare`.
3. `TestHTTPOperations`: проверку «429 `RATE_LIMITED` — ровно под `authlimit.Prefix`» снять — 429 у каждой
   операции держит тест кита; `authlimit_test.go` сверяет код отказа с кодом лимитера кита.
4. `cmd/migrate`, `cmd/seed-admin`: `newLog()` → `logkit.NewFromEnv()`; `newLog` и оба `TestNewLog` удалить.
5. Доки и обвязка под пп. 2–4; бэклог — снять `golibinfra-httpkit-429` и `golibinfra-logkit-env`.

## Где мы сейчас
План заведён. Следующий шаг — S1: регрессионный тест 429 (на `90adb36` красный), затем `describeErrors`.
