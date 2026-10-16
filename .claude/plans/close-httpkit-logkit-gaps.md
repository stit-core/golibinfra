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
- Прочие ответы кита вне таблиц операций — `413 BODY_TOO_LARGE`, `503 REQUEST_TIMEOUT`, `503 AUTH_UNAVAILABLE`,
  `WWW-Authenticate` у 401 — не пункт шаблона: хвост `httpkit-kit-errors-contract` в бэклоге (S3).
- `httpkit.Healthcheck` остаётся на `logkit.Config{}`: его контракт — причина отказа в stdout при любом `LOG_*`.
- Пункты бэклога golibinfra рядом с треком без пересечения: `httpkit-ratelimit-overflow-bucket` (поведение
  лимитера, а не контракт), `logkit-redact-disabled-literal` (путь литерала; помощник идёт путём ENV, где
  `NewRedactConfig` проверяет и выключенный redact), `kit-form-leftovers` (`ServiceConfig` помощник, как `Module`,
  не читает).

## Критерии приёмки
- [x] **AC1** — При включённом лимитере сервера (`HTTP_RATE_LIMIT_RPS > 0`) у каждой операции OpenAPI — публичной
  и под `Security`, с таблицей `Declare` и без, `/health` и `/ready`, добавленной в OpenAPI мимо `huma.Register` —
  ответ 429 со схемой `APIError` (`application/problem+json`), кодом `RATE_LIMITED` в описании и заголовком
  `Retry-After` (целое от 1, обязательный); код и заголовок — те, что лимитер отвечает на этой операции. При
  `HTTP_RATE_LIMIT_RPS=0` 429 у операции — только из её таблицы. Проверка: `go test -race -run
  TestErrorMap_DescribesServerRateLimit ./httpkit/` → PASS; на `90adb36` — FAIL (у операции без таблицы 429 нет)
  - Подтверждено: на `90adb36` тест красный — у `ping`, `getMe`, `stream`, `/health`, `/ready` 429 нет, лимитер
    отвечает `RATE_LIMITED` с `Retry-After: 1`, а контракт `ping` 429 не знает (`N/g-kits/red-s1-429.log`); на
    ветке — PASS (`N/g-kits/green-s1-429.log`); ручные мутации хука и лимитера M1–M9 (флаг лимитера, заголовок у 429
    таблицы, повтор и порядок кода, `Responses == nil`, `Required`, минимум, код лимитера) тест убивает
    (`N/g-kits/mutations.sh`, лог `mutations.log`)
- [x] **AC2** — 429 не задваивается: таблица с 429 `RATE_LIMITED` (свой лимитер потребителя, как `authlimit.Rule`)
  при включённом лимитере даёт описание `Too Many Requests: RATE_LIMITED`, с другим кодом 429 — `Too Many Requests:
  AUTH_LOCKED, RATE_LIMITED` (коды таблицы — первыми); `Retry-After` — у каждого 429 операции. Проверка: тот же
  тест → PASS; на `90adb36` — FAIL (нет `Retry-After`)
  - Подтверждено: тот же тест — на `90adb36` у `logout` и `login` нет `Retry-After`, `login` без `RATE_LIMITED`; на
    ветке — PASS; шаблон на ветке (`go.work`): у `/api/auth/*` описание 429 прежнее, код не повторён
    (`N/g-kits/template-openapi-s1.diff`)
- [x] **AC3** — `logkit.NewFromEnv()` собирает `Logger` команды из `LOG_*` и `LOG_REDACT_*` тем же ридером ENV,
  что граф `logkit.Module` с `NewConfig` и `NewRedactConfig`: при одном окружении записи двух логгеров совпадают
  (формат, уровень, маски, замена, `source`); негодные `LOG_LEVEL`, `LOG_FORMAT`, `LOG_SILENT`,
  `LOG_REDACT_PATTERNS` (и при `LOG_REDACT_DISABLED=true`), `LOG_REDACT_REPLACE_WITH` — `logkit.ErrConfig`; при
  `LOG_SILENT=true` stdout пуст, записи `ERROR` — в stderr. Проверка: `go test -race -run TestNewFromEnv
  ./logkit/` → PASS; на `90adb36` — не собирается (нет `NewFromEnv`)
  - Подтверждено: `TestNewFromEnv_ParsesLikeModule` (умолчания; text и debug; свои маски, замена, `source` и
    уровень error; выключенный redact — записи двух логгеров равны, окружение в них видно),
    `TestNewFromEnv_RejectsInvalidEnv` (8 случаев, в том числе паттерн у выключенного redact),
    `TestNewFromEnv_SilentKeepsErrorsInStderr` → PASS (`N/g-kits/green-s2-logkit.log`); на `90adb36` `NewFromEnv`
    нет (`git grep` пуст); ручные мутации L1–L4 (без `LOG_REDACT_*`, без stderr, без ошибки `NewRedactConfig` и
    `NewConfig`) тесты убивают (`N/g-kits/mutations.log`)
