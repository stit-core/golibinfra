---
status: in-progress
parent: go-backend-template:reaudit-to-nine
blocked-by: —
---

# close-httpkit-logkit-gaps — ответы кита в контракте httpkit, логгер команды из LOG_* в logkit

## Цель
Закрыть в китах два пункта бэклога шаблона `go-backend-template` (программа `reaudit-to-nine`, шаг S9, трек
g-kits) и пункт `httpkit-kit-errors-contract`, добавленный дирижёром: ответы, которые httpkit отдаёт на операции
сам (429 лимитера, 503 дедлайна, 413 лимита тела, 401 с `WWW-Authenticate`), описаны в контракте операций там и
тогда, где кит их отдаёт, а короткоживущая команда вне fx получает логгер одной строкой `logkit` с тем же разбором
`LOG_*`, что у графа с `logkit.Module`. Потребитель получает всё тегом `v1.6.0`.

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
- `httpkit-kit-errors-contract` (S4, поручение дирижёра): на `cc5fa17` кит отдаёт `413 BODY_TOO_LARGE` операции huma
  с телом под лимитом, `503 REQUEST_TIMEOUT` дедлайна (`httpkit/deadline.go`, кроме потока SSE), `503
  AUTH_UNAVAILABLE` от extractor'а схемы и `WWW-Authenticate` у 401 `HumaAuth` (`httpkit/middleware.go`), а
  `describeErrors` их не описывает; 401 он описывает и у операции с необязательной аутентификацией, где `HumaAuth`
  401 не отдаёт. Строгий лимит `/api/auth/*` — собственный лимитер шаблона: его 429 шаблон описывает сам (S5).

## Не входит
- Правки шаблона (`go.mod`, `TestHTTPOperations`, `newLog`, `openapi.json`, доки) — трек g-upgrade после тега;
  здесь — проверка на временном `go.work` (AC5, AC7) и раздел «Потребителю».
- Остатки S4 — пунктами бэклога: `503 AUTH_UNAVAILABLE` у операций `hubkit/sso`
  (`httpkit-auth-unavailable-scheme`), 413 у multipart-операции (`httpkit-multipart-body-limit`), свой 503
  `/ready` (`httpkit-ready-503-contract`).
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
  закоммиченного только ответами-ошибками кита: 429 `RATE_LIMITED` с `Retry-After` у операций без него, у
  `/api/auth/*` — прежнее описание 429 без повтора кода и `Retry-After`. Проверка: прогоны в `N/wt-g-kits-t`, итог —
  в «Подтверждено»
  - Подтверждено: на `8719533` (`N/g-kits/s5-template.log`, скрипт `N/g-kits/trial2/s5-check.sh`) — `go build
    ./...` → OK; меняются только ответы-ошибки: 429 `RATE_LIMITED` у 7 операций вне `/api/auth/*` (с `/health` и
    `/ready`), у 8 операций `/api/auth/*` описание 429 прежнее, у всех 429 — обязательный `Retry-After`; 503
    `REQUEST_TIMEOUT` у 15 операций, 413 `BODY_TOO_LARGE` у 7 операций с телом, `WWW-Authenticate` (`Cookie`) у 401
    шести операций под `Security`; повторов кода нет; `TestHTTPOperations` шаблона без правок падает только
    проверкой «429 только под `/api/auth/`» (7 операций)
- [x] **AC6** — Ответы, которые кит отдаёт сам, — в контракте там и тогда, где он их отдаёт, коды не повторяются,
  описание совпадает с ответом кита: `413 BODY_TOO_LARGE` — у операции huma с телом под лимитом (не multipart, не
  маршрут мимо huma); `503 REQUEST_TIMEOUT` — при дедлайне запроса, кроме потока SSE; 401 `HumaAuth` с
  необязательным `WWW-Authenticate` (вызов схем операции) — только у операции huma с обязательной аутентификацией;
  `503 AUTH_UNAVAILABLE` объявляет таблица владельца схемы, кит код не повторяет. Проверка: `go test -race -run
  TestErrorMap_DescribesKitResponses ./httpkit/` → PASS; на `cc5fa17` — FAIL
  - Подтверждено: на `cc5fa17` тест красный — нет 413 и 503, у 401 нет `WWW-Authenticate`, у операции с `{}` 401
    есть, кит отвечает 413 и 503 вне контракта (`N/g-kits/red-s4-kit-errors.log`); на ветке — PASS
    (`green-s4-kit-errors.log`); мутации K1–K11 (условия 401, 503 и 413, multipart, маршрут мимо huma, вызов и
    обязательность заголовка, код дедлайна) тест убивает (`N/g-kits/mutations.sh`, `mutations.log`)
