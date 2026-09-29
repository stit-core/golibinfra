---
status: in-progress
parent: go-backend-template:fix-reference-code
blocked-by: —
---

# harden-kits — фиксы китов по аудитам шаблона и релиз v1.2.0

## Цель
Закрыть в китах, которые импортирует `go-backend-template`, дефекты из аудита шаблона и fx-аудита:
жизненный цикл и fail-closed в `httpkit`, общий контракт ошибок API, явная обязательность
`timerkit`, миграции outbox-инстансов в графе, строгий `outboxtest`, `logkit.FxLogger` и
маскирование по умолчанию, атомарный `bruteforcekit`, помощник подписки `natskit`, предел
параллельности Argon2id. Итог — аннотированный тег `v1.2.0` в origin вместе с `main`.

## Контекст
- Аудит шаблона (`M/audit-report.html`: SEC-02, SEC-05, SEC-09, TST-01, COD-05, COD-06, дневник
  догфудера D02) и fx-аудит (`M/fx-audit.md`: FX-01, FX-03, FX-04, FX-05, FX-12; прототип P1 —
  `M/fxexp/zz_httpfix_proto.go.txt`); `M` — материалы программы `/home/mm/.cache/stit-audit-program/`.
- Программа, метод (исполнитель этапа → независимый валидатор) и политика push —
  `go-backend-template/.claude/plans/audit-program.md`: push `main` и тега — после PASS
  валидатора партии и `make gate-clean`.
- Объём — только киты, которые импортирует шаблон (logkit, pgkit, httpkit, clockkit, authnkit,
  testkit/{pgtest,handlertest,authntest,audittest,outboxtest}, outboxkit(+pgstore),
  auditkit(+pgstore), natskit, config, bizmetric, bruteforcekit(+pgstore), timerkit, healthkit,
  flagkit, cachekit). Неиспользуемые киты правятся, только если их ломает общий механизм.
- Потребители: `go-backend-template` на `v1.1.0` (обновится сам следующим этапом),
  `ai-gateway-template` и `amocrm-gateway-template` на `v0.39.0`. Шаблон ждёт этот релиз
  строками `backlog.md` (golibinfra-httpkit-fx, -timerkit-dep, -fxlogger, -outbox-migrations).
- fx v1.24: Invoke'и дочерних `fx.Module` выполняются раньше Invoke'ов уровня, куда они
  включены (`module.go:313`); хуки идут в порядке добавления, OnStop — в обратном; хук,
  добавленный во время Start, не запускается (`internal/lifecycle/lifecycle.go:205`).
- huma v2.39: `huma.NewError`/`NewErrorWithContext` — process-global; `defineErrors` берёт схему
  ошибок из `NewError(0, "")`; Transformer'ы видят каждое тело ответа до записи статуса;
  `HeadersError` даёт заголовки ответа-ошибки; OnAddOperation идёт после `defineErrors`.

## Не входит
- Правки в `go-backend-template` и шаблонах-шлюзах: миграция — по аннотации тега.
- Перенос I/O конструкторов `pgkit`/`natskit` в OnStart — см. «Решения».