- [x] **AC4** — Доки называют новое: README httpkit и `docs/` (контракт ошибок, `ErrorMap`, `RateLimitConfig`,
  тестирование), README logkit (без fx, API, `LOG_SILENT`, инварианты), строки индекса корневого README; сниппет
  `NewFromEnv` держит `readme_test.go`. Проверка: `make envdoc-check` → OK; `go test -run TestReadme ./logkit/` →
  PASS
  - Подтверждено: `make envdoc-check` → OK (254 переменных); `TestReadme_QuickStartWithoutFx` с `NewFromEnv` → PASS
- [x] **AC5** — Шаблон на ветке (временный `go.work` в `N/wt-g-kits-t` от `main` шаблона): `go build ./...` → OK;
  `openapi.json` по `make gen` (`go test ./cmd/api -run '^TestOpenAPISpec$' -args -update`) отличается от
  закоммиченного только 429: новые 429 `RATE_LIMITED` с `Retry-After` у операций без них, у `/api/auth/*` — прежнее
  описание без повтора кода и `Retry-After`; правки «Потребителю» на пробу (без коммита) дают зелёные
  `TestOpenAPISpec`, `TestHTTPOperations`, тесты `authlimit`, archtest и сборку команд на `logkit.NewFromEnv`.
  Проверка: прогоны в `N/wt-g-kits-t`, итог — в «Подтверждено»
  - Подтверждено: на `402ed8c` (`N/g-kits/s3-template.log`, скрипт `N/g-kits/trial/s3-check.sh`) — `go build ./...`
    → OK; `openapi.json` меняется только в 429: у 7 операций вне `/api/auth/*`, с `/health` и `/ready`, — новый 429
    `RATE_LIMITED`, у 8 операций `/api/auth/*` описание прежнее, повторов кода нет, у всех — обязательный
    `Retry-After`; `TestHTTPOperations` шаблона без правок падает только проверкой «429 только под `/api/auth/`» (7
    операций); с правками «Потребителю» (`N/g-kits/trial/template-trial.patch`) контракт байт в байт тот же, `go vet
    -tags=integration ./...`, `go test ./...`, archtest, тесты `authlimit` и `TestRun_Usage` команды миграций — зелёные

## Шаги
### Киты
- [x] **S1** — httpkit: 429 лимитера сервера и `Retry-After` в контракте операций (`describeErrors`),
  регрессионный тест, доки ошибок и OpenAPI httpkit; коммит `fix(httpkit)!:` с `BREAKING CHANGE:`
  - Проверка: новый тест на `90adb36` → FAIL, на ветке → PASS; `go test -short -race ./httpkit/...
    ./testkit/handlertest/...` → PASS; `flock N/gate.lock make check` → `✓ check clean (./...)`
  - Закрывает: AC1, AC2, AC4 · Зависит от: —
  - Сделано: `describeErrors(rateLimited)` объявляет 429 `codeRateLimited` у каждой операции при
    `HTTP_RATE_LIMIT_RPS > 0` (код — после кодов таблицы, без повтора; ответов нет — ответ со схемой `APIError`) и
    обязательный `Retry-After` (целое от 1) у любого 429; `TestErrorMap_DescribesServerRateLimit` на `90adb36` →
    FAIL, на ветке → PASS; `TestErrorMap_Declare` — код кита после `AUTH_LOCKED`; доки — README httpkit,
    `docs/{architecture,usage,configuration,testing}.md`, строка индекса корневого README; `go test -short -race
    ./httpkit/... ./testkit/handlertest/...` → PASS; `make check` → `✓ check clean (./...)`