- [x] **AC7** — Шаблон на ветке (`go.work`) в обоих режимах: при умолчаниях `openapi.json` меняется только в
  ответах-ошибках, без повтора кодов; при `HTTP_RATE_LIMIT_RPS=0` и включённом строгом лимите у 8 операций
  `/api/auth/*` в контракте 429 `RATE_LIMITED` (правило шаблона), у остальных 429 нет, а строгий лимит отвечает 429
  `RATE_LIMITED` с `Retry-After`; правки «Потребителю» на пробу — тесты шаблона зелёные. Проверка: прогоны в
  `N/wt-g-kits-t`, итог — в «Подтверждено»
  - Подтверждено: на `8719533` (`N/g-kits/s5-template.log`) — умолчания: как в AC5; `HTTP_RATE_LIMIT_RPS=0` при
    строгом лимите 1/1 (`middlewares` шаблона, проба `N/g-kits/trial2/zz_gkits_limit_test.go`): у 8 операций
    `/api/auth/*` 429 `RATE_LIMITED` с обязательным `Retry-After` из `authlimit.Rule`, у прочих 429 нет, строгий
    лимит отвечает 429 `RATE_LIMITED` с `Retry-After`; правки «Потребителю» (`N/g-kits/trial2/template-trial.patch`,
    5 файлов) — контракт байт в байт тот же, `go vet -tags=integration ./...`, `go test ./...`, archtest, тесты
    `authlimit` и `TestRun_Usage` команды миграций — зелёные

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
### Поручение дирижёра
- [x] **S4** — httpkit: 413, 503 дедлайна и `WWW-Authenticate` у 401 — в контракте операций там, где кит их отдаёт;
  401 — только при обязательной аутентификации операции huma; `AUTH_UNAVAILABLE` — таблицей владельца схемы;
  остатки — в бэклог; коммит `fix(httpkit)!:`
  - Проверка: новый тест на `cc5fa17` → FAIL, на ветке → PASS; мутации `N/g-kits/mutations.sh` убиты; `flock
    N/gate.lock make check` → `✓ check clean (./...)`
  - Закрывает: AC6 · Зависит от: S1
  - Сделано: `describeErrors(cfg, registered)` — 503 `codeRequestTimeout` при `HTTP_REQUEST_TIMEOUT > 0` (кроме
    `eventStream`), 413 `codeBodyTooLarge` у операции huma с телом при `MaxBodyBytes > 0` (кроме multipart), 401 и
    `WWW-Authenticate` (пример — `challenge` схем) — у операции huma без пустой альтернативы `Security`;
    `TestErrorMap_DescribesKitResponses`; доки — README httpkit, `docs/{architecture,usage,configuration,testing}.md`,
    индекс корневого README; бэклог: снят `httpkit-kit-errors-contract`, заведены
    `httpkit-auth-unavailable-scheme`, `httpkit-multipart-body-limit`, `httpkit-ready-503-contract`; мутации
    M1–M9, L1–L4, K1–K11 убиты; `make check` → `✓ check clean (./...)`
- [x] **S5** — «Потребителю» по 429 пересмотрен: шаблон оставляет описание своего строгого лимита, снимается только
  дублирующее; шаблон на ветке проверен в обоих режимах, пробный патч обновлён
  - Проверка: прогоны AC7 → как в критерии; `flock N/gate.lock make check` → `✓ check clean (./...)`
  - Закрывает: AC7 · Зависит от: S4
  - Сделано: проба на шаблоне — как в AC5 и AC7; «Потребителю» и строка Breaking аннотации по 429 переписаны
    (`authlimit.Rule` остаётся, граф проверки 429 в `TestHTTPOperations` — без общего лимитера); «Решения»
    обновлены; `make check` → `✓ check clean (./...)`

## Решения
- Строки аннотации `v1.6.0` и «Потребителю» — подразделы `## Handoff`: так их берёт `/release` (профиль плана),
  других разделов формат планов не знает.