## Критерии приёмки
- [ ] **AC1** — HTTP-сервер `httpkit.Module` начинает слушать после OnStart всех fx-модулей и гасится до их OnStop при любом месте `httpkit.Module` среди опций `fx.New`; запрос в полёте на остановке завершается при живых ресурсах. Проверка: `go test -short -race -run 'TestModule_Lifecycle' ./httpkit/` → PASS
- [ ] **AC2** — Операция с `Security` без `httpkit.AuthExtractor` в графе — ошибка старта с именами операций; `HumaAuth` без extractor'а отвечает на защищённую операцию `500 AUTH_NOT_CONFIGURED`, а не пропускает. Проверка: `go test -short -race -run 'TestModule_Auth|TestHumaAuth' ./httpkit/` → PASS
- [ ] **AC3** — Порядок middleware потребителя задаёт приложение одним значением `httpkit.Middlewares`; value group с перемешиваемым порядком нет. Проверка: `go test -short -race -run TestModule_Middlewares ./httpkit/` → PASS; `grep -rn MiddlewareGroup --include=*.go .` → пусто
- [ ] **AC4** — Любой ответ-ошибка сервера `httpkit` (сопоставленная и несопоставленная ошибка хендлера, валидация, auth, неизвестный маршрут, метод, лимит, panic) — `application/problem+json` с `code` и `request_id`, равным `X-Request-ID` и полю лога; 422 несёт `errors[]{location,code,message}`; 5xx без текста причины; 429 с `Retry-After`. Проверка: `go test -short -race -run TestErrorContract ./httpkit/` → PASS
- [ ] **AC5** — `httpkit.ErrorMap` сопоставляет доменную ошибку (`errors.Is` или предикат) со статусом, `code`, опц. полем и `Retry-After` из `RetryAfter()`; `Unmapped` перечисляет несопоставленные; несопоставленная даёт 500 и строку `handler error` с `operation` в логе; `Declare` кладёт статусы и коды в `openapi.json`. Проверка: `go test -short -race -run 'TestErrorMap' ./httpkit/` → PASS
- [x] **AC6** — `timerkit.Provide` без `timerkit.Module` в графе — ошибка сборки (`fx.ValidateApp`), в том числе у `outboxkit.Module`/`Instance` и `bruteforcekit/pgstore.Module`. Проверка: `go test -short -race -run 'RequiresTimerkit|RequiresModule' ./timerkit/ ./outboxkit/... ./bruteforcekit/...` → PASS
  - Подтверждено: `go test -short -race -run 'RequiresTimerkit|RequiresModule' ./timerkit/ ./outboxkit/... ./bruteforcekit/...` → PASS на `a4030bf`
- [x] **AC7** — `outboxkit.Instance(name, table)` кладёт схему таблицы в группу `pgkit.migrations`: `pgkit.MigrateModule` накатывает её без ручного списка. Проверка: `go test -short -race -run TestInstance_ProvidesMigrations ./outboxkit/` → PASS; `make test-full` (pgstore) → PASS
  - Подтверждено: `go test -short -race -run TestInstance_ProvidesMigrations ./outboxkit/` → PASS; `make test-full PKGS=./outboxkit/...` → PASS (`TestFactoryModule_InstanceOnCustomTable` заводит таблицу через `pgkit.MigrateModule`); мутант «Instance без группы» краснеет в обоих
- [x] **AC8** — `outboxtest.Recorder.Publish` вне транзакции возвращает `outboxkit.ErrNoTx` и событие не копит (мутант D02 «Publish вне транзакции» краснеет). Проверка: `go test -short -race ./testkit/outboxtest/` → PASS
  - Подтверждено: `go test -short -race ./testkit/outboxtest/` → PASS (`TestRecorder_PublishRequiresTx`); мутант D02 на ветке догфудера шаблона (`note/usecase.go`: `Publish(ctx, …)` вместо `txCtx`) — на v1.1.0 выжил, на этом golibinfra через `replace` в своём worktree — `TestCreate` FAIL
- [ ] **AC9** — `logkit.FxLogger` пишет события fx в структурный лог: причина падения старта — JSON-строка уровня ERROR, рутина — DEBUG. Проверка: `go test -short -race -run TestFxLogger ./logkit/` → PASS
- [ ] **AC10** — `NewRedactConfig` без env включает маскирование; значения под ключами `token`/`password`/`secret`/`authorization`/`cookie`/… и hex-цепочки ≥32 символов маскируются, числа не трогаются. Проверка: `go test -short -race -run 'TestRedact' ./logkit/` → PASS; `make envdoc-check` → OK
- [ ] **AC11** — `bruteforcekit`: subject нормализуется (trim, нижний регистр), IPv6 по умолчанию сворачивается до /64, `Guard.Attempt` атомарен: из N параллельных попыток пары проходят не больше `MaxFailures`. Проверка: `go test -short -race ./bruteforcekit/... ./testkit/bruteforcetest/` → PASS; `make test-full` → `TestStore_ConcurrentAttempts` PASS
- [ ] **AC12** — `natskit.SubscribeOnStart` подписывает в OnStart, дренит в OnStop; ошибка подписки (включая `ErrJetStreamDisabled`) валит старт. Проверка: `go test -short -race -run TestSubscribeOnStart ./natskit/` → PASS
- [ ] **AC13** — `Argon2idHasher` считает одновременно не больше `max(1, GOMAXPROCS/Threads)` хэшей; ожидание очереди прерывается ctx. Проверка: `go test -short -race -run TestArgon2id_Concurrency ./authnkit/` → PASS
- [ ] **AC14** — README и доки затронутых китов описывают текущее поведение, `make gate-clean` и `make test-full` зелёные, аннотированный тег `v1.2.0` с разделами `Breaking`/`Поведение`/`Изменения`/`Гейт` в origin вместе с `main`. Проверка: `make gate-clean` → зелёный; `git tag -l --format='%(contents)' v1.2.0` → четыре раздела; `git ls-remote --tags origin v1.2.0` → есть; `git status -sb` → синхронно с origin

