---
status: in-progress
parent: —
blocked-by: —
---

# check-cookie-origin — проверка Origin у изменяющего запроса с cookie сессии

## Цель
Сервисы stit держат сессию в cookie, и изменяющий запрос со страницы поддомена того же сайта уходит с ней: такой
запрос — same-site, `SameSite=Strict` его не отсекает, а простой `POST` без `Content-Type` huma читает как JSON, без
preflight. Дать httpkit общий механизм: сервис называет свои cookie сессии, и изменяющий запрос с ними проходит только
с `Origin` из `HTTP_CORS_ORIGINS`. По умолчанию поведение httpkit прежнее; шаблон бэкенда включает проверку следующим
шагом — с тега `v1.8.0`.

## Контекст
- Решение владельца: проверка Origin — механизм httpkit, а не код шаблона (задача сессии корня stit-core).
- Образец механики (только читать): `kapitalstroy-platform/golibinfra/passkit/origin.go`, `extractor.go`,
  `origin_test.go`, README § Подделка запросов. Там проверка — в extractor'е cookie-схемы; здесь её место — цепочка
  chi: публичные операции шаблона `POST /api/auth/refresh` и `/api/auth/logout` читают `refresh_token` входом huma,
  мимо extractor'а.
- Шаблон `go-backend-template`: cookie `access_token` и `refresh_token` (`internal/platform/authgate/cookie.go`,
  `SameSite=Strict`); спецификация собирается графом без старта при конфиге по умолчанию (`cmd/api/api_test.go`,
  `TestOpenAPISpec`).
- Кит: цепочка — `httpkit/server.go` `chain`; ответы кита в OpenAPI — `httpkit/errormap.go` `describeErrors` и
  `kitResponse`; проверки старта — `httpkit/module.go` `serve`; таблицы сигналов сверяет `httpkit/signals_test.go`,
  таблицу ответов кита `httpkit/docs/testing.md` — `httpkit/testingdoc_test.go`.
- Ревью диффа, тег `v1.8.0` и пуш — у сессии корня: здесь коммиты в `main` локально.

## Не входит
- `GET` с побочным эффектом и upgrade WebSocket (`GET`): метод безопасный, проверка его не касается.
- `Sec-Fetch-Site` и `Referer`: правило одно — `Origin` из списка.
- Синтаксис записей `HTTP_CORS_ORIGINS`: как у CORS, запись с путём или другим регистром ни с одним `Origin` не
  совпадёт.
- Включение проверки в `go-backend-template` — шаг шаблона после тега (§ Handoff).
- Свои проверки `oidckit` (`POST /auth/logout` пускает `Sec-Fetch-Site: same-site`) и cookie `hubkit/sso` — киты вне
  объёма аудита; их cookie закрывает `SessionCookies` сервиса.

## Критерии приёмки
- [x] **AC1** — С `SessionCookies` изменяющий запрос (не `GET`, `HEAD`, `OPTIONS`, `TRACE`) с cookie сессии проходит
  только с `Origin`, точно совпавшим с записью `HTTP_CORS_ORIGINS`; без `Origin`, с чужим, с `null` и с адресом,
  который разрешает только `*` списка, — `403` problem+json `ORIGIN_NOT_ALLOWED` до хендлера и extractor'а схемы;
  запрос без этих cookie (Bearer, сервис) и безопасный метод проходят. Проверка: `go test -race -run
  'TestSessionCookies_(Matrix|ConfigOrigins|BeforeAuth|RawRoute)' ./httpkit/` → PASS
  - Подтверждено: `TestSessionCookies_Matrix` (9 методов × 9 Cookie × 9 Origin), `…ConfigOrigins`, `…BeforeAuth`
    (extractor и хендлер не зовутся, тело сверх лимита — 403, а не 413; Bearer без Origin — 204), `…RawRoute` — PASS
- [x] **AC2** — Без `SessionCookies` поведение httpkit прежнее: ожидания прежних тестов кита не правятся, 403 кит в
  OpenAPI не объявляет. Проверка: `go test -race ./httpkit/... ./testkit/handlertest/` → PASS;
  `TestSessionCookies_OpenAPI` — у `POST` сервера без cookie сессии ответа 403 нет
  - Подтверждено: `go test -race ./httpkit/... ./testkit/handlertest/` — PASS, ожидания прежних тестов не тронуты
    (сценарий сигналов и таблица ответов кита дополнены); `TestSessionCookies_OpenAPI` без cookie — 403 только из
    `Declare`; `TestSessionCookies_Module` без значения — `POST` с cookie без Origin 204