- [x] **S2** — logkit: `NewFromEnv` — тесты паритета с `Module`, ошибок конфига и `LOG_SILENT`; README logkit,
  строка индекса корневого README
  - Проверка: `go test -short -race ./logkit/...` → PASS; `make envdoc-check` → OK; `flock N/gate.lock make
    check` → `✓ check clean (./...)`
  - Закрывает: AC3, AC4 · Зависит от: —
  - Сделано: `logkit.NewFromEnv()` — `NewConfig`, `NewRedactConfig` и `New` с `WithRedact` и `WithStderrFallback`;
    тесты `logkit/fromenv_test.go`; README logkit (без fx, `LOG_SILENT`, API, инварианты), сниппет в
    `readme_test.go`, doc-comment `WithStderrFallback`, строка индекса корневого README; `go test -short -race
    ./logkit/...` → PASS; `make envdoc-check` → OK; `make check` → `✓ check clean (./...)`
### Потребитель
- [x] **S3** — проверка на шаблоне через временный `go.work`; «Потребителю» и строки аннотации сверены с итогом,
  хвост — в бэклог
  - Проверка: прогоны AC5 → как в критерии; `flock N/gate.lock make check` → `✓ check clean (./...)`
  - Закрывает: AC5 · Зависит от: S1, S2
  - Сделано: проба на шаблоне — как в AC5; `make release-diff PREV=v1.5.0` — несовместимого API нет, новое —
    `logkit.NewFromEnv` (`N/g-kits/release-diff.log`), Breaking S1 — по футеру коммита; «Потребителю» уточнён по
    пробе; хвост — `httpkit-kit-errors-contract` в бэклог; `make check` → `✓ check clean (./...)`

## Решения
- Строки аннотации `v1.6.0` и «Потребителю» — подразделы `## Handoff`: так их берёт `/release` (профиль плана),
  других разделов формат планов не знает.
- 429 кит объявляет только при включённом лимитере сервера (`HTTP_RATE_LIMIT_RPS > 0`) — тот же флаг, что ставит
  `RateLimit` в цепочку: контракт не обещает ответа, которого сервер не даёт. Свой лимитер потребителя на части путей
  при выключенном лимитере сервера объявляется правилом таблицы — кит сведёт код без повтора.
- `Retry-After` — у любого 429 операции, и из таблицы: кит ставит его на каждый ответ 429 (`APIError.retryAfter`,
  не меньше секунды), поэтому заголовок обязательный, целое от 1.
- `default` huma у операции без таблицы остаётся рядом с 429 кита, как рядом с 401 у `Security`: хук идёт после
  `defineErrors` huma, и решать за huma о 422 и 500 кит не берётся.
- Операция, которую потребитель описал в OpenAPI мимо `huma.Register` без ответов (`Responses == nil`), получает 429
  без паники: лимитер стоит и на её маршруте.
- Код 429 — константа `codeRateLimited` рядом с лимитером, как `codeUnauthorized` у `HumaAuth`: её отдают лимитер и
  `codeForStatus` и объявляет хук. Ради этого вне `errormap.go` — строка в `httpkit/ratelimit.go`, `httpkit/error.go`
  и вызов хука в `httpkit/server.go`; иначе ответ и описание держали бы два литерала.
- S1 — `fix(httpkit)!`: контракт не описывал ответ сервера (дефект с регрессионным тестом), а перегенерация
  контракта и тестов потребителя — breaking.
- `NewFromEnv` — без опций: команде нужна одна строка, свой конфиг или опции — `New`; имя — конструктор `New*` с
  источником, рядом с `New` и `NewSlog` (`FromEnv` спутался бы с `FromSlog` — обёрткой готового логгера).
  `ServiceConfig` он, как `Module`, не читает.
- `NewFromEnv` ставит `WithStderrFallback`: у логгера команды OTel-sink'а нет по построению — правило кита для таких
  логгеров (`FxLogger`, логгер ошибок экспорта telemetry); иначе при `LOG_SILENT=true` команда теряет причину отказа.
  Разбор `LOG_*` тот же, что у графа с `Module`: `TestNewFromEnv_ParsesLikeModule` сверяет записи двух логгеров.
- Проба «Потребителю» — в своём worktree шаблона без коммита (скрипт и патч — `N/g-kits/trial/`): правки шаблона
  делает g-upgrade после тега, а проба показывает, что путь рабочий и контракт после него байт в байт тот же.
- Операциям `/api/auth/*` без доменных ошибок (`logoutUser`, `requestPasswordReset`) — `Errors: []int{500}`, а не
  исключение из проверки `default` шаблона: проверка остаётся сторожем забытого `Declare`, набор ответов — прежним.