## Шаги
### Инфраструктурные киты
- [x] **S1** — `timerkit.Provide` требует `timerkit.Module` (маркер `timerkit.Scheduler`); fx-тесты и доки зависимых китов
  - Проверка: `go test -short -race ./timerkit/ ./outboxkit/... ./bruteforcekit/... ./hubkit/...` → PASS · Закрывает: AC6
  - Сделано: `a4030bf` (`make check` зелёный)
- [x] **S2** — `outboxkit.RepositoryFactory` — интерфейс `Repository`+`Migrations`; `Instance` кладёт миграции в `pgkit.migrations`
  - Проверка: `go test -short -race ./outboxkit/...` → PASS · Закрывает: AC7 · Зависит от: S1
  - Сделано: `feat(outboxkit)!` — `go test -short -race ./outboxkit/...` → PASS, `make test-full PKGS=./outboxkit/...` → PASS, `make check` зелёный
- [x] **S3** — `outboxtest.Recorder` требует транзакцию (`pgkit.InTx`)
  - Проверка: `go test -short -race ./testkit/outboxtest/` → PASS · Закрывает: AC8
  - Сделано: `feat(outboxtest)!` — `go test -short -race ./testkit/outboxtest/` → PASS, `make check` зелёный
- [ ] **S4** — `logkit.FxLogger`
  - Проверка: `go test -short -race ./logkit/` → PASS · Закрывает: AC9
- [ ] **S5** — `logkit`: маскирование по ключу и hex-токенов, redact включён по умолчанию
  - Проверка: `go test -short -race ./logkit/`, `make envdoc-check` → PASS/OK · Закрывает: AC10
- [ ] **S6** — `bruteforcekit`: нормализация subject, IPv6 /64 по умолчанию
  - Проверка: `go test -short -race ./bruteforcekit/...` → PASS · Закрывает: AC11
- [ ] **S7** — `bruteforcekit`: атомарный `Guard.Attempt` вместо `Check`+`RecordFailure`; фейки `bruteforcetest`
  - Проверка: `go test -race ./bruteforcekit/... ./testkit/bruteforcetest/` (Docker) → PASS · Закрывает: AC11 · Зависит от: S6
- [ ] **S8** — `authnkit`: предел параллельности Argon2id
  - Проверка: `go test -short -race ./authnkit/` → PASS · Закрывает: AC13
- [ ] **S9** — `natskit.SubscribeOnStart`
  - Проверка: `go test -short -race ./natskit/` → PASS · Закрывает: AC12
### httpkit
- [ ] **S10** — Хук HTTP-сервера в Invoke уровня `httpkit.Module`: старт последним, остановка первой
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC1
- [ ] **S11** — Fail-closed auth: ошибка старта и `HumaAuth` без extractor'а
  - Проверка: `go test -short -race ./httpkit/ ./oidckit/ ./hubkit/...` → PASS · Закрывает: AC2 · Зависит от: S10
- [ ] **S12** — `httpkit.Middlewares` вместо группы `httpkit.middleware`
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC3 · Зависит от: S10
- [ ] **S13** — Контракт ошибок: problem+json с `code`/`request_id`/`errors[]`, `Retry-After`, 404/405 маршрутизатора; `handlertest`
  - Проверка: `go test -short -race ./httpkit/ ./testkit/handlertest/ ./oidckit/ ./cachekit/` → PASS · Закрывает: AC4 · Зависит от: S11
- [ ] **S14** — `ErrorMap` (`Map`/`Unmapped`/`Declare`) вместо `MapErr`/`MapIs`/`MapAs`, коды в OpenAPI
  - Проверка: `go test -short -race ./httpkit/` → PASS · Закрывает: AC5 · Зависит от: S13