- 429 кит объявляет только при включённом лимитере сервера (`HTTP_RATE_LIMIT_RPS > 0`) — тот же флаг, что ставит
  `RateLimit` в цепочку: контракт не обещает ответа, которого сервер не даёт. Свой лимитер потребителя на части путей
  описывает его владелец правилом таблицы и при включённом лимитере сервера: кит код не повторит, а при выключенном
  429 своего лимитера остаётся в контракте.
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
- Проба «Потребителю» — в своём worktree шаблона без коммита (скрипт и патч — `N/g-kits/trial2/`): правки шаблона
  делает g-upgrade после тега, а проба показывает, что путь рабочий и контракт после него байт в байт тот же.
- Шаблон оставляет `authlimit.Rule` (поручение дирижёра, S5): строгий лимит `/api/auth/*` — его собственный лимитер,
  и при `HTTP_RATE_LIMIT_RPS=0` его 429 держит в контракте только это правило (проба: ответ 429 остаётся). Дублирует
  кит лишь проверку `TestHTTPOperations` «429 только под `/api/auth/`»: с общим лимитером 429 у каждой операции
  объявляет кит, поэтому её граф собирается без общего лимитера, а сама проверка остаётся. Футер коммита `c84ebbc`
  советует снять своё правило — для аннотации действует строка Breaking раздела Handoff.
- Ответы кита — только там, где он их отдаёт: 401 и 413 — у операции huma (`Server.registered` — операция из
  `huma.Register` этого сервера); у маршрута мимо huma, описанного руками, 401 и 413 — ответы его хендлера
  (`HumaAuth` его не видит, тело режет `MaxBody`, а отвечает он сам); 429 и 503 дедлайна стоят в цепочке и у него.
- 401 — только при обязательной аутентификации: при пустой альтернативе (`{}`) `HumaAuth` на отказ схемы пускает
  запрос без личности, 401 не отдаёт — прежнее описание 401 у такой операции неверное.
- `WWW-Authenticate` — необязательный заголовок 401 с примером — вызовом схем операции: его ставит `HumaAuth`, а свой
  401 хендлера на той же операции — нет.
- 503 `REQUEST_TIMEOUT` — у каждой операции, кроме потока SSE: тот же признак `eventStream`, что снимает дедлайн в
  `operationDeadline`.
- 413 — по итоговому `MaxBodyBytes` операции (хук описания — последний и видит лимит хука потребителя); multipart
  исключён: форму huma разбирает сама, превышение у неё — 422 (проба на ветке); поведение — бэклог
  `httpkit-multipart-body-limit`, строка доки «Превышение — всегда 413» поправлена.
- `503 AUTH_UNAVAILABLE` кит не описывает: его отдаёт только extractor, знающий недоступный бэкенд (`hubkit/sso` —
  да, локальный JWT шаблона — нет), и у всех операций под `Security` описание было бы ложным. Объявляет владелец
  схемы правилом таблицы — тем же путём, что шаблон объявляет коды 401 своего extractor'а (`authgate.Secured`); кит
  сводит код без повтора (тест). `hubkit/sso` вне объёма и правится только под API — бэклог
  `httpkit-auth-unavailable-scheme`.
- `/ready` получает 503 дедлайна, как прочие операции; его собственный 503 (`draining`, `unhealthy`) контракт не
  описывает — бэклог `httpkit-ready-503-contract`.
- Код 413 — константа `codeBodyTooLarge` рядом с `MaxBody`, как `codeRateLimited`: её отдают huma (`codeForStatus`) и
  `Idempotency` и объявляет хук. Вне `errormap.go` — по строке в `httpkit/{maxbody,error,idempotency,server}.go` и
  метод `Server.registered`.
- S4 — `fix(httpkit)!`: контракт операций меняется (новые ответы, снятый 401 у необязательной аутентификации) —
  перегенерация контракта и тестов потребителя.

## Handoff
### Аннотация v1.6.0 — строки трека
Breaking:
- httpkit: при включённом лимитере (`HTTP_RATE_LIMIT_RPS > 0`, по умолчанию) каждая операция в OpenAPI объявляет
  ответ `429 RATE_LIMITED` (схема `APIError`; код — после кодов 429 таблицы, без повтора), у каждого 429 —
  обязательный заголовок `Retry-After` (целое от 1) → перегенерировать закоммиченный контракт (`openapi.json`) и
  поправить тесты, которые держат набор ответов операций; своё правило 429 `RATE_LIMITED` (свой лимитер на части
  путей) оставить: код не повторится, а при выключенном лимитере сервера 429 своего лимитера останется в контракте.