- [ ] **AC3** — Включение явное и без новой ENV: значение графа `httpkit.SessionCookies`, без fx —
  `WithSessionCookies`; имя, которого не бывает у cookie, — `ErrConfig` `NewServer`; без адреса в `HTTP_CORS_ORIGINS`
  `Module` не стартует (`ErrConfig`), а граф без старта собирается. Проверка: `go test -race -run
  'TestSessionCookies_(Names|NeedOrigin|Module)' ./httpkit/` → PASS; `make envdoc-check` → OK
  - Подтверждено: `TestSessionCookies_Names`, `…NeedOrigin` (пусто, `*`, `*`+`null`, пробел: `app.Err()` — nil,
    `Start` — `ErrConfig` с `HTTP_CORS_ORIGINS` и именами), `…Module` (`fx.ValidateApp`, живой сервер; без значения —
    прежнее поведение) — PASS; `make envdoc-check` — OK, 256 переменных
- [x] **AC4** — `403 ORIGIN_NOT_ALLOWED` — в OpenAPI каждой операции изменяющего метода (huma, группы huma, мимо huma
  с описанием руками) механизмом ответов кита, после кодов `Declare`; таблица ответов кита `docs/testing.md` со
  столбцом 403 сверена тестом. Проверка: `go test -run 'TestSessionCookies_OpenAPI|TestTestingDoc_KitResponsesTable'
  ./httpkit/` → PASS
  - Подтверждено: `TestSessionCookies_OpenAPI` (`POST`, `PUT` с `Declare` — `Forbidden: FORBIDDEN, ORIGIN_NOT_ALLOWED`,
    `PATCH`, `DELETE`, группа, мимо huma — `APIError`; `GET`, `HEAD`, SSE — без 403; ответ кита — код контракта),
    `TestTestingDoc_KitResponsesTable` — PASS; клетка 403 у `GET` под `Security`, поставленная «да», — отказ теста
- [ ] **AC5** — Отказ виден оператору: `http_origin_rejections_total{path, reason}` (значения `reason` конечны, Origin
  в label нет) и WARN с методом и `Origin` — в таблицах `docs/observability.md`, их сверяет живой `Module`.
  Проверка: `go test -race -run 'TestSignals|TestSessionCookies_Signals' ./httpkit/` → PASS
  - Подтверждено: `TestSignals_MatchObservabilityDoc` (случаи `cookie-no-origin`, `cookie-foreign`, `cookie-app`;
    оба значения `reason` встречены), `TestSignals_CodeMatchesObservabilityDoc`, `TestSessionCookies_Signals` (серии
    `missing`/`not_allowed`/`unmatched`, WARN без значения cookie, длинный Origin обрезан) — PASS
- [x] **AC6** — Тест хендлера потребителя включает проверку и шлёт `Origin`: `handlertest.WithSessionCookies`.
  Проверка: `go test -race -run TestNewAPI_SessionCookies ./testkit/handlertest/` → PASS
  - Подтверждено: `TestNewAPI_SessionCookies` — без Origin и с чужим 403, с адресом конфига и Bearer 204, без опции
    проверки нет, невалидное имя валит тест — PASS
- [ ] **AC7** — Доки описывают проверку — правило, включение, ответ, сигналы, тест потребителя: README кита,
  `httpkit/docs/`, `SECURITY.md`, строки корневого README и `testkit/README.md`. Проверка: ручная —
  `httpkit/docs/architecture.md § Проверка Origin`, `httpkit/docs/usage.md § Сессия в cookie`, `SECURITY.md`
  § Что в скоупе; доки-тесты кита в `make check` → `✓ check clean (./...)`
- [ ] **AC8** — Выпуск `v1.8.0`: аннотация по черновику (`make release-diff PREV=v1.7.0`), тег в origin и github.
  Проверка: `git ls-remote origin refs/tags/v1.8.0` и `git ls-remote github refs/tags/v1.8.0` → тег

## Шаги
### Механизм
- [x] **S1** — httpkit: `SessionCookies` и `WithSessionCookies`, проверка в цепочке после `RateLimit`, отказ имени и
  старта без адреса, метрика и WARN; тесты матрицы, fx и сигналов, таблицы `docs/observability.md`
  - Проверка: `go test -race ./httpkit/` → PASS · Закрывает: AC1, AC3, AC5
  - Сделано: `httpkit/origin.go`, проводка в `server.go`/`module.go`, `http_origin_rejections_total` в
    `metrics.go`; `httpkit/origin_test.go`, случаи и `reason` в `signals_test.go`; таблицы `docs/observability.md`,
    § Проверка Origin и строка конвейера в `docs/architecture.md`; `go test -race ./httpkit/...` — PASS, lint
    `./httpkit/...` — 0 issues