### Релиз
- [ ] **S15** — Корневой README/CLAUDE.md сверены, `make test-full`, `make gate-clean`, тег `v1.2.0` локально; push `main` и тега — после PASS валидатора партии
  - Проверка: `make gate-clean` → зелёный, `make test-full` → PASS · Закрывает: AC14 · Зависит от: S1, S2, S3, S4, S5, S6, S7, S8, S9, S10, S11, S12, S13, S14

## Решения
- Все пункты подтверждены чтением кода, кроме FX-12 «I/O конструкторов под таймаутом» (ниже).
- FX-01: хук сервера регистрирует `fx.Invoke` уровня, куда включён `httpkit.Module`
  (`fx.Options(fx.Module("httpkit", …), fx.Invoke(serve))`), конструктор хуков не добавляет. fx
  выполняет Invoke'и всех `fx.Module` раньше, поэтому место `httpkit.Module` среди модулей не
  важно; прототип P1 (Invoke внутри модуля) требовал ставить модуль последним. Ограничение в
  доке: `httpkit.Module` — на верхнем уровне `fx.New`, корневой `fx.Invoke` с хуками — до него.
- FX-03: проверка — в OnStart сервера по операциям, собранным OnAddOperation; `HumaAuth(nil)`
  отвечает 500 на защищённую операцию (скрытые операции OnAddOperation не видит).
- FX-12 (middleware): одно необязательное значение `httpkit.Middlewares` в порядке слайса вместо
  группы — порядок явный по построению, сортировка и номера не нужны; группа удаляется.
- Контракт ошибок: `huma.NewError`/`NewErrorWithContext` подменяются в `init()` пакета — форма
  ошибок одна во всём процессе, включая `humatest` в тестах потребителя; `request_id` и лог
  причины 5xx — Transformer сервера (логгер сервера, без глобального указателя); поле ошибки из
  huma получает код `INVALID`, эхо `value` убрано (пароль не возвращается клиенту);
  `Retry-After` — из метода `RetryAfter()` в цепочке ошибки, у 429 не меньше секунды.
- `ErrorMap` — одна таблица на домен/операцию: `Map` в хендлере, `Declare` для OpenAPI (коды —
  в описание ответа статуса через OnAddOperation), `Unmapped` для теста полноты;
  `MapErr`/`MapIs`/`MapAs` удалены — один канонический способ.
- FX-04 обобщён: маркер требует `timerkit.Provide`, поэтому job без запускающего модуля
  невозможна у любого кита (outboxkit, bruteforcekit/pgstore, hubkit/sync).
- FX-12 (outbox): `RepositoryFactory` — интерфейс со схемой таблицы: `outboxkit` не может
  импортировать `pgstore` (цикл), а фабрика знает и хранилище, и его миграции.
- `outboxtest.Recorder` всегда требует транзакцию — как дефолт `OUTBOX_REQUIRE_TX=true`; фейк
  без переключателя.
- SEC-02: redact включается дефолтом `LOG_REDACT_ENABLED=true` (нулевой `RedactConfig{}` —
  выключен, как и был); ключи сравниваются по словам (`input_tokens` — не секрет), маскируются
  строки, группы и структуры под ключом, числа нет; hex-цепочка ≥32 (токены, SHA-1/256).
- SEC-05: попытка учитывается до проверки секрета (`Attempt`), успех сбрасывает окно — гонка
  «прочитал, потом записал» и забытый `RecordFailure` невозможны; upsert с `WHERE` не пишет
  строку при активной блокировке.
- SEC-09 подтверждён кодом: 64 МиБ на хэш без предела, логин анонимен; предел — семафор
  `max(1, GOMAXPROCS/Threads)` без настройки (lanes не больше ядер, пропускная способность та же).
- COD-06: `SubscribeOnStart(lc, subscribe)` принимает любую подписку; выключенный JetStream —
  ошибка старта, а не молчаливый пропуск.
- FX-12 «I/O конструкторов» отброшен: Ping пула ограничен `PG_CONNECT_TIMEOUT` (10s; `0` —
  документированный opt-out), `nats.Connect` — `NATS_CONNECT_TIMEOUT`, каждый вызов JetStream API
  в `ensureStreams` — 5s по умолчанию nats.go, зависший вызов обрывает старт ошибкой. Перенос в
  OnStart не убирает I/O из сборки графа (pgxpool прогревает `MinConns` в фоне) и для natskit
  ломает поддержанную подписку в `fx.Invoke` — несоразмерно.