## Handoff
### Аннотация v1.6.0 — строки трека
Breaking:
- httpkit: при включённом лимитере (`HTTP_RATE_LIMIT_RPS > 0`, по умолчанию) каждая операция в OpenAPI объявляет
  ответ `429 RATE_LIMITED` (схема `APIError`; код — после кодов 429 таблицы, без повтора), у каждого 429 —
  обязательный заголовок `Retry-After` (целое от 1) → перегенерировать закоммиченный контракт (`openapi.json`) и
  поправить тесты, которые держат набор ответов операций; правило 429 `RATE_LIMITED` в таблице ради своего лимитера
  при включённом лимитере сервера не нужно — снять.

Изменения:
- logkit: `NewFromEnv()` — `Logger` команды вне fx из `LOG_*` и `LOG_REDACT_*`, как граф с `Module`; при
  `LOG_SILENT=true` записи `ERROR` — в stderr.

### Потребителю — go-backend-template (трек g-upgrade)
Пп. 2–4 на пробу — `N/g-kits/trial/template-trial.patch` (скрипт `trial/apply.py`, прогон `N/g-kits/s3-template.log`):
с ними `openapi.json` байт в байт тот же, что после одного `make gen`.
1. `go get git.stit.tech/stit-core/golibinfra@v1.6.0 && go mod tidy`; `make gen` — в `openapi.json` 429
   `RATE_LIMITED` с обязательным `Retry-After` у каждой операции, и у `/health`, `/ready`; у `/api/auth/*` описание
   429 прежнее, добавлен `Retry-After`.
2. 429 строгого лимита `/api/auth/*` описывает кит: код и заголовок те же, что у `authlimit.Middleware` (это
   `httpkit.RateLimit`). `authlimit.Rule` и `errLimited` удалить (комментарий `Prefix` — без «правилом Rule»),
   правило — из таблиц `loginErrors`, `refreshErrors`, `changePasswordErrors`, `registerErrors`,
   `confirmPasswordResetErrors`, `confirmEmailErrors`; таблицы из него одного — `logoutErrors`,
   `requestPasswordResetErrors` — удалить вместе с `Declare`, а их операциям — `Errors:
   []int{http.StatusInternalServerError}`: huma описывает 422 и 500, а не `default`, и проверка `default` в
   `TestHTTPOperations` остаётся.
3. `TestHTTPOperations`: проверку «429 `RATE_LIMITED` — ровно под `authlimit.Prefix`» снять (на теге она падает на
   7 операциях вне префикса) — 429 у каждой операции держит `TestErrorMap_DescribesServerRateLimit` кита;
   `authlimit_test.go` сверяет код отказа с `"RATE_LIMITED"` и наличие `Retry-After`.
4. `cmd/migrate`, `cmd/seed-admin`: `newLog()` → `logkit.NewFromEnv()`; `newLog`, оба `TestNewLog` и импорт
   `logkit` в `cmd/migrate/run_test.go` удалить — разбор `LOG_*`, `ErrConfig` и stderr держат тесты кита
   (`TestNewFromEnv_*`). Команды начинают читать `LOG_REDACT_*` и при `LOG_SILENT=true` пишут записи `ERROR` в
   stderr.
5. Доки и обвязка: `docs/reference/architecture.md` (§ Контракт ошибок API — исключение «кроме операции под
   `authlimit.Prefix`» и абзац про `authlimit.Rule`), `.claude/skills/scaffold/references/endpoint.md` (строка про
   `authlimit.Rule`), `scripts/tools/mutants.txt` (якоря `loginErrors` и `confirmEmailErrors` с `authlimit.Rule`),
   пример `newLog()` в `TestMainOnlyEnters` (`internal/archtest/generated_test.go`, текст случая); бэклог — снять
   `golibinfra-httpkit-429` и `golibinfra-logkit-env`.

## Где мы сейчас
Шаги S1–S3 закрыты, критерии подтверждены: 429 лимитера и `Retry-After` — в контракте каждой операции,
`logkit.NewFromEnv` — логгер команды одной строкой, шаблон на ветке проверен. План держит Handoff до выпуска
`v1.6.0`: дирижёр после валидатора сливает ветку, переносит строки в аннотацию тега и закрывает план
(`chore(plan): close-httpkit-logkit-gaps закрыт`).