- httpkit: в OpenAPI операций — ответы, которые кит отдаёт сам: `503 REQUEST_TIMEOUT` при `HTTP_REQUEST_TIMEOUT > 0`
  (кроме потока SSE), `413 BODY_TOO_LARGE` у операции huma с телом под лимитом (кроме multipart), у 401 `HumaAuth` —
  необязательный `WWW-Authenticate`; `401 UNAUTHORIZED` — только у операции huma с обязательной аутентификацией (не
  при `{}` и не у маршрута мимо huma) → перегенерировать контракт и тесты набора ответов; `503 AUTH_UNAVAILABLE`
  своей схемы объявить правилом таблицы её операций (`Err: httpkit.ErrAuthUnavailable`).

Изменения:
- logkit: `NewFromEnv()` — `Logger` команды вне fx из `LOG_*` и `LOG_REDACT_*`, как граф с `Module`; при
  `LOG_SILENT=true` записи `ERROR` — в stderr.

### Потребителю — go-backend-template (трек g-upgrade)
Пп. 2–3 на пробу — `N/g-kits/trial2/template-trial.patch` (скрипт `trial2/apply.py`, прогон
`N/g-kits/s5-template.log`): с ними `openapi.json` байт в байт тот же, что после одного `make gen`.
1. `go get git.stit.tech/stit-core/golibinfra@v1.6.0 && go mod tidy`; `make gen` — в `openapi.json` ответы кита:
   429 `RATE_LIMITED` с обязательным `Retry-After` у каждой операции (у 8 операций `/api/auth/*` описание 429
   прежнее), 503 `REQUEST_TIMEOUT` у каждой, 413 `BODY_TOO_LARGE` у 7 операций с телом, необязательный
   `WWW-Authenticate` (`Cookie`) у 401 шести операций под `Security`; повторов кода нет.
2. `authlimit.Rule` в таблицах `/api/auth/*` оставить: это описание собственного строгого лимита шаблона — при
   `HTTP_RATE_LIMIT_RPS=0` и включённом строгом лимите 429 у `/api/auth/*` держит только оно, а при общем лимитере
   кит код не повторяет. Дублирует только проверка `TestHTTPOperations` «429 `RATE_LIMITED` — ровно под
   `authlimit.Prefix`»: граф этого теста собирать без общего лимитера (`fx.Decorate`: `RateLimit.RPS = 0`), проверку
   оставить.
3. `cmd/migrate`, `cmd/seed-admin`: `newLog()` → `logkit.NewFromEnv()`; `newLog`, оба `TestNewLog` и импорт
   `logkit` в `cmd/migrate/run_test.go` удалить — разбор `LOG_*`, `ErrConfig` и stderr держат тесты кита
   (`TestNewFromEnv_*`). Команды начинают читать `LOG_REDACT_*` и при `LOG_SILENT=true` пишут записи `ERROR` в
   stderr.
4. Доки и обвязка: `docs/reference/architecture.md` (§ Контракт ошибок API — «у операций вне префикса его нет»: 429
   общего лимитера кит объявляет у каждой операции, правило держит 429 строгого лимита и при выключенном общем;
   ответы кита — 503 дедлайна, 413, `WWW-Authenticate`), `.claude/skills/scaffold/references/endpoint.md` (строка
   про `authlimit.Rule` и `TestHTTPOperations`), пример `newLog()` в `TestMainOnlyEnters`
   (`internal/archtest/generated_test.go`, текст случая); `make mutate A=--check` — якоря `mutants.txt`; бэклог —
   снять `golibinfra-httpkit-429` и `golibinfra-logkit-env`.
5. Схема с бэкендом (JWKS, хаб) объявляет `503 AUTH_UNAVAILABLE` правилом таблицы своих операций
   (`Err: httpkit.ErrAuthUnavailable`); шаблону с локальным JWT не нужно.

## Где мы сейчас
Шаги S1–S5 закрыты, критерии AC1–AC7 подтверждены: ответы кита (429, 503 дедлайна, 413, 401 с `WWW-Authenticate`) —
в контракте операций там, где кит их отдаёт, `logkit.NewFromEnv` — логгер команды одной строкой, шаблон на ветке
проверен в обоих режимах лимитеров. План держит Handoff до выпуска `v1.6.0`: дирижёр после валидатора сливает ветку,
переносит строки в аннотацию тега и закрывает план (`chore(plan): close-httpkit-logkit-gaps закрыт`).