- [x] **S2** — testkit/handlertest: `WithSessionCookies`
  - Проверка: `go test -race ./testkit/handlertest/` → PASS · Закрывает: AC6 · Зависит от: S1
  - Сделано: опция и `TestNewAPI_SessionCookies`; абзац с примером в `httpkit/docs/testing.md`, строка
    `handlertest` в `testkit/README.md`; `go test -race ./testkit/handlertest/` — PASS, lint — 0 issues
- [x] **S3** — httpkit: `403 ORIGIN_NOT_ALLOWED` в OpenAPI изменяющих операций, столбец 403 таблицы `docs/testing.md`
  - Проверка: `go test -race ./httpkit/...` → PASS · Закрывает: AC2, AC4 · Зависит от: S2
  - Сделано: `describeErrors` — `kitResponse` 403 при cookie сессии у метода не из безопасных;
    `TestSessionCookies_OpenAPI`; столбец 403 и строка `DELETE` таблицы ответов кита, строки — с методом;
    § Контракт ошибок `docs/architecture.md`; `go test -race ./httpkit/... ./testkit/handlertest/` — PASS, lint — 0 issues
### Доки и выпуск
- [ ] **S4** — Доки: README кита, `docs/configuration.md` и `docs/usage.md` httpkit, `SECURITY.md`, корневой README
  - Проверка: `make envdoc-check` → OK; ручная — разделы AC7 · Закрывает: AC7 · Зависит от: S3
- [ ] **S5** — Гейт, `## Handoff`, черновик аннотации `v1.8.0` по `make release-diff PREV=v1.7.0`
  - Проверка: `make check` → `✓ check clean (./...)` · Закрывает: AC7 · Зависит от: S4
- [ ] **S6** — Ревью диффа и выпуск `v1.8.0` (`/release`) — сессия корня; план закрывается тем же
  - Проверка: тег в origin и github · Закрывает: AC8 · Зависит от: S5

## Решения
- Проверка — в цепочке chi после `RateLimit`, а не в extractor'е схемы: cookie читают и публичные операции (refresh и
  logout шаблона — входом huma), и маршруты мимо huma; отказ — до тела, `HumaAuth`, extractor'а и хендлера, а лимитер
  считает и отвергнутый запрос.
- Запрос с cookie сессии и `Authorization` — тоже с проверкой: правило — про cookie в запросе. Чужая страница
  `Authorization` без CORS-разрешения не поставит, клиенту с Bearer cookie не нужна (в образце такой запрос открывал
  Bearer).
- Изменяющий — любой метод, кроме безопасных `GET`, `HEAD`, `OPTIONS`, `TRACE` (RFC 9110 §9.2.1): неизвестный метод
  тоже с проверкой.
- Cookie в запросе — по имени, и пустая: `r.Cookie(name)`, тот же разбор, что у extractor'а и входа huma
  `cookie:"…"`; имя, которое `http.Cookie.Valid` отвергает, запрос не принёс бы — `ErrConfig`, а не молчащая проверка.
- Имена — значение графа `SessionCookies` (форма `PublicRoutes`), а не ENV: их задаёт код сервиса (константы его
  cookie), адреса — уже `HTTP_CORS_ORIGINS`.
- Без адреса в списке не стартует `Module` (`ErrConfig` в `OnStart`), а не `NewServer`: граф без старта —
  спецификация шаблона при конфиге по умолчанию, проверка маршрутов, `handlertest` — собирается без адреса, как граф
  схемы без extractor'а; без fx такой сервер отвечает 403 (fail-closed).
- `http_origin_rejections_total{path, reason}`: `reason` — `missing` или `not_allowed`; сам `Origin` — только в WARN
  (обрезан до 256 байт): заголовок пишет клиент.
- 403 в OpenAPI — у каждой операции изменяющего метода, а не только под cookie-схемой: cookie браузер прикладывает к
  запросу любого пути сервиса, и проверка его касается.

## Handoff
—

## Где мы сейчас
S1–S3 закрыты: проверка Origin в цепочке httpkit, сигналы, `handlertest.WithSessionCookies`, 403 в OpenAPI. Дальше —
S4: README кита, `configuration.md`, `usage.md`, `SECURITY.md`, корневой README.