- S2: два источника с одним именем `pgkit.MigrateUpAll` отвергает (`duplicate source name`) —
  ручной `pgkit.ProvideMigrations(pgstore.TableMigrations(t))` рядом с `Instance` валит старт
  громко; дедупликацию в pgkit не вводим (молча скрыла бы конфликт источников).
- S2: `pgstore.Module` и `pgstore.FactoryModule` сочетаются (граф и живой старт на Postgres) —
  строка доков «не сочетаются» была неправдой, исправлена рядом (`usage.md`, doc-comment).
- S2: юнит-тест группы — на фейковой фабрике: реальная регистрирует backlog-gauge'и, которые
  без старта не снять; связку с pgstore держит integration-тест через `pgkit.MigrateModule`.
- S3: ошибка фейка — та же обёртка, что у pgstore (`ErrNoTx` + подсказка про
  `Transactor.WithTx`); `Recorder` импортирует `pgkit` ради `InTx` — фейк и хранилище
  решают «в транзакции ли» одной функцией.
- Задеты неиспользуемые шаблоном киты: `testkit/bruteforcetest` (интерфейс `Guard`),
  тесты `hubkit/sync` (требование `timerkit.Module`), при необходимости `oidckit`/`hubkit/sso`
  (контракт ошибок `httpkit`).

## Открытые вопросы
—

## Handoff
- `go-backend-template` при bump: `beforeHTTP` не нужен; client-IP — `httpkit.Middlewares`;
  `MapErr` → `ErrorMap.Map` + `Declare`; `LoginGuard` — `Attempt` до проверки пароля +
  `RecordSuccess`; subscriber — `natskit.SubscribeOnStart`; `NATS_JETSTREAM_ENABLED=true` в
  `.env.example`; `logkit.FxLogger` + `fx.RecoverFromPanics()` в `main`; `openapi.json`
  перегенерировать (схема `APIError`); логи маскируются по умолчанию.
- Шлюзы (`v0.39.0`): цепочка аннотаций тегов до `v1.2.0`.

## Где мы сейчас
- Сделано: план (`bc726e3`), S1 (`a4030bf`, `feat(timerkit)!`) — маркер `timerkit.Scheduler`;
  S2 (`887527a`, `feat(outboxkit)!`) — `RepositoryFactory` стал интерфейсом (`Repository`,
  `Migrations`), `Instance` кладёт `pgkit.Source` таблицы в `pgkit.MigrationsGroup`;
  S3 (`feat(outboxtest)!`) — `Recorder.Publish` вне `pgkit.InTx` → `outboxkit.ErrNoTx`.
- Осталось: S4–S15 по «Шагам», дизайн каждого — в «Решениях». Тега нет; ветка не запушена.
- Следующий шаг — S4: `logkit.FxLogger` — `fx.WithLogger` через `fxevent.SlogLogger` поверх
  `*slog.Logger` из графа; рутина — DEBUG, ошибки — ERROR (FX-05); тест `TestFxLogger`.
- Для аннотации тега копить по шагам:
  - Breaking — `timerkit.Provide` требует `timerkit.Module` (S1); `outboxkit.RepositoryFactory` —
    интерфейс `Repository(table)`/`Migrations(table)` вместо func-типа, своя фабрика реализует оба
    метода; `Instance` сам регистрирует схему таблицы — ручной
    `pgkit.ProvideMigrations(pgstore.TableMigrations(t))` рядом с ним убрать, иначе
    `MigrateModule` падает `duplicate source name` (S2); `outboxtest.Recorder.Publish` вне
    транзакции → `outboxkit.ErrNoTx`: usecase в тесте — с фейком `pgtest.Identity`/`Recorder`
    или ctx под `pgkit.WithTestTx` (S3).
  - Поведение — с `pgkit.MigrateModule` таблицы инстансов накатываются на старте сами (S2).
  - Изменения — `pgstore.Module` и `FactoryModule` сочетаются в одном графе (доки, S2).
- Перед коммитом — `make check` (govulncheck однажды завис на сети — перезапуск помог).
